package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/repository"
)

const testUser = "00000000-0000-4000-8000-000000000001"
const testFile = "00000000-0000-4000-8000-000000000002"
const otherUser = "00000000-0000-4000-8000-000000000003"

type fakeFiles struct {
	stalePages      [][]repository.UploadOwner
	staleQueries    int
	staleVisits     []string
	finishDeleteErr error
	upload          domain.Multipart
	file            domain.File
	createErr       error
	created         bool
	marked          bool
}

func (f *fakeFiles) CreatePending(ctx context.Context, in domain.NewFile) (domain.File, error) {
	if f.createErr != nil {
		return domain.File{}, f.createErr
	}
	f.created = true
	f.file = domain.File{ID: in.ID, OwnerUserID: in.OwnerUserID, IsPublic: in.IsPublic, BucketName: in.BucketName, ObjectKey: in.ObjectKey, OriginalName: in.OriginalName, ContentType: in.ContentType, Status: domain.FileStatusPending}
	if in.ExpectedSizeBytes > 0 {
		f.file.ExpectedSizeBytes = &in.ExpectedSizeBytes
	}
	f.file.UpdatedAt = time.Now().UTC()
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
	deleteErr               error
	abortErr                error
	listErr                 error
	deleteCalls             int
	deleteBucket, deleteKey string
	parts                   []domain.Part
	headErr                 error
	finishErr               error
	finishCalls             int
	abortCalls              int
	partCalls               int
	partTTL                 time.Duration
	partSize                int64
	upload                  domain.UploadInput
	download                domain.DownloadInput
	object                  domain.ObjectInfo
	headCalls               int
	err                     error
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
	if f.headErr != nil {
		return domain.ObjectInfo{}, f.headErr
	}
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

func (f *fakeFiles) BeginDeletion(ctx context.Context, id, owner string) (domain.File, error) {
	file, err := f.FindOwned(ctx, id, owner)
	if err != nil {
		return domain.File{}, err
	}
	switch file.Status {
	case domain.FileStatusReady:
		f.file.Status = domain.FileStatusDeleting
	case domain.FileStatusDeleting, domain.FileStatusDeleted:
	default:
		return domain.File{}, domain.ErrInvalidStatus
	}
	return f.file, nil
}

func (f *fakeFiles) FinishDeletion(ctx context.Context, id, owner string) error {
	if f.finishDeleteErr != nil {
		return f.finishDeleteErr
	}
	f.file.Status = domain.FileStatusDeleted
	now := time.Now()
	f.file.DeletedAt = &now
	return nil
}

func (f *fakeFiles) DeletingFiles(context.Context) ([]repository.UploadOwner, error) {
	if f.file.Status != domain.FileStatusDeleting {
		return nil, nil
	}
	return []repository.UploadOwner{{FileID: f.file.ID, UserID: f.file.OwnerUserID}}, nil
}

func (f *fakeStorage) DeleteObject(ctx context.Context, bucket, key string) error {
	f.deleteCalls++
	f.deleteBucket, f.deleteKey = bucket, key
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.err
}

func (f *fakeFiles) StalePendingFiles(ctx context.Context, cutoff time.Time, after string) ([]repository.UploadOwner, error) {
	f.staleQueries++
	if f.stalePages != nil {
		if f.staleQueries <= len(f.stalePages) {
			return f.stalePages[f.staleQueries-1], nil
		}
		return nil, nil
	}
	if f.file.Status == domain.FileStatusPending && !f.file.UpdatedAt.After(cutoff) && f.file.ID > after {
		return []repository.UploadOwner{{FileID: f.file.ID, UserID: f.file.OwnerUserID}}, nil
	}
	return nil, nil
}

func (f *fakeFiles) WithStalePending(ctx context.Context, id, owner string, cutoff time.Time, fn func(*domain.File, *domain.Multipart) error) error {
	f.staleVisits = append(f.staleVisits, id)
	if f.file.ID != id || f.file.OwnerUserID != owner || f.file.Status != domain.FileStatusPending || f.file.UpdatedAt.After(cutoff) {
		return nil
	}
	file, upload := f.file, f.upload
	var multipart *domain.Multipart
	if upload.UploadID != "" {
		multipart = &upload
	}
	if err := fn(&file, multipart); err != nil {
		return err
	}
	file.UpdatedAt = time.Now().UTC()
	f.file, f.upload = file, upload
	return nil
}

func TestRecoverPendingSingle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		object  domain.ObjectInfo
		headErr error
		want    domain.FileStatus
		deletes int
	}{
		{"complete", domain.ObjectInfo{SizeBytes: 5, ContentType: "application/pdf"}, nil, domain.FileStatusReady, 0},
		{"absent", domain.ObjectInfo{}, domain.ErrObjectNotFound, domain.FileStatusFailed, 1},
		{"wrong type", domain.ObjectInfo{SizeBytes: 5, ContentType: "image/png"}, nil, domain.FileStatusFailed, 1},
		{"wrong size", domain.ObjectInfo{SizeBytes: 3, ContentType: "application/pdf"}, nil, domain.FileStatusFailed, 1},
		{"empty", domain.ObjectInfo{SizeBytes: 0, ContentType: "application/pdf"}, nil, domain.FileStatusFailed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, files, storage := setup(t)
			expected := int64(5)
			files.file.ExpectedSizeBytes, files.file.UpdatedAt = &expected, time.Now().Add(-25*time.Hour)
			storage.object, storage.headErr = tc.object, tc.headErr
			if err := s.RecoverPending(context.Background()); err != nil {
				t.Fatal(err)
			}
			if files.file.Status != tc.want || storage.deleteCalls != tc.deletes {
				t.Fatalf("state=%s deletes=%d", files.file.Status, storage.deleteCalls)
			}
			if tc.want == domain.FileStatusReady && (files.file.UploadedAt == nil || files.file.SizeBytes == nil || *files.file.SizeBytes != 5) {
				t.Fatal("ready metadata missing")
			}
		})
	}
}

func TestRecoverPendingEligibilityAndRecheck(t *testing.T) {
	s, files, storage := setup(t)
	files.file.UpdatedAt = time.Now().Add(-23 * time.Hour)
	if err := s.RecoverPending(context.Background()); err != nil || storage.headCalls != 0 {
		t.Fatal("fresh file processed", err)
	}
	// The row changed after selection: the locked recheck must skip it.
	files.stalePages = [][]repository.UploadOwner{{{FileID: testFile, UserID: testUser}}}
	files.staleQueries = 0
	if err := s.RecoverPending(context.Background()); err != nil || storage.headCalls != 0 {
		t.Fatal("recheck ignored update", err)
	}
	files.file.Status, files.file.UpdatedAt = domain.FileStatusReady, time.Now().Add(-25*time.Hour)
	files.staleQueries = 0
	if err := s.RecoverPending(context.Background()); err != nil || storage.headCalls != 0 {
		t.Fatal("ready file processed", err)
	}
}

func TestRecoverPendingRetriesTemporaryFailures(t *testing.T) {
	for _, failure := range []string{"head", "delete"} {
		t.Run(failure, func(t *testing.T) {
			s, files, storage := setup(t)
			files.file.UpdatedAt = time.Now().Add(-25 * time.Hour)
			before := files.file.UpdatedAt
			storage.headErr = domain.ErrStorageUnavailable
			if failure == "delete" {
				storage.headErr, storage.deleteErr = domain.ErrObjectNotFound, domain.ErrStorageUnavailable
			}
			if err := s.RecoverPending(context.Background()); !errors.Is(err, domain.ErrStorageUnavailable) {
				t.Fatal(err)
			}
			if files.file.Status != domain.FileStatusPending || !files.file.UpdatedAt.Equal(before) {
				t.Fatal("temporary failure committed state")
			}
			storage.headErr, storage.deleteErr = domain.ErrObjectNotFound, nil
			if err := s.RecoverPending(context.Background()); err != nil || files.file.Status != domain.FileStatusFailed {
				t.Fatal("retry failed", err)
			}
		})
	}
}

func TestRecoverPendingVisitsEveryBatchDespiteFailure(t *testing.T) {
	s, files, storage := setup(t)
	files.file.UpdatedAt = time.Now().Add(-25 * time.Hour)
	files.stalePages = [][]repository.UploadOwner{
		{{FileID: testFile, UserID: testUser}}, {{FileID: "later-file", UserID: testUser}},
	}
	storage.headErr = domain.ErrStorageUnavailable
	if err := s.RecoverPending(context.Background()); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatal(err)
	}
	if files.staleQueries != 3 || len(files.staleVisits) != 2 || files.staleVisits[1] != "later-file" {
		t.Fatal("failed first batch starved later files")
	}
}

func TestDeleteFileOwnerAndLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, public := range []bool{false, true} {
		s, files, storage := setup(t)
		files.file.Status, files.file.IsPublic = domain.FileStatusReady, public
		for _, user := range []string{otherUser, ""} {
			want := domain.ErrNotFound
			if user == "" {
				want = domain.ErrInvalidArgument
			}
			if err := s.DeleteFile(ctx, testFile, user); !errors.Is(err, want) || storage.deleteCalls != 0 || files.file.Status != domain.FileStatusReady {
				t.Fatalf("unauthorized delete: %v", err)
			}
		}
		if err := s.DeleteFile(ctx, testFile, testUser); err != nil {
			t.Fatal(err)
		}
		if files.file.Status != domain.FileStatusDeleted || files.file.DeletedAt == nil || storage.deleteBucket != "matchlab-media" || storage.deleteKey != "users/key" {
			t.Fatal("deletion not persisted")
		}
		if _, err := s.GetFile(ctx, testFile, testUser); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := s.CreateDownloadURL(ctx, testFile, testUser); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
		if err := s.DeleteFile(ctx, testFile, testUser); err != nil || storage.deleteCalls != 1 {
			t.Fatalf("repeat: %v", err)
		}
		if err := s.DeleteFile(ctx, testFile, otherUser); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
	}
}

func TestDeleteFileRecovery(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"s3", "database"} {
		t.Run(failure, func(t *testing.T) {
			s, files, storage := setup(t)
			files.file.Status = domain.FileStatusReady
			if failure == "s3" {
				storage.err = domain.ErrStorageUnavailable
			} else {
				files.finishDeleteErr = errors.New("commit failed")
			}
			if err := s.DeleteFile(ctx, testFile, testUser); err == nil || files.file.Status != domain.FileStatusDeleting {
				t.Fatalf("lost intent: %v", err)
			}
			if _, err := s.GetFile(ctx, testFile, testUser); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("deleting file visible: %v", err)
			}
			storage.err, files.finishDeleteErr = nil, nil
			if err := s.RecoverDeletions(ctx); err != nil || files.file.Status != domain.FileStatusDeleted || storage.deleteCalls != 2 {
				t.Fatalf("recovery: %v", err)
			}
		})
	}
}

func TestDeleteFileRejectsUnfinishedUpload(t *testing.T) {
	for _, state := range []domain.FileStatus{domain.FileStatusPending, domain.FileStatusFailed} {
		s, files, storage := setup(t)
		files.file.Status = state
		if err := s.DeleteFile(context.Background(), testFile, testUser); !errors.Is(err, domain.ErrInvalidStatus) || storage.deleteCalls != 0 {
			t.Fatalf("state %s: %v", state, err)
		}
	}
}
