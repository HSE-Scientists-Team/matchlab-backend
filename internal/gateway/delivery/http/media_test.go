package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockMedia struct {
	create         *mediav1.CreateUploadRequest
	userID, fileID string
	calls          int
	err            error
	file           *mediav1.File
	deadline       bool
}

func (m *mockMedia) capture(ctx context.Context, user, id string) {
	m.userID, m.fileID = user, id
	m.calls++
	_, m.deadline = ctx.Deadline()
}
func (m *mockMedia) CreateUpload(ctx context.Context, req *mediav1.CreateUploadRequest, _ ...grpc.CallOption) (*mediav1.CreateUploadResponse, error) {
	m.create = req
	m.capture(ctx, req.GetUserId(), "")
	return &mediav1.CreateUploadResponse{File: m.file, UploadUrl: "http://s3/upload", Method: "PUT", Headers: map[string]string{"If-None-Match": "*"}, ExpiresAtUnix: 600}, m.err
}
func (m *mockMedia) CompleteUpload(ctx context.Context, req *mediav1.CompleteUploadRequest, _ ...grpc.CallOption) (*mediav1.CompleteUploadResponse, error) {
	m.capture(ctx, req.GetUserId(), req.GetFileId())
	return &mediav1.CompleteUploadResponse{File: m.file}, m.err
}
func (m *mockMedia) GetFile(ctx context.Context, req *mediav1.GetFileRequest, _ ...grpc.CallOption) (*mediav1.GetFileResponse, error) {
	m.capture(ctx, req.GetUserId(), req.GetFileId())
	return &mediav1.GetFileResponse{File: m.file}, m.err
}
func (m *mockMedia) CreateDownloadURL(ctx context.Context, req *mediav1.CreateDownloadURLRequest, _ ...grpc.CallOption) (*mediav1.CreateDownloadURLResponse, error) {
	m.capture(ctx, req.GetUserId(), req.GetFileId())
	return &mediav1.CreateDownloadURLResponse{DownloadUrl: "http://s3/download", ExpiresAtUnix: 600}, m.err
}
func mediaTestRouter(media *mockMedia, auth *mockAuth, user *mockUser) *mux.Router {
	router := testRouter(auth, user)
	RegisterMedia(router, media, auth, user, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return router
}
func mediaRequest(router *mux.Router, method, path, body, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestMediaRoutes(t *testing.T) {
	media := &mockMedia{file: &mediav1.File{Id: "file-id", OwnerUserId: "owner", IsPublic: true, Status: mediav1.FileStatus_FILE_STATUS_PENDING}}
	auth := &mockAuth{}
	router := mediaTestRouter(media, auth, &mockUser{})
	response := mediaRequest(router, "POST", "/api/v1/media/files", `{"original_name":"report.pdf","content_type":"application/pdf","size_bytes":5,"is_public":true}`, "Bearer token")
	if response.Code != 201 || media.create.GetUserId() != "4f9a4c95-6144-4ec8-89e8-3866207d7561" || !media.create.GetIsPublic() || media.create.GetSizeBytes() != 5 || !media.deadline || response.Header().Get("Location") != "/api/v1/media/files/file-id" {
		t.Fatalf("create: %d %s %+v", response.Code, response.Body, media.create)
	}
	var created struct {
		File      mediaFile         `json:"file"`
		UploadURL string            `json:"upload_url"`
		Headers   map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.File.Status != "pending" || !created.File.IsPublic || created.File.SizeBytes != nil || created.UploadURL == "" || created.Headers["If-None-Match"] != "*" {
		t.Fatalf("upload response: %s, %v", response.Body, err)
	}
	media.file.Status = mediav1.FileStatus_FILE_STATUS_READY
	for _, tc := range []struct{ method, suffix, token, wantUser string }{
		{"POST", "/complete", "Bearer token", media.create.GetUserId()},
		{"GET", "", "Bearer token", media.create.GetUserId()},
		{"GET", "", "", ""},
		{"GET", "/download-url", "", ""},
		{"GET", "/download-url", "Bearer token", media.create.GetUserId()},
	} {
		t.Run(tc.method+tc.suffix+tc.token, func(t *testing.T) {
			auth.validateRequest = nil
			response := mediaRequest(router, tc.method, "/api/v1/media/files/file-id"+tc.suffix, "", tc.token)
			if response.Code != 200 || media.userID != tc.wantUser || media.fileID != "file-id" || !media.deadline || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("route: %d %s user=%q", response.Code, response.Body, media.userID)
			}
			if tc.token == "" && auth.validateRequest != nil {
				t.Fatal("guest request contacted Auth")
			}
		})
	}
}

func TestMediaAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, method, suffix, token, email string
		authErr                            error
		want                               int
	}{
		{"missing create token", "POST", "", "", "", nil, 401},
		{"missing complete token", "POST", "/id/complete", "", "", nil, 401},
		{"malformed read token", "GET", "/id", "Basic secret", "", nil, 401},
		{"invalid read token", "GET", "/id", "Bearer bad", "", status.Error(codes.Unauthenticated, "bad"), 401},
		{"auth unavailable", "GET", "/id", "Bearer token", "", status.Error(codes.Unavailable, "private"), 503},
		{"unverified", "POST", "", "Bearer token", "pending", nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := &mockMedia{}
			router := mediaTestRouter(media, &mockAuth{validateErr: tc.authErr}, &mockUser{emailStatus: tc.email})
			response := mediaRequest(router, tc.method, "/api/v1/media/files"+tc.suffix, `{}`, tc.token)
			if response.Code != tc.want || media.calls != 0 {
				t.Fatalf("auth: %d %s calls=%d", response.Code, response.Body, media.calls)
			}
		})
	}
}

func TestMediaRejectsInvalidJSONAndOwnerSpoofing(t *testing.T) {
	for _, body := range []string{`{"user_id":"another-owner"}`, `{`, `{} {}`, `{"size_bytes":"5"}`, `{"is_public":"true"}`, `{"original_name":"` + strings.Repeat("x", 17000) + `"}`} {
		media := &mockMedia{}
		response := mediaRequest(mediaTestRouter(media, &mockAuth{}, &mockUser{}), "POST", "/api/v1/media/files", body, "Bearer token")
		if response.Code != 400 || media.calls != 0 {
			t.Fatalf("body accepted: %d calls=%d", response.Code, media.calls)
		}
	}
}

func TestMediaHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.NotFound, 404}, {codes.AlreadyExists, 409}, {codes.FailedPrecondition, 409},
		{codes.PermissionDenied, 403}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 500},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			media := &mockMedia{err: status.Error(tc.code, "private-db-details")}
			response := mediaRequest(mediaTestRouter(media, &mockAuth{}, &mockUser{}), "GET", "/api/v1/media/files/id/download-url", "", "")
			if response.Code != tc.want || strings.Contains(response.Body.String(), "private-db") {
				t.Fatalf("error: %d %s", response.Code, response.Body)
			}
		})
	}
}
