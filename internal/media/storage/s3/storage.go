package s3

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/config"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type Storage struct {
	client    *awss3.Client
	presigner *awss3.PresignClient
	timeout   time.Duration
	bucket    string
}

// New использует внутренний endpoint для запросов сервиса и public_endpoint
// для подписания клиентских ссылок. Адреса уже подписанных ссылок не изменяются.
func New(cfg config.S3) (*Storage, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	awsConfig := aws.Config{
		Region:                     cfg.Region,
		Credentials:                aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		HTTPClient:                 &http.Client{Timeout: cfg.RequestTimeout},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}
	newClient := func(endpoint string) *awss3.Client {
		return awss3.NewFromConfig(awsConfig, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = cfg.UsePathStyle
		})
	}
	return &Storage{client: newClient(cfg.Endpoint), presigner: awss3.NewPresignClient(newClient(cfg.PublicEndpoint)), timeout: cfg.RequestTimeout, bucket: cfg.Bucket}, nil
}

// Check проверяет существующий bucket; Media не создаёт bucket и не настраивает IAM.
func (s *Storage) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return storageError(ctx, err)
}

func (s *Storage) PresignUpload(ctx context.Context, input domain.UploadInput) (domain.SignedRequest, error) {
	if input.Bucket == "" || input.Key == "" || input.ContentType == "" || input.SizeBytes <= 0 || !validTTL(input.TTL) {
		return domain.SignedRequest{}, domain.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	expiresAt := time.Now().UTC().Truncate(time.Second).Add(input.TTL)
	result, err := s.presigner.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(input.Bucket), Key: aws.String(input.Key),
		ContentType: aws.String(input.ContentType), ContentLength: aws.Int64(input.SizeBytes),
		IfNoneMatch: aws.String("*"),
	}, func(o *awss3.PresignOptions) { o.Expires = input.TTL })
	if err != nil {
		return domain.SignedRequest{}, storageError(ctx, err)
	}
	return signedRequest(result, expiresAt), nil
}

func (s *Storage) HeadObject(ctx context.Context, bucket, key string) (domain.ObjectInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return domain.ObjectInfo{}, domain.ErrObjectNotFound
		}
		return domain.ObjectInfo{}, storageError(ctx, err)
	}
	if result.ContentLength == nil {
		return domain.ObjectInfo{}, fmt.Errorf("%w: S3 не вернул размер объекта", domain.ErrStorageUnavailable)
	}
	object := domain.ObjectInfo{SizeBytes: *result.ContentLength, ContentType: aws.ToString(result.ContentType), ETag: result.ETag}
	// В БД хранится hex SHA-256 всего объекта, а не multipart/composite checksum.
	if result.ChecksumSHA256 != nil && result.ChecksumType != types.ChecksumTypeComposite {
		decoded, err := base64.StdEncoding.DecodeString(*result.ChecksumSHA256)
		if err == nil && len(decoded) == 32 {
			checksum := hex.EncodeToString(decoded)
			object.ChecksumSHA256 = &checksum
		}
	}
	return object, nil
}

func (s *Storage) PresignDownload(ctx context.Context, input domain.DownloadInput) (domain.SignedRequest, error) {
	if input.Bucket == "" || input.Key == "" || !validTTL(input.TTL) {
		return domain.SignedRequest{}, domain.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	expiresAt := time.Now().UTC().Truncate(time.Second).Add(input.TTL)
	result, err := s.presigner.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(input.Bucket), Key: aws.String(input.Key),
		ResponseContentDisposition: aws.String(mime.FormatMediaType("attachment", map[string]string{"filename": input.OriginalName})),
	}, func(o *awss3.PresignOptions) { o.Expires = input.TTL })
	if err != nil {
		return domain.SignedRequest{}, storageError(ctx, err)
	}
	return signedRequest(result, expiresAt), nil
}

func validTTL(ttl time.Duration) bool {
	return ttl >= time.Second && ttl <= 7*24*time.Hour && ttl%time.Second == 0
}

func signedRequest(result *v4.PresignedHTTPRequest, expiresAt time.Time) domain.SignedRequest {
	headers := make(map[string]string)
	for name, values := range result.SignedHeader {
		// Host определяется URL, браузер не позволяет задавать его вручную.
		if !strings.EqualFold(name, "Host") {
			headers[name] = strings.Join(values, ",")
		}
	}
	return domain.SignedRequest{URL: result.URL, Method: result.Method, Headers: headers, ExpiresAt: expiresAt}
}

func storageError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %w", domain.ErrStorageUnavailable, err)
}
