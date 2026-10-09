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
	file                                      domain.File
	request                                   domain.SignedRequest
	err                                       error
	userID, fileID, originalName, contentType string
	size                                      int64
	isPublic                                  bool
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
