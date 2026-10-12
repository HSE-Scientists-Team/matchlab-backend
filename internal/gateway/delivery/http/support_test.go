package http

import (
	"context"
	"encoding/json"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockSupport struct {
	req      *supportv1.CreateComplaintRequest
	err      error
	deadline bool
}

func (s *mockSupport) CreateComplaint(ctx context.Context, req *supportv1.CreateComplaintRequest, _ ...grpc.CallOption) (*supportv1.CreateComplaintResponse, error) {
	s.req = req
	_, s.deadline = ctx.Deadline()
	return &supportv1.CreateComplaintResponse{Id: "created", Status: supportv1.SupportTicketStatus_SUPPORT_TICKET_STATUS_OPEN, CreatedAtUnix: 100}, s.err
}
func TestSupportRoute(t *testing.T) {
	request := func(body, token string, s *mockSupport, auth *mockAuth, user *mockUser) *httptest.ResponseRecorder {
		router := mux.NewRouter()
		RegisterSupport(router, s, auth, user, slog.New(slog.NewTextHandler(io.Discard, nil)))
		req := httptest.NewRequest("POST", "/api/v1/complaints", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	body := `{"subject":"Жалоба","description":"Описание","target_user_id":"00000000-0000-4000-8000-000000000002","file_ids":["00000000-0000-4000-8000-000000000003"]}`
	s := &mockSupport{}
	response := request(body, "Bearer token", s, &mockAuth{}, &mockUser{})
	var result struct {
		ID, Status string
		CreatedAt  string `json:"created_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 201 || result.ID != "created" || result.Status != "open" || result.CreatedAt != "1970-01-01T00:01:40Z" || s.req.GetUserId() != "4f9a4c95-6144-4ec8-89e8-3866207d7561" || s.req.GetTargetUserId() != "00000000-0000-4000-8000-000000000002" || len(s.req.GetFileIds()) != 1 || !s.deadline {
		t.Fatalf("create: %d %s %+v", response.Code, response.Body, s)
	}
	for _, tc := range []struct {
		name, body, token string
		auth              *mockAuth
		user              *mockUser
		want              int
	}{
		{"anonymous", body, "", &mockAuth{}, &mockUser{}, 401},
		{"invalid session", body, "Bearer token", &mockAuth{validateErr: status.Error(codes.Unauthenticated, "bad")}, &mockUser{}, 401},
		{"unverified", body, "Bearer token", &mockAuth{}, &mockUser{emailStatus: "pending"}, 403},
		{"spoof author", strings.Replace(body, `"subject"`, `"user_id":"other","subject"`, 1), "Bearer token", &mockAuth{}, &mockUser{}, 400},
		{"missing target", `{"subject":"a","description":"b"}`, "Bearer token", &mockAuth{}, &mockUser{}, 400},
		{"both targets", `{"subject":"a","description":"b","target_user_id":"a","target_project_id":"b"}`, "Bearer token", &mockAuth{}, &mockUser{}, 400},
		{"null target", `{"subject":"a","description":"b","target_user_id":null}`, "Bearer token", &mockAuth{}, &mockUser{}, 400},
		{"trailing JSON", body + ` {}`, "Bearer token", &mockAuth{}, &mockUser{}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockSupport{}
			r := request(tc.body, tc.token, s, tc.auth, tc.user)
			if r.Code != tc.want || s.req != nil {
				t.Fatalf("got %d want %d; forwarded=%v", r.Code, tc.want, s.req)
			}
		})
	}
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.NotFound, 404}, {codes.FailedPrecondition, 409}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 503}, {codes.Internal, 500}} {
		s := &mockSupport{err: status.Error(tc.code, "failed")}
		if r := request(body, "Bearer token", s, &mockAuth{}, &mockUser{}); r.Code != tc.want {
			t.Fatalf("RPC %v: %d", tc.code, r.Code)
		}
	}
}
