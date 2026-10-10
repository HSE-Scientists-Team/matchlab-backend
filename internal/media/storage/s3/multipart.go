package s3

import (
	"context"
	"errors"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func multipartError(ctx context.Context, err error) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchUpload":
			return domain.ErrNotFound
		case "InvalidPart", "InvalidPartOrder", "EntityTooSmall":
			return domain.ErrObjectMismatch
		}
	}
	return storageError(ctx, err)
}

func (s *Storage) StartMultipart(ctx context.Context, bucket, key, contentType string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := s.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String(contentType)}, func(o *awss3.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return "", multipartError(ctx, err)
	}
	if aws.ToString(result.UploadId) == "" {
		return "", domain.ErrStorageUnavailable
	}
	return *result.UploadId, nil
}

func (s *Storage) PresignPart(ctx context.Context, bucket, key, uploadID string, number int32, size int64, ttl time.Duration) (domain.SignedRequest, error) {
	if bucket == "" || key == "" || uploadID == "" || number < 1 || number > domain.MaxParts || size <= 0 || size > domain.MaxPartSize || !validTTL(ttl) {
		return domain.SignedRequest{}, domain.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	expires := time.Now().UTC().Truncate(time.Second).Add(ttl)
	result, err := s.presigner.PresignUploadPart(ctx, &awss3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(number), ContentLength: aws.Int64(size)}, func(o *awss3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return domain.SignedRequest{}, multipartError(ctx, err)
	}
	return signedRequest(result, expires), nil
}

func (s *Storage) ListParts(ctx context.Context, bucket, key, uploadID string) ([]domain.Part, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	pages := awss3.NewListPartsPaginator(s.client, &awss3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	parts := []domain.Part{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, multipartError(ctx, err)
		}
		for _, part := range page.Parts {
			parts = append(parts, domain.Part{Number: aws.ToInt32(part.PartNumber), ETag: aws.ToString(part.ETag), SizeBytes: aws.ToInt64(part.Size)})
		}
		if len(parts) > domain.MaxParts {
			return nil, domain.ErrObjectMismatch
		}
	}
	return parts, nil
}

func (s *Storage) FinishMultipart(ctx context.Context, bucket, key, uploadID string, parts []domain.Part) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, types.CompletedPart{PartNumber: aws.Int32(part.Number), ETag: aws.String(part.ETag)})
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}}, func(o *awss3.Options) { o.Retryer = aws.NopRetryer{} })
	return multipartError(ctx, err)
}

func (s *Storage) AbortMultipart(ctx context.Context, bucket, key, uploadID string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	err = multipartError(ctx, err)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}
