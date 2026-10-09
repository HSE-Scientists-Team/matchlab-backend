package usecase

import (
	"context"
	"fmt"
	"mime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/repository"
	"github.com/google/uuid"
)

type ObjectStorage interface {
	PresignUpload(context.Context, domain.UploadInput) (domain.SignedRequest, error)
	HeadObject(context.Context, string, string) (domain.ObjectInfo, error)
	PresignDownload(context.Context, domain.DownloadInput) (domain.SignedRequest, error)
}

type Options struct {
	Bucket              string
	MaxSizeBytes        int64
	AllowedContentTypes []string
	UploadTTL           time.Duration
	DownloadTTL         time.Duration
}

type Service struct {
	files        repository.Files
	storage      ObjectStorage
	options      Options
	allowedTypes map[string]bool
}

func NewService(files repository.Files, storage ObjectStorage, options Options) (*Service, error) {
	if files == nil || storage == nil || strings.TrimSpace(options.Bucket) == "" || options.MaxSizeBytes <= 0 || !validTTL(options.UploadTTL) || !validTTL(options.DownloadTTL) {
		return nil, fmt.Errorf("требуются repository, storage, bucket, положительный лимит размера и TTL до 7 дней")
	}
	allowed := make(map[string]bool)
	for _, value := range options.AllowedContentTypes {
		typ, params, err := mime.ParseMediaType(value)
		if err != nil || len(params) != 0 || strings.Contains(typ, "*") || strings.Count(typ, "/") != 1 {
			return nil, fmt.Errorf("некорректный разрешённый Content-Type: %q", value)
		}
		allowed[typ] = true
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("требуется список разрешённых Content-Type")
	}
	return &Service{files: files, storage: storage, options: options, allowedTypes: allowed}, nil
}

func validTTL(ttl time.Duration) bool {
	return ttl >= time.Second && ttl <= 7*24*time.Hour && ttl%time.Second == 0
}

func normalizeID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("%w: требуется непустой UUID", domain.ErrInvalidArgument)
	}
	return id.String(), nil
}

func (s *Service) CreateUpload(ctx context.Context, userID, originalName, contentType string, sizeBytes int64, isPublic bool) (domain.Upload, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return domain.Upload{}, err
	}
	if !utf8.ValidString(originalName) || strings.TrimSpace(originalName) == "" || originalName == "." || originalName == ".." || utf8.RuneCountInString(originalName) > 255 || strings.ContainsAny(originalName, "/\\") || strings.IndexFunc(originalName, unicode.IsControl) >= 0 {
		return domain.Upload{}, fmt.Errorf("%w: требуется имя файла до 255 символов без пути и управляющих символов", domain.ErrInvalidArgument)
	}
	typ, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 || !s.allowedTypes[typ] {
		return domain.Upload{}, fmt.Errorf("%w: недопустимый Content-Type", domain.ErrInvalidArgument)
	}
	if sizeBytes <= 0 || sizeBytes > s.options.MaxSizeBytes {
		return domain.Upload{}, fmt.Errorf("%w: размер вне допустимого диапазона", domain.ErrInvalidArgument)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return domain.Upload{}, fmt.Errorf("создание ID файла: %w", err)
	}
	input := domain.NewFile{ID: id.String(), OwnerUserID: userID, IsPublic: isPublic, BucketName: s.options.Bucket,
		ObjectKey: "users/" + userID + "/" + id.String(), OriginalName: originalName, ContentType: typ}
	request, err := s.storage.PresignUpload(ctx, domain.UploadInput{Bucket: input.BucketName, Key: input.ObjectKey, ContentType: typ, SizeBytes: sizeBytes, TTL: s.options.UploadTTL})
	if err != nil {
		return domain.Upload{}, err
	}
	file, err := s.files.CreatePending(ctx, input)
	if err != nil {
		return domain.Upload{}, err
	}
	return domain.Upload{File: file, Request: request}, nil
}

func (s *Service) GetFile(ctx context.Context, fileID, userID string) (domain.File, error) {
	fileID, err := normalizeID(fileID)
	if err != nil {
		return domain.File{}, err
	}
	if userID != "" {
		userID, err = normalizeID(userID)
		if err != nil {
			return domain.File{}, err
		}
	}
	file, err := s.files.FindReadable(ctx, fileID, userID)
	if err != nil {
		return domain.File{}, err
	}
	if file.Status == domain.FileStatusDeleted {
		return domain.File{}, domain.ErrNotFound
	}
	return file, nil
}

func (s *Service) CompleteUpload(ctx context.Context, fileID, userID string) (domain.File, error) {
	fileID, err := normalizeID(fileID)
	if err != nil {
		return domain.File{}, err
	}
	userID, err = normalizeID(userID)
	if err != nil {
		return domain.File{}, err
	}
	file, err := s.files.FindOwned(ctx, fileID, userID)
	if err != nil {
		return domain.File{}, err
	}
	if file.Status == domain.FileStatusDeleted {
		return domain.File{}, domain.ErrNotFound
	}
	if file.Status == domain.FileStatusReady {
		return file, nil
	}
	if file.Status != domain.FileStatusPending {
		return domain.File{}, domain.ErrInvalidStatus
	}
	object, err := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
	if err != nil {
		return domain.File{}, err
	}
	typ, params, err := mime.ParseMediaType(object.ContentType)
	if err != nil || len(params) != 0 || !s.allowedTypes[typ] || typ != file.ContentType || object.SizeBytes < 0 || object.SizeBytes > s.options.MaxSizeBytes {
		return domain.File{}, domain.ErrObjectMismatch
	}
	object.ContentType = typ
	return s.files.MarkReady(ctx, file.ID, file.OwnerUserID, object)
}

func (s *Service) CreateDownloadURL(ctx context.Context, fileID, userID string) (domain.SignedRequest, error) {
	file, err := s.GetFile(ctx, fileID, userID)
	if err != nil {
		return domain.SignedRequest{}, err
	}
	if file.Status != domain.FileStatusReady {
		return domain.SignedRequest{}, domain.ErrInvalidStatus
	}
	return s.storage.PresignDownload(ctx, domain.DownloadInput{Bucket: file.BucketName, Key: file.ObjectKey, OriginalName: file.OriginalName, TTL: s.options.DownloadTTL})
}
