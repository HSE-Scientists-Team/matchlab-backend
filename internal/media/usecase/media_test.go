package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

const testUser = "00000000-0000-4000-8000-000000000001"
const testFile = "00000000-0000-4000-8000-000000000002"
const otherUser = "00000000-0000-4000-8000-000000000003"

type fakeFiles struct {
	file      domain.File
	createErr error
	created   bool
	marked    bool
}

func (f *fakeFiles) CreatePending(ctx context.Context, in domain.NewFile) (domain.File, error) {
	if f.createErr != nil {
		return domain.File{}, f.createErr
	}
	f.created = true
	f.file = domain.File{ID: in.ID, OwnerUserID: in.OwnerUserID, IsPublic: in.IsPublic, BucketName: in.BucketName, ObjectKey: in.ObjectKey, OriginalName: in.OriginalName, ContentType: in.ContentType, Status: domain.FileStatusPending}
	return f.file, nil
}

func (f *fakeFiles) FindOwned(ctx context.Context, id, owner string) (domain.File, error) {
	if id != f.file.ID || owner != f.file.OwnerUserID {
		return domain.File{}, domain.ErrNotFound
	}
	return f.file, nil
}

func (f *fakeFiles) FindReadable(ctx context.Context, id, user string) (domain.File, error) {
	if id != f.file.ID || (user != f.file.OwnerUserID && !f.file.IsPublic) {
		return domain.File{}, domain.ErrNotFound
	}
	return f.file, nil
}

func (f *fakeFiles) MarkReady(ctx context.Context, id, owner string, object domain.ObjectInfo) (domain.File, error) {
	f.marked = true
	f.file.Status = domain.FileStatusReady
	f.file.SizeBytes = &object.SizeBytes
	return f.file, nil
}

type fakeStorage struct {
	upload    domain.UploadInput
	download  domain.DownloadInput
	object    domain.ObjectInfo
	headCalls int
	err       error
}

func (f *fakeStorage) PresignUpload(ctx context.Context, in domain.UploadInput) (domain.SignedRequest, error) {
	f.upload = in
	if f.err != nil {
		return domain.SignedRequest{}, f.err
	}
	return domain.SignedRequest{URL: "http://storage/upload", Method: "PUT", ExpiresAt: time.Now().Add(in.TTL)}, nil
}

func (f *fakeStorage) HeadObject(ctx context.Context, bucket, key string) (domain.ObjectInfo, error) {
	f.headCalls++
	return f.object, f.err
}

func (f *fakeStorage) PresignDownload(ctx context.Context, in domain.DownloadInput) (domain.SignedRequest, error) {
	f.download = in
	return domain.SignedRequest{URL: "http://storage/download", Method: "GET"}, f.err
}

func setup(t *testing.T) (*Service, *fakeFiles, *fakeStorage) {
	t.Helper()
	files := &fakeFiles{file: domain.File{ID: testFile, OwnerUserID: testUser, BucketName: "matchlab-media", ObjectKey: "users/key", OriginalName: "report.pdf", ContentType: "application/pdf", Status: domain.FileStatusPending}}
	storage := &fakeStorage{object: domain.ObjectInfo{SizeBytes: 5, ContentType: "application/pdf"}}
	service, err := NewService(files, storage, Options{Bucket: "matchlab-media", MaxSizeBytes: 10, AllowedContentTypes: []string{"application/pdf"}, UploadTTL: 10 * time.Minute, DownloadTTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return service, files, storage
}

func TestCreateUploadValidation(t *testing.T) {
	for _, tc := range []struct {
		name, user, filename, typ string
		size                      int64
	}{
		{"user", "invalid", "report.pdf", "application/pdf", 5},
		{"empty name", testUser, " ", "application/pdf", 5},
		{"path", testUser, "../report.pdf", "application/pdf", 5},
		{"windows path", testUser, `a\report.pdf`, "application/pdf", 5},
		{"control", testUser, "report\npdf", "application/pdf", 5},
		{"long name", testUser, strings.Repeat("я", 256), "application/pdf", 5},
		{"type", testUser, "report.pdf", "text/html", 5},
		{"parameters", testUser, "report.pdf", "application/pdf; charset=utf-8", 5},
		{"negative", testUser, "report.pdf", "application/pdf", -1},
		{"empty", testUser, "report.pdf", "application/pdf", 0},
		{"too big", testUser, "report.pdf", "application/pdf", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, files, storage := setup(t)
			_, err := service.CreateUpload(context.Background(), tc.user, tc.filename, tc.typ, tc.size, false)
			if !errors.Is(err, domain.ErrInvalidArgument) || files.created || storage.upload.Key != "" {
				t.Fatalf("invalid upload performed work: %v", err)
			}
		})
	}
}

func TestCreateUploadStorageAndDatabaseFailure(t *testing.T) {
	service, files, storage := setup(t)
	storage.err = domain.ErrStorageUnavailable
	if _, err := service.CreateUpload(context.Background(), testUser, "report.pdf", "application/pdf", 5, false); !errors.Is(err, domain.ErrStorageUnavailable) || files.created {
		t.Fatalf("storage failure: %v", err)
	}
	storage.err = nil
	files.createErr = errors.New("database unavailable")
	result, err := service.CreateUpload(context.Background(), testUser, "report.pdf", "application/pdf", 5, false)
	if !errors.Is(err, files.createErr) || result.Request.URL != "" {
		t.Fatalf("URL escaped failed save: %v", err)
	}
}

func TestUploadLifecycle(t *testing.T) {
	service, files, storage := setup(t)
	ctx := context.Background()
	created, err := service.CreateUpload(ctx, testUser, "report.pdf", "application/pdf", 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if created.File.Status != domain.FileStatusPending || created.File.SizeBytes != nil || !strings.HasPrefix(storage.upload.Key, "users/"+testUser+"/") || strings.Contains(storage.upload.Key, "report.pdf") || storage.upload.SizeBytes != 5 {
		t.Fatal("incorrect upload metadata")
	}
	id := created.File.ID
	if _, err := service.CreateDownloadURL(ctx, id, testUser); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatalf("pending download: %v", err)
	}
	if _, err := service.CompleteUpload(ctx, id, otherUser); !errors.Is(err, domain.ErrNotFound) || storage.headCalls != 0 {
		t.Fatalf("stranger confirmation: %v", err)
	}
	ready, err := service.CompleteUpload(ctx, id, testUser)
	if err != nil || ready.Status != domain.FileStatusReady || !files.marked {
		t.Fatalf("complete: %v", err)
	}
	storage.err = domain.ErrStorageUnavailable
	if _, err := service.CompleteUpload(ctx, id, testUser); err != nil || storage.headCalls != 1 {
		t.Fatalf("repeat contacted S3: %v", err)
	}
	storage.err = nil
	if _, err := service.CreateDownloadURL(ctx, id, testUser); err != nil || storage.download.Key != files.file.ObjectKey || storage.download.TTL != 5*time.Minute {
		t.Fatalf("download: %v", err)
	}
	if _, err := service.CreateDownloadURL(ctx, id, otherUser); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger download: %v", err)
	}
}

func TestCompleteRejectsBadObjectAndKeepsPending(t *testing.T) {
	for _, object := range []domain.ObjectInfo{{SizeBytes: -1, ContentType: "application/pdf"}, {SizeBytes: 11, ContentType: "application/pdf"}, {SizeBytes: 5, ContentType: "image/png"}} {
		service, files, storage := setup(t)
		storage.object = object
		if _, err := service.CompleteUpload(context.Background(), testFile, testUser); !errors.Is(err, domain.ErrObjectMismatch) || files.marked {
			t.Fatalf("bad object accepted: %v", err)
		}
	}
	for _, failure := range []error{domain.ErrObjectNotFound, domain.ErrStorageUnavailable, context.DeadlineExceeded} {
		service, files, storage := setup(t)
		storage.err = failure
		if _, err := service.CompleteUpload(context.Background(), testFile, testUser); !errors.Is(err, failure) || files.marked {
			t.Fatalf("failure changed pending: %v", err)
		}
	}
}

func TestStatesAndGetFileWithoutS3(t *testing.T) {
	for _, state := range []domain.FileStatus{domain.FileStatusDeleting, domain.FileStatusFailed, domain.FileStatusDeleted} {
		service, files, storage := setup(t)
		files.file.Status = state
		want := domain.ErrInvalidStatus
		if state == domain.FileStatusDeleted {
			want = domain.ErrNotFound
		}
		if _, err := service.CompleteUpload(context.Background(), testFile, testUser); !errors.Is(err, want) || storage.headCalls != 0 {
			t.Fatalf("state %s: %v", state, err)
		}
	}
	service, _, storage := setup(t)
	storage.err = domain.ErrStorageUnavailable
	if _, err := service.GetFile(context.Background(), testFile, testUser); err != nil || storage.headCalls != 0 {
		t.Fatalf("metadata depends on S3: %v", err)
	}
}

func TestFileReadAccess(t *testing.T) {
	for _, public := range []bool{false, true} {
		for _, reader := range []struct{ name, id string }{{"owner", testUser}, {"stranger", otherUser}, {"guest", ""}} {
			name := "private/"
			if public {
				name = "public/"
			}
			t.Run(name+reader.name, func(t *testing.T) {
				service, files, storage := setup(t)
				files.file.IsPublic, files.file.Status = public, domain.FileStatusReady
				allowed := public || reader.id == testUser
				file, err := service.GetFile(context.Background(), testFile, reader.id)
				if allowed {
					if err != nil || file.IsPublic != public {
						t.Fatalf("metadata: %+v, %v", file, err)
					}
				} else if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("private metadata leaked: %v", err)
				}
				url, err := service.CreateDownloadURL(context.Background(), testFile, reader.id)
				if allowed {
					if err != nil || url.URL == "" {
						t.Fatalf("download: %v", err)
					}
				} else if !errors.Is(err, domain.ErrNotFound) || storage.download.Key != "" {
					t.Fatalf("private download leaked: %v", err)
				}
			})
		}
	}
}

func TestPublicUploadAccess(t *testing.T) {
	service, files, storage := setup(t)
	created, err := service.CreateUpload(context.Background(), testUser, "report.pdf", "application/pdf", 5, true)
	if err != nil || !created.File.IsPublic || !files.file.IsPublic {
		t.Fatalf("public upload: %+v, %v", created, err)
	}
	if _, err := service.CreateDownloadURL(context.Background(), created.File.ID, ""); !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatalf("pending public download: %v", err)
	}
	for _, state := range []domain.FileStatus{domain.FileStatusPending, domain.FileStatusReady} {
		files.file.Status = state
		for _, user := range []string{otherUser, ""} {
			want := domain.ErrNotFound
			if user == "" {
				want = domain.ErrInvalidArgument
			}
			if _, err := service.CompleteUpload(context.Background(), created.File.ID, user); !errors.Is(err, want) || storage.headCalls != 0 || files.marked {
				t.Fatalf("non-owner confirmation in %s: %v", state, err)
			}
		}
	}
	files.file.Status = domain.FileStatusDeleted
	if _, err := service.GetFile(context.Background(), created.File.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted public file: %v", err)
	}
	if _, err := service.GetFile(context.Background(), created.File.ID, "invalid"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid reader ID: %v", err)
	}
}
