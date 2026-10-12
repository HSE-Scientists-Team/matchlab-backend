package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/repository"
)

func (f *fakeFiles) CreateMultipart(ctx context.Context, in domain.NewFile, upload domain.Multipart) (domain.File, error) {
	file, err := f.CreatePending(ctx, in)
	if err == nil {
		f.upload = upload
	}
	return file, err
}
func (f *fakeFiles) WithMultipart(ctx context.Context, id, user string, fn func(*domain.File, *domain.Multipart) error) error {
	if id != f.file.ID || user != f.file.OwnerUserID || f.upload.UploadID == "" {
		return domain.ErrNotFound
	}
	file, upload := f.file, f.upload
	if err := fn(&file, &upload); err != nil {
		return err
	}
	f.file, f.upload = file, upload
	return nil
}
func (f *fakeFiles) IsMultipart(context.Context, string) (bool, error) {
	return f.upload.UploadID != "", nil
}
func (f *fakeFiles) RecoverableUploads(context.Context) ([]repository.UploadOwner, error) {
	if f.upload.Status == domain.MultipartCompleting || f.upload.Status == domain.MultipartUploading && !time.Now().Before(f.upload.ExpiresAt) {
		return []repository.UploadOwner{{FileID: f.file.ID, UserID: f.file.OwnerUserID}}, nil
	}
	return nil, nil
}
func (f *fakeStorage) StartMultipart(context.Context, string, string, string) (string, error) {
	return "upload-id", f.err
}
func (f *fakeStorage) PresignPart(ctx context.Context, bucket, key, id string, number int32, size int64, ttl time.Duration) (domain.SignedRequest, error) {
	f.partCalls++
	f.partTTL = ttl
	f.partSize = size
	return domain.SignedRequest{URL: "http://storage/part", Method: "PUT", ExpiresAt: time.Now().Add(ttl)}, f.err
}
func (f *fakeStorage) ListParts(context.Context, string, string, string) ([]domain.Part, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.parts, f.err
}
func (f *fakeStorage) FinishMultipart(context.Context, string, string, string, []domain.Part) error {
	f.finishCalls++
	if f.finishErr != nil {
		return f.finishErr
	}
	f.headErr = nil
	return nil
}
func (f *fakeStorage) AbortMultipart(context.Context, string, string, string) error {
	f.abortCalls++
	if f.abortErr != nil {
		return f.abortErr
	}
	return f.err
}

func multipartSetup(t *testing.T) (*Service, *fakeFiles, *fakeStorage) {
	s, files, storage := setup(t)
	s.options.MaxSizeBytes = 20 * 1024 * 1024
	size := s.options.MultipartPartSize + 3
	state, err := s.CreateMultipart(context.Background(), testUser, "report.pdf", "application/pdf", size, false)
	if err != nil {
		t.Fatal(err)
	}
	storage.parts = []domain.Part{{Number: 1, ETag: "\"first\"", SizeBytes: s.options.MultipartPartSize}, {Number: 2, ETag: "\"last\"", SizeBytes: 3}}
	storage.object = domain.ObjectInfo{SizeBytes: size, ContentType: "application/pdf"}
	storage.headErr = domain.ErrObjectNotFound
	if state.Upload.PartCount() != 2 || files.file.Status != domain.FileStatusPending {
		t.Fatal("incorrect session")
	}
	return s, files, storage
}

func TestMultipartPartURLsAndOwner(t *testing.T) {
	s, files, storage := multipartSetup(t)
	ctx := context.Background()
	for _, numbers := range [][]int32{nil, {0}, {3}, {1, 1}} {
		if _, err := s.PartURLs(ctx, files.file.ID, testUser, numbers); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("numbers=%v: %v", numbers, err)
		}
	}
	if storage.partCalls != 0 {
		t.Fatal("invalid input signed parts")
	}
	if _, err := s.PartURLs(ctx, files.file.ID, otherUser, []int32{1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	files.upload.ExpiresAt = time.Now().Add(30 * time.Second)
	urls, err := s.PartURLs(ctx, files.file.ID, testUser, []int32{2})
	if err != nil || len(urls) != 1 || storage.partSize != 3 || storage.partTTL > 30*time.Second {
		t.Fatalf("urls=%+v err=%v", urls, err)
	}
	state, err := s.GetMultipart(ctx, files.file.ID, testUser)
	if err != nil || state.UploadedBytes != files.upload.ExpectedSize || len(state.Parts) != 2 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := s.GetMultipart(ctx, files.file.ID, otherUser); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(ctx, files.file.ID, testUser); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatalf("single PUT bypass: %v", err)
	}
	files.upload.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := s.PartURLs(ctx, files.file.ID, testUser, []int32{1}); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatal(err)
	}
}

func TestRecoverPendingMultipart(t *testing.T) {
	for _, mode := range []string{"assembled", "full", "partial", "ambiguous", "completing", "bad object", "invalid part", "finish unavailable", "list unavailable", "abort unavailable", "delete unavailable"} {
		t.Run(mode, func(t *testing.T) {
			s, files, storage := multipartSetup(t)
			files.file.UpdatedAt, files.upload.ExpiresAt = time.Now().Add(-25*time.Hour), time.Now().Add(-time.Hour)
			storage.headErr = domain.ErrObjectNotFound
			want, wantErr := domain.FileStatusReady, error(nil)
			switch mode {
			case "assembled":
				storage.headErr = nil
			case "partial":
				storage.parts = storage.parts[:1]
				want = domain.FileStatusFailed
			case "ambiguous":
				storage.parts = append(storage.parts, domain.Part{Number: 1, ETag: "other", SizeBytes: storage.parts[0].SizeBytes})
				want = domain.FileStatusFailed
			case "completing":
				files.upload.Status, files.upload.Manifest = domain.MultipartCompleting, append([]domain.Part(nil), storage.parts...)
				storage.parts = nil
			case "bad object":
				storage.headErr = nil
				storage.object.SizeBytes--
				want = domain.FileStatusFailed
			case "invalid part":
				storage.finishErr = domain.ErrObjectMismatch
				want = domain.FileStatusFailed
			case "finish unavailable":
				storage.finishErr = domain.ErrStorageUnavailable
				want, wantErr = domain.FileStatusPending, domain.ErrStorageUnavailable
			case "list unavailable":
				storage.listErr = domain.ErrStorageUnavailable
				want, wantErr = domain.FileStatusPending, domain.ErrStorageUnavailable
			case "abort unavailable":
				storage.parts = nil
				storage.abortErr = domain.ErrStorageUnavailable
				want, wantErr = domain.FileStatusPending, domain.ErrStorageUnavailable
			case "delete unavailable":
				storage.parts = nil
				storage.deleteErr = domain.ErrStorageUnavailable
				want, wantErr = domain.FileStatusPending, domain.ErrStorageUnavailable
			}
			before := files.file.UpdatedAt
			err := s.RecoverPending(context.Background())
			if !errors.Is(err, wantErr) || files.file.Status != want {
				t.Fatalf("state=%s err=%v", files.file.Status, err)
			}
			if want == domain.FileStatusReady && (files.upload.Status != domain.MultipartCompleted || storage.abortCalls != 0 || storage.deleteCalls != 0) {
				t.Fatal("complete removed valid data")
			}
			if want == domain.FileStatusFailed && (files.upload.Status != domain.MultipartAborted || storage.abortCalls != 1 || storage.deleteCalls != 1) {
				t.Fatal("cleanup did not remove both parts and object")
			}
			if want == domain.FileStatusPending && (files.upload.Status != domain.MultipartUploading || !files.file.UpdatedAt.Equal(before)) {
				t.Fatal("temporary failure changed database")
			}
		})
	}
}

func TestMultipartCompletionValidation(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "etag", "size", "extra"} {
		t.Run(name, func(t *testing.T) {
			s, files, storage := multipartSetup(t)
			manifest := append([]domain.Part(nil), storage.parts...)
			switch name {
			case "missing":
				manifest = manifest[:1]
			case "duplicate":
				manifest[1] = manifest[0]
			case "etag":
				manifest[0].ETag = "wrong"
			case "size":
				storage.parts[0].SizeBytes--
			case "extra":
				storage.parts = append(storage.parts, domain.Part{Number: 3})
			}
			if _, err := s.CompleteMultipart(context.Background(), files.file.ID, testUser, manifest); !errors.Is(err, domain.ErrObjectMismatch) {
				t.Fatal(err)
			}
			if files.upload.Status != domain.MultipartUploading || storage.finishCalls != 0 {
				t.Fatal("invalid upload completed")
			}
		})
	}
}

func TestMultipartCompletionRecoversAfterS3Success(t *testing.T) {
	s, files, storage := multipartSetup(t)
	ctx := context.Background()
	manifest := append([]domain.Part(nil), storage.parts...)
	storage.finishErr = domain.ErrStorageUnavailable
	if _, err := s.CompleteMultipart(ctx, files.file.ID, testUser, manifest); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatal(err)
	}
	if files.upload.Status != domain.MultipartCompleting || len(files.upload.Manifest) != 2 {
		t.Fatal("manifest not persisted")
	}
	if err := s.AbortMultipart(ctx, files.file.ID, testUser); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatal("completion raced abort", err)
	}
	// S3 completed, but its response or the PostgreSQL commit was lost.
	storage.headErr = nil
	storage.parts = nil
	if err := s.RecoverMultipart(ctx); err != nil {
		t.Fatal(err)
	}
	if files.file.Status != domain.FileStatusReady || files.upload.Status != domain.MultipartCompleted || storage.finishCalls != 1 {
		t.Fatal("completion not recovered")
	}
	file, err := s.CompleteMultipart(ctx, files.file.ID, testUser, manifest)
	if err != nil || file.Status != domain.FileStatusReady || storage.finishCalls != 1 {
		t.Fatal("repeat completion", err)
	}
}

func TestMultipartCompleteAndAbort(t *testing.T) {
	s, files, storage := multipartSetup(t)
	ctx := context.Background()
	file, err := s.CompleteMultipart(ctx, files.file.ID, testUser, storage.parts)
	if err != nil || file.Status != domain.FileStatusReady || storage.finishCalls != 1 {
		t.Fatalf("file=%+v err=%v", file, err)
	}
	s, files, storage = multipartSetup(t)
	if err := s.AbortMultipart(ctx, files.file.ID, otherUser); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	storage.err = domain.ErrStorageUnavailable
	if err := s.AbortMultipart(ctx, files.file.ID, testUser); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatal(err)
	}
	if files.upload.Status != domain.MultipartUploading {
		t.Fatal("failed abort persisted")
	}
	storage.err = nil
	if err := s.AbortMultipart(ctx, files.file.ID, testUser); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipart(ctx, files.file.ID, testUser); err != nil {
		t.Fatal(err)
	}
	if files.upload.Status != domain.MultipartAborted || files.file.Status != domain.FileStatusFailed || storage.abortCalls != 2 {
		t.Fatal("abort not idempotent")
	}
}

func TestMultipartDuplicateVersionsDoNotInflateProgress(t *testing.T) {
	s, files, storage := multipartSetup(t)
	manifest := append([]domain.Part(nil), storage.parts...)
	storage.parts = append(storage.parts, domain.Part{Number: 2, ETag: "old", SizeBytes: 3})
	state, err := s.GetMultipart(context.Background(), files.file.ID, testUser)
	if err != nil || state.UploadedBytes != files.upload.ExpectedSize {
		t.Fatal("duplicate counted twice", err)
	}
	file, err := s.CompleteMultipart(context.Background(), files.file.ID, testUser, manifest)
	if err != nil || file.Status != domain.FileStatusReady {
		t.Fatal("acknowledged ETag rejected", err)
	}
}

func TestMultipartChangedPartCanBeCompletedAgain(t *testing.T) {
	s, files, storage := multipartSetup(t)
	storage.finishErr = domain.ErrObjectMismatch
	if _, err := s.CompleteMultipart(context.Background(), files.file.ID, testUser, storage.parts); !errors.Is(err, domain.ErrObjectMismatch) {
		t.Fatal(err)
	}
	if files.upload.Status != domain.MultipartUploading || len(files.upload.Manifest) != 0 {
		t.Fatal("session stuck completing")
	}
	storage.finishErr = nil
	storage.parts[0].ETag = "new"
	if _, err := s.CompleteMultipart(context.Background(), files.file.ID, testUser, storage.parts); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartExpiryAndCreationFailure(t *testing.T) {
	s, files, storage := multipartSetup(t)
	ctx := context.Background()
	if err := s.RecoverMultipart(ctx); err != nil || storage.abortCalls != 0 {
		t.Fatal("active upload aborted", err)
	}
	files.upload.ExpiresAt = time.Now().Add(-time.Second)
	storage.parts = nil
	if err := s.RecoverMultipart(ctx); err != nil || files.upload.Status != domain.MultipartAborted {
		t.Fatal("expired upload not cleaned", err)
	}
	s, files, storage = setup(t)
	files.createErr = errors.New("database unavailable")
	if _, err := s.CreateMultipart(ctx, testUser, "report.pdf", "application/pdf", 5, false); err == nil || storage.abortCalls != 1 {
		t.Fatal("orphan upload not aborted", err)
	}
}
