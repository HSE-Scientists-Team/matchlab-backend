package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

type MultipartStorage interface {
	StartMultipart(context.Context, string, string, string) (string, error)
	PresignPart(context.Context, string, string, string, int32, int64, time.Duration) (domain.SignedRequest, error)
	ListParts(context.Context, string, string, string) ([]domain.Part, error)
	FinishMultipart(context.Context, string, string, string, []domain.Part) error
	AbortMultipart(context.Context, string, string, string) error
}

func (s *Service) CreateMultipart(ctx context.Context, user, name, typ string, size int64, public bool) (domain.MultipartState, error) {
	input, err := s.newFile(user, name, typ, size, public)
	if err != nil {
		return domain.MultipartState{}, err
	}
	id, err := s.storage.StartMultipart(ctx, input.BucketName, input.ObjectKey, input.ContentType)
	if err != nil {
		return domain.MultipartState{}, err
	}
	upload := domain.Multipart{FileID: input.ID, UploadID: id, ExpectedSize: size, PartSize: s.options.MultipartPartSize, Status: domain.MultipartUploading, ExpiresAt: time.Now().UTC().Add(s.options.MultipartTTL)}
	file, err := s.files.CreateMultipart(ctx, input, upload)
	if err != nil {
		// The request may have expired; use a separate bounded cleanup context.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		abortErr := s.storage.AbortMultipart(cleanup, input.BucketName, input.ObjectKey, id)
		return domain.MultipartState{}, errors.Join(err, abortErr)
	}
	return domain.MultipartState{File: file, Upload: upload, Parts: []domain.Part{}}, nil
}

func (s *Service) withMultipart(ctx context.Context, id, user string, fn func(*domain.File, *domain.Multipart) error) error {
	id, err := normalizeID(id)
	if err != nil {
		return err
	}
	user, err = normalizeID(user)
	if err != nil {
		return err
	}
	return s.files.WithMultipart(ctx, id, user, fn)
}

func activeUpload(file *domain.File, upload *domain.Multipart) error {
	if file.Status != domain.FileStatusPending || upload.Status != domain.MultipartUploading || !time.Now().Before(upload.ExpiresAt) {
		return domain.ErrInvalidStatus
	}
	return nil
}

func (s *Service) PartURLs(ctx context.Context, id, user string, numbers []int32) ([]domain.PartURL, error) {
	if len(numbers) == 0 || len(numbers) > 100 {
		return nil, domain.ErrInvalidArgument
	}
	result := make([]domain.PartURL, 0, len(numbers))
	err := s.withMultipart(ctx, id, user, func(file *domain.File, upload *domain.Multipart) error {
		if err := activeUpload(file, upload); err != nil {
			return err
		}
		seen := map[int32]bool{}
		for _, number := range numbers {
			if number < 1 || number > upload.PartCount() || seen[number] {
				return domain.ErrInvalidArgument
			}
			seen[number] = true
		}
		ttl := s.options.UploadTTL
		remaining := time.Until(upload.ExpiresAt).Truncate(time.Second)
		if remaining < ttl {
			ttl = remaining
		}
		if ttl < time.Second {
			return domain.ErrInvalidStatus
		}
		for _, number := range numbers {
			size := upload.SizeOfPart(number)
			request, err := s.storage.PresignPart(ctx, file.BucketName, file.ObjectKey, upload.UploadID, number, size, ttl)
			if err != nil {
				return err
			}
			result = append(result, domain.PartURL{Number: number, SizeBytes: size, Request: request})
		}
		return nil
	})
	return result, err
}

func (s *Service) GetMultipart(ctx context.Context, id, user string) (domain.MultipartState, error) {
	var result domain.MultipartState
	err := s.withMultipart(ctx, id, user, func(file *domain.File, upload *domain.Multipart) error {
		result = domain.MultipartState{File: *file, Upload: *upload, Parts: []domain.Part{}}
		if upload.Status == domain.MultipartCompleted {
			result.UploadedBytes = upload.ExpectedSize
			return nil
		}
		if upload.Status == domain.MultipartAborted {
			return nil
		}
		parts, err := s.storage.ListParts(ctx, file.BucketName, file.ObjectKey, upload.UploadID)
		if errors.Is(err, domain.ErrNotFound) && upload.Status == domain.MultipartCompleting {
			object, headErr := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
			if headErr == nil {
				if object.SizeBytes != upload.ExpectedSize || object.ContentType != file.ContentType {
					return domain.ErrObjectMismatch
				}
				result.UploadedBytes = upload.ExpectedSize
				return nil
			}
			if !errors.Is(headErr, domain.ErrObjectNotFound) {
				return headErr
			}
			return nil
		}
		if err != nil {
			return err
		}
		seen := map[int32]bool{}
		for _, part := range parts {
			if part.Number < 1 || part.Number > upload.PartCount() || part.SizeBytes != upload.SizeOfPart(part.Number) {
				return domain.ErrObjectMismatch
			}
			if !seen[part.Number] {
				result.UploadedBytes += part.SizeBytes
				seen[part.Number] = true
			}
		}
		result.Parts = parts
		return nil
	})
	return result, err
}

func validateManifest(upload *domain.Multipart, manifest, actual []domain.Part) error {
	if len(manifest) != int(upload.PartCount()) {
		return domain.ErrObjectMismatch
	}
	// SeaweedFS 4.47 may list old and new ETags for a replaced part.
	// Validate the client's acknowledged ETag rather than guessing the latest.
	actualByNumber := map[int32]map[string]int64{}
	for _, part := range actual {
		if part.Number < 1 || part.Number > upload.PartCount() {
			return domain.ErrObjectMismatch
		}
		if actualByNumber[part.Number] == nil {
			actualByNumber[part.Number] = map[string]int64{}
		}
		actualByNumber[part.Number][part.ETag] = part.SizeBytes
	}
	for i, part := range manifest {
		size, ok := actualByNumber[part.Number][part.ETag]
		if part.Number != int32(i+1) || part.ETag == "" || !ok || size != upload.SizeOfPart(part.Number) {
			return domain.ErrObjectMismatch
		}
	}
	return nil
}

// Without a client manifest, different ETags for one number are ambiguous.
// Never choose an arbitrary version of a replaced part during recovery.
func recoverableManifest(upload *domain.Multipart, parts []domain.Part) []domain.Part {
	byNumber := make(map[int32]domain.Part, len(parts))
	for _, part := range parts {
		if part.Number < 1 || part.Number > upload.PartCount() || part.ETag == "" || part.SizeBytes != upload.SizeOfPart(part.Number) {
			return nil
		}
		if old, exists := byNumber[part.Number]; exists && old.ETag != part.ETag {
			return nil
		}
		byNumber[part.Number] = part
	}
	if len(byNumber) != int(upload.PartCount()) {
		return nil
	}
	manifest := make([]domain.Part, upload.PartCount())
	for i := range manifest {
		manifest[i] = byNumber[int32(i+1)]
	}
	return manifest
}

func (s *Service) CompleteMultipart(ctx context.Context, id, user string, parts []domain.Part) (domain.File, error) {
	if len(parts) == 0 || len(parts) > domain.MaxParts {
		return domain.File{}, domain.ErrInvalidArgument
	}
	manifest := append([]domain.Part(nil), parts...)
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Number < manifest[j].Number })
	// Commit the manifest before touching S3; any replica can resume completion.
	err := s.withMultipart(ctx, id, user, func(file *domain.File, upload *domain.Multipart) error {
		if upload.Status == domain.MultipartCompleted {
			return nil
		}
		if upload.Status == domain.MultipartCompleting {
			if len(manifest) != len(upload.Manifest) {
				return domain.ErrInvalidStatus
			}
			for i, part := range manifest {
				if part.Number != upload.Manifest[i].Number || part.ETag != upload.Manifest[i].ETag {
					return domain.ErrInvalidStatus
				}
			}
			return nil
		}
		if err := activeUpload(file, upload); err != nil {
			return err
		}
		actual, err := s.storage.ListParts(ctx, file.BucketName, file.ObjectKey, upload.UploadID)
		if err != nil {
			return err
		}
		if err = validateManifest(upload, manifest, actual); err != nil {
			return err
		}
		upload.Manifest = manifest
		upload.Status = domain.MultipartCompleting
		return nil
	})
	if err != nil {
		return domain.File{}, err
	}
	return s.finishMultipart(ctx, id, user)
}

func (s *Service) finishMultipart(ctx context.Context, id, user string) (domain.File, error) {
	var result domain.File
	var completionErr error
	err := s.withMultipart(ctx, id, user, func(file *domain.File, upload *domain.Multipart) error {
		if upload.Status == domain.MultipartCompleted {
			result = *file
			return nil
		}
		if upload.Status != domain.MultipartCompleting {
			return domain.ErrInvalidStatus
		}
		object, err := s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
		if errors.Is(err, domain.ErrObjectNotFound) {
			if err = s.storage.FinishMultipart(ctx, file.BucketName, file.ObjectKey, upload.UploadID, upload.Manifest); err != nil {
				// A part may have been replaced via an already issued URL.
				// A definitive InvalidPart response permits a fresh manifest.
				if errors.Is(err, domain.ErrObjectMismatch) {
					upload.Status = domain.MultipartUploading
					upload.Manifest = nil
					completionErr = err
					return nil
				}
				if errors.Is(err, domain.ErrNotFound) {
					// Another S3 request may have succeeded after its caller lost the lock.
					object, err = s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
					if errors.Is(err, domain.ErrObjectNotFound) && !time.Now().Before(upload.ExpiresAt) {
						upload.Status = domain.MultipartAborted
						file.Status = domain.FileStatusFailed
						completionErr = err
						return nil
					}
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}
			object, err = s.storage.HeadObject(ctx, file.BucketName, file.ObjectKey)
		}
		if err != nil {
			return err
		}
		if object.SizeBytes != upload.ExpectedSize || object.ContentType != file.ContentType {
			return domain.ErrObjectMismatch
		}
		now := time.Now().UTC()
		file.Status = domain.FileStatusReady
		file.SizeBytes = &object.SizeBytes
		file.ETag = object.ETag
		file.ChecksumSHA256 = object.ChecksumSHA256
		file.UploadedAt = &now
		upload.Status = domain.MultipartCompleted
		result = *file
		return nil
	})
	return result, errors.Join(err, completionErr)
}

func (s *Service) AbortMultipart(ctx context.Context, id, user string) error {
	return s.abortMultipart(ctx, id, user, false)
}
func (s *Service) abortMultipart(ctx context.Context, id, user string, expiredOnly bool) error {
	return s.withMultipart(ctx, id, user, func(file *domain.File, upload *domain.Multipart) error {
		if upload.Status == domain.MultipartAborted {
			return nil
		}
		if upload.Status != domain.MultipartUploading || file.Status != domain.FileStatusPending {
			return domain.ErrInvalidStatus
		}
		if expiredOnly && time.Now().Before(upload.ExpiresAt) {
			return nil
		}
		if err := s.storage.AbortMultipart(ctx, file.BucketName, file.ObjectKey, upload.UploadID); err != nil {
			return err
		}
		upload.Status = domain.MultipartAborted
		file.Status = domain.FileStatusFailed
		return nil
	})
}

// RecoverMultipart aborts expired sessions and reconciles interrupted completions.
func (s *Service) RecoverMultipart(ctx context.Context) error {
	entries, err := s.files.RecoverableUploads(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := s.finishMultipart(callCtx, entry.FileID, entry.UserID)
		if errors.Is(err, domain.ErrInvalidStatus) {
			err = s.recoverPendingFile(callCtx, entry.FileID, entry.UserID, time.Now().UTC())
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("recover multipart: %w", err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}
