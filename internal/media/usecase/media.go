package usecase

import (
	"context"
	"errors"
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
	MultipartStorage
	DeleteObject(context.Context, string, string) error
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
	MultipartPartSize   int64
	MultipartTTL        time.Duration
}

type Service struct {
	files        repository.Files
	storage      ObjectStorage
	options      Options
	allowedTypes map[string]bool
}

const pendingMaxIdle = 24 * time.Hour

// RecoverPending visits all pending files last updated at least 24 hours ago.
// Temporary dependency failures leave their state intact for the next tick.
func (s *Service) RecoverPending(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-pendingMaxIdle)
	var failures []error
	after := ""
	for {
		files, err := s.files.StalePendingFiles(ctx, cutoff, after)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if len(files) == 0 {
			break
		}
		for _, file := range files {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.recoverPendingFile(callCtx, file.FileID, file.UserID, cutoff)
			cancel()
			if err != nil {
				failures = append(failures, fmt.Errorf("recover pending: %w", err))
			}
			if ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
		}
		after = files[len(files)-1].FileID
	}
	return errors.Join(failures...)
}

func (s *Service) validateUploadedObject(file *domain.File, upload *domain.Multipart, object domain.ObjectInfo) error {
	typ, params, err := mime.ParseMediaType(object.ContentType)
	if err != nil || len(params) != 0 || !s.allowedTypes[typ] || typ != file.ContentType || object.SizeBytes <= 0 || object.SizeBytes > s.options.MaxSizeBytes {
		return domain.ErrObjectMismatch
	}
	if file.ExpectedSizeBytes != nil && object.SizeBytes != *file.ExpectedSizeBytes {
		return domain.ErrObjectMismatch
	}
	if upload != nil && object.SizeBytes != upload.ExpectedSize {
		return domain.ErrObjectMismatch
	}
	return nil
}

func markRecoveredReady(file *domain.File, upload *domain.Multipart, object domain.ObjectInfo) {
	now := time.Now().UTC()
	file.Status, file.SizeBytes, file.ETag, file.ChecksumSHA256, file.UploadedAt = domain.FileStatusReady, &object.SizeBytes, object.ETag, object.ChecksumSHA256, &now
	if upload != nil {
		upload.Status = domain.MultipartCompleted
	}
}

func (s *Service) recoverPendingFile(ctx context.Context, id, owner string, cutoff time.Time) error {
	return s.files.WithStalePending(ctx, id, owner, cutoff, func(file *domain.File, upload *domain.Multipart) error {
		object, err := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
		if err == nil && s.validateUploadedObject(file, upload, object) == nil {
			markRecoveredReady(file, upload, object)
			return nil
		}
		if err != nil && !errors.Is(err, domain.ErrObjectNotFound) {
			return err
		}
		// An absent final object may still have a complete set of S3 parts.
		if errors.Is(err, domain.ErrObjectNotFound) && upload != nil && (upload.Status == domain.MultipartUploading || upload.Status == domain.MultipartCompleting) {
			manifest := upload.Manifest
			if upload.Status == domain.MultipartUploading {
				parts, listErr := s.storage.ListParts(ctx, file.BucketName, file.ObjectKey, upload.UploadID)
				if listErr != nil && !errors.Is(listErr, domain.ErrNotFound) {
					return listErr
				}
				manifest = recoverableManifest(upload, parts)
			}
			if len(manifest) > 0 {
				finishErr := s.storage.FinishMultipart(ctx, file.BucketName, file.ObjectKey, upload.UploadID, manifest)
				if finishErr != nil && !errors.Is(finishErr, domain.ErrObjectMismatch) && !errors.Is(finishErr, domain.ErrNotFound) {
					return finishErr
				}
				// Recheck after NoSuchUpload too: a previous complete may have succeeded.
				object, headErr := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
				if headErr == nil && s.validateUploadedObject(file, upload, object) == nil {
					upload.Manifest = manifest
					markRecoveredReady(file, upload, object)
					return nil
				}
				if headErr != nil && !errors.Is(headErr, domain.ErrObjectNotFound) {
					return headErr
				}
				// A successful Complete followed by a missing HEAD can be transient.
				if finishErr == nil && errors.Is(headErr, domain.ErrObjectNotFound) {
					return headErr
				}
			}
		}
		if upload != nil {
			if err := s.storage.AbortMultipart(ctx, file.BucketName, file.ObjectKey, upload.UploadID); err != nil {
				return err
			}
		}
		if err := s.storage.DeleteObject(ctx, file.BucketName, file.ObjectKey); err != nil {
			return err
		}
		file.Status = domain.FileStatusFailed
		if upload != nil {
			upload.Status, upload.Manifest = domain.MultipartAborted, nil
		}
		return nil
	})
}

func NewService(files repository.Files, storage ObjectStorage, options Options) (*Service, error) {
	if options.MultipartPartSize == 0 {
		options.MultipartPartSize = 8 * 1024 * 1024
	}
	if options.MultipartTTL == 0 {
		options.MultipartTTL = 24 * time.Hour
	}
	if options.MultipartPartSize < domain.MinPartSize || options.MultipartPartSize > domain.MaxPartSize || !validTTL(options.MultipartTTL) || options.MaxSizeBytes > options.MultipartPartSize*domain.MaxParts {
		return nil, fmt.Errorf("некорректные размер части, срок или максимальный размер multipart-загрузки")
	}
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
	input, err := s.newFile(userID, originalName, contentType, sizeBytes, isPublic)
	if err != nil {
		return domain.Upload{}, err
	}
	request, err := s.storage.PresignUpload(ctx, domain.UploadInput{Bucket: input.BucketName, Key: input.ObjectKey, ContentType: input.ContentType, SizeBytes: sizeBytes, TTL: s.options.UploadTTL})
	if err != nil {
		return domain.Upload{}, err
	}
	file, err := s.files.CreatePending(ctx, input)
	if err != nil {
		return domain.Upload{}, err
	}
	return domain.Upload{File: file, Request: request}, nil
}

func (s *Service) newFile(userID, originalName, contentType string, sizeBytes int64, isPublic bool) (domain.NewFile, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return domain.NewFile{}, err
	}
	if !utf8.ValidString(originalName) || strings.TrimSpace(originalName) == "" || originalName == "." || originalName == ".." || utf8.RuneCountInString(originalName) > 255 || strings.ContainsAny(originalName, "/\\") || strings.IndexFunc(originalName, unicode.IsControl) >= 0 {
		return domain.NewFile{}, fmt.Errorf("%w: требуется имя файла до 255 символов без пути и управляющих символов", domain.ErrInvalidArgument)
	}
	typ, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 || !s.allowedTypes[typ] {
		return domain.NewFile{}, fmt.Errorf("%w: недопустимый Content-Type", domain.ErrInvalidArgument)
	}
	if sizeBytes <= 0 || sizeBytes > s.options.MaxSizeBytes {
		return domain.NewFile{}, fmt.Errorf("%w: размер вне допустимого диапазона", domain.ErrInvalidArgument)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return domain.NewFile{}, fmt.Errorf("создание ID файла: %w", err)
	}
	input := domain.NewFile{ID: id.String(), OwnerUserID: userID, IsPublic: isPublic, BucketName: s.options.Bucket, ExpectedSizeBytes: sizeBytes,
		ObjectKey: "users/" + userID + "/" + id.String(), OriginalName: originalName, ContentType: typ}
	return input, nil
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
	if file.Status == domain.FileStatusDeleted || file.Status == domain.FileStatusDeleting {
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
	multipart, err := s.files.IsMultipart(ctx, file.ID)
	if err != nil {
		return domain.File{}, err
	}
	if multipart {
		return domain.File{}, domain.ErrInvalidStatus
	}
	object, err := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
	if err != nil {
		return domain.File{}, err
	}
	if err := s.validateUploadedObject(&file, nil, object); err != nil {
		return domain.File{}, err
	}
	object.ContentType = file.ContentType
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

// DeleteFile always checks ownership, including for public and deleted files.
func (s *Service) DeleteFile(ctx context.Context, id, owner string) error {
	id, err := normalizeID(id)
	if err != nil {
		return err
	}
	owner, err = normalizeID(owner)
	if err != nil {
		return err
	}
	file, err := s.files.BeginDeletion(ctx, id, owner)
	if err != nil {
		return err
	}
	if file.Status == domain.FileStatusDeleted {
		return nil
	}
	if err := s.storage.DeleteObject(ctx, file.BucketName, file.ObjectKey); err != nil {
		return err
	}
	return s.files.FinishDeletion(ctx, id, owner)
}

// RecoverDeletions retries S3 deletion after failures or process restarts.
func (s *Service) RecoverDeletions(ctx context.Context) error {
	files, err := s.files.DeletingFiles(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, file := range files {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.DeleteFile(callCtx, file.FileID, file.UserID)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("recover deletion: %w", err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}
