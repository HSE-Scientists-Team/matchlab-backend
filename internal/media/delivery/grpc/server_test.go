package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeMedia struct {
	partNumbers                               []int32
	parts                                     []domain.Part
	file                                      domain.File
	request                                   domain.SignedRequest
	err                                       error
	userID, fileID, originalName, contentType string
	size                                      int64
	isPublic                                  bool
}

func (f *fakeMedia) DeleteFile(ctx context.Context, id, user string) error {
	f.fileID, f.userID = id, user
	return f.err
}

func TestDeleteFileRPC(t *testing.T) {
	f := &fakeMedia{}
	client := newTestClient(t, f)
	_, err := client.DeleteFile(context.Background(), &mediav1.DeleteFileRequest{UserId: "owner", FileId: "file"})
	if err != nil || f.userID != "owner" || f.fileID != "file" {
		t.Fatalf("delete: %v", err)
	}
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{domain.ErrNotFound, codes.NotFound}, {domain.ErrInvalidStatus, codes.FailedPrecondition}, {domain.ErrStorageUnavailable, codes.Unavailable},
	} {
		f.err = tc.err
		if _, err := client.DeleteFile(context.Background(), &mediav1.DeleteFileRequest{}); status.Code(err) != tc.code {
			t.Fatal(err)
		}
	}
}

func (f *fakeMedia) CreateMultipart(ctx context.Context, user, name, typ string, size int64, public bool) (domain.MultipartState, error) {
	f.userID, f.originalName, f.contentType, f.size, f.isPublic = user, name, typ, size, public
	return domain.MultipartState{File: f.file, Upload: domain.Multipart{ExpectedSize: size, PartSize: domain.MinPartSize, Status: domain.MultipartUploading, ExpiresAt: time.Unix(600, 0)}}, f.err
}
func (f *fakeMedia) GetMultipart(ctx context.Context, id, user string) (domain.MultipartState, error) {
	f.fileID, f.userID = id, user
	return domain.MultipartState{File: f.file, Upload: domain.Multipart{ExpectedSize: 5, PartSize: domain.MinPartSize, Status: domain.MultipartUploading}, Parts: []domain.Part{{Number: 1, ETag: "etag", SizeBytes: 5}}, UploadedBytes: 5}, f.err
}
func (f *fakeMedia) PartURLs(ctx context.Context, id, user string, numbers []int32) ([]domain.PartURL, error) {
	f.fileID, f.userID, f.partNumbers = id, user, numbers
	return []domain.PartURL{{Number: 1, SizeBytes: 5, Request: f.request}}, f.err
}
func (f *fakeMedia) CompleteMultipart(ctx context.Context, id, user string, parts []domain.Part) (domain.File, error) {
	f.fileID, f.userID, f.parts = id, user, parts
	return f.file, f.err
}
func (f *fakeMedia) AbortMultipart(ctx context.Context, id, user string) error {
	f.fileID, f.userID = id, user
	return f.err
}

func TestMultipartRPCMessages(t *testing.T) {
	f := &fakeMedia{file: domain.File{ID: "file", Status: domain.FileStatusPending}, request: domain.SignedRequest{URL: "http://storage/part", Method: "PUT", Headers: map[string]string{"Content-Length": "5"}, ExpiresAt: time.Unix(600, 0)}}
	client := newTestClient(t, f)
	ctx := context.Background()
	state, err := client.CreateMultipart(ctx, &mediav1.CreateUploadRequest{UserId: "owner", OriginalName: "report.pdf", ContentType: "application/pdf", SizeBytes: 5, IsPublic: true})
	if err != nil || state.GetPartCount() != 1 || state.GetExpiresAtUnix() != 600 || f.userID != "owner" || !f.isPublic {
		t.Fatalf("create: %v %v", state, err)
	}
	state, err = client.GetMultipart(ctx, &mediav1.GetFileRequest{UserId: "owner", FileId: "file"})
	if err != nil || state.GetUploadedBytes() != 5 || len(state.GetParts()) != 1 || state.GetParts()[0].GetEtag() != "etag" {
		t.Fatalf("get: %v %v", state, err)
	}
	urls, err := client.CreatePartURLs(ctx, &mediav1.CreatePartURLsRequest{UserId: "owner", FileId: "file", PartNumbers: []int32{1}})
	if err != nil || len(urls.GetParts()) != 1 || urls.GetParts()[0].GetUploadUrl() != f.request.URL || urls.GetParts()[0].GetHeaders()["Content-Length"] != "5" || f.partNumbers[0] != 1 {
		t.Fatalf("urls: %v %v", urls, err)
	}
	_, err = client.CompleteMultipart(ctx, &mediav1.CompleteMultipartRequest{UserId: "owner", FileId: "file", Parts: []*mediav1.MultipartPart{{PartNumber: 1, Etag: "etag"}}})
	if err != nil || len(f.parts) != 1 || f.parts[0].ETag != "etag" {
		t.Fatalf("complete: %v", err)
	}
	_, err = client.AbortMultipart(ctx, &mediav1.CompleteUploadRequest{UserId: "owner", FileId: "file"})
	if err != nil || f.fileID != "file" || f.userID != "owner" {
		t.Fatal(err)
	}
	f.err = domain.ErrInvalidStatus
	if _, err = client.CompleteMultipart(ctx, &mediav1.CompleteMultipartRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}

func (f *fakeMedia) CreateUpload(ctx context.Context, user, name, typ string, size int64, isPublic bool) (domain.Upload, error) {
	f.isPublic = isPublic
	f.userID, f.originalName, f.contentType, f.size = user, name, typ, size
	return domain.Upload{File: f.file, Request: f.request}, f.err
}
func (f *fakeMedia) CompleteUpload(ctx context.Context, file, user string) (domain.File, error) {
	f.fileID, f.userID = file, user
	return f.file, f.err
}
func (f *fakeMedia) GetFile(ctx context.Context, file, user string) (domain.File, error) {
	f.fileID, f.userID = file, user
	return f.file, f.err
}
func (f *fakeMedia) CreateDownloadURL(ctx context.Context, file, user string) (domain.SignedRequest, error) {
	f.fileID, f.userID = file, user
	return f.request, f.err
}

func newTestClient(t *testing.T, media Media) mediav1.MediaServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := googlegrpc.NewServer()
	mediav1.RegisterMediaServiceServer(server, NewServer(media))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := googlegrpc.NewClient("passthrough:///media", googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
		googlegrpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mediav1.NewMediaServiceClient(conn)
}

func TestRPCMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	media := &fakeMedia{file: domain.File{ID: "file", OwnerUserID: "owner", OriginalName: "report.pdf", ContentType: "application/pdf", Status: domain.FileStatusPending, CreatedAt: time.Unix(100, 0)},
		request: domain.SignedRequest{URL: "http://storage/upload", Method: "PUT", Headers: map[string]string{"If-None-Match": "*"}, ExpiresAt: time.Unix(600, 0)}}
	client := newTestClient(t, media)
	media.file.IsPublic = true
	created, err := client.CreateUpload(ctx, &mediav1.CreateUploadRequest{UserId: "owner", OriginalName: "report.pdf", ContentType: "application/pdf", SizeBytes: 42, IsPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	if media.userID != "owner" || media.originalName != "report.pdf" || media.contentType != "application/pdf" || media.size != 42 || created.GetFile().GetStatus() != mediav1.FileStatus_FILE_STATUS_PENDING || created.GetFile().SizeBytes != nil || created.GetFile().UploadedAtUnix != nil || created.GetUploadUrl() != media.request.URL || created.GetMethod() != "PUT" || created.GetHeaders()["If-None-Match"] != "*" || created.GetExpiresAtUnix() != 600 {
		t.Fatal("incorrect upload request/response")
	}
	if !media.isPublic || !created.GetFile().GetIsPublic() {
		t.Fatal("public flag lost in gRPC request/response")
	}
	size := int64(42)
	uploaded := time.Unix(200, 0)
	media.file.Status, media.file.SizeBytes, media.file.UploadedAt = domain.FileStatusReady, &size, &uploaded
	complete, err := client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{FileId: "file", UserId: "owner"})
	if err != nil || media.fileID != "file" || media.userID != "owner" || complete.GetFile().GetStatus() != mediav1.FileStatus_FILE_STATUS_READY || complete.GetFile().GetSizeBytes() != 42 || complete.GetFile().GetUploadedAtUnix() != 200 {
		t.Fatalf("complete: %v", err)
	}
	metadata, err := client.GetFile(ctx, &mediav1.GetFileRequest{FileId: "file"})
	if err != nil || media.userID != "" || !metadata.GetFile().GetIsPublic() || metadata.GetFile().GetOriginalName() != "report.pdf" || metadata.GetFile().GetCreatedAtUnix() != 100 {
		t.Fatalf("metadata: %v", err)
	}
	media.request.URL = "http://storage/download"
	download, err := client.CreateDownloadURL(ctx, &mediav1.CreateDownloadURLRequest{FileId: "file"})
	if err != nil || media.fileID != "file" || media.userID != "" || download.GetDownloadUrl() != media.request.URL || download.GetExpiresAtUnix() != 600 {
		t.Fatalf("download: %v", err)
	}
}

func TestGRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{"invalid argument", domain.ErrInvalidArgument, codes.InvalidArgument},
		{"not found", domain.ErrNotFound, codes.NotFound},
		{"duplicate", domain.ErrAlreadyExists, codes.AlreadyExists},
		{"invalid status", domain.ErrInvalidStatus, codes.FailedPrecondition},
		{"not uploaded", domain.ErrObjectNotFound, codes.FailedPrecondition},
		{"mismatch", domain.ErrObjectMismatch, codes.FailedPrecondition},
		{"storage unavailable", domain.ErrStorageUnavailable, codes.Unavailable},
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"internal", errors.New("private-database-details"), codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, &fakeMedia{err: fmt.Errorf("private-details: %w", tc.err)})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.GetFile(ctx, &mediav1.GetFileRequest{})
			if status.Code(err) != tc.code || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("unsafe or incorrect status: %v", err)
			}
		})
	}
}
