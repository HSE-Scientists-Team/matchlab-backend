package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	userv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockAuth struct {
	validateErr, revokeErr error
	validateRequest        *authv1.ValidateSessionRequest
	revokeRequest          *authv1.RevokeSessionRequest
}

func (m *mockAuth) ValidateSession(_ context.Context, req *authv1.ValidateSessionRequest, _ ...grpc.CallOption) (*authv1.ValidateSessionResponse, error) {
	m.validateRequest = req
	return &authv1.ValidateSessionResponse{UserId: "4f9a4c95-6144-4ec8-89e8-3866207d7561"}, m.validateErr
}
func (m *mockAuth) RevokeSession(_ context.Context, req *authv1.RevokeSessionRequest, _ ...grpc.CallOption) (*authv1.RevokeSessionResponse, error) {
	m.revokeRequest = req
	return &authv1.RevokeSessionResponse{}, m.revokeErr
}

type mockUser struct {
	registerErr, loginErr error
	registerRequest       *userv1.RegisterRequest
	loginRequest          *userv1.LoginRequest
	emailRequest          *userv1.RequestEmailVerificationRequest
	confirmRequest        *userv1.ConfirmEmailRequest
}

func (m *mockUser) Register(_ context.Context, req *userv1.RegisterRequest, _ ...grpc.CallOption) (*userv1.RegisterResponse, error) {
	m.registerRequest = req
	return &userv1.RegisterResponse{UserId: "4f9a4c95-6144-4ec8-89e8-3866207d7561"}, m.registerErr
}
func (m *mockUser) Login(_ context.Context, req *userv1.LoginRequest, _ ...grpc.CallOption) (*userv1.LoginResponse, error) {
	m.loginRequest = req
	return &userv1.LoginResponse{UserId: "4f9a4c95-6144-4ec8-89e8-3866207d7561", SessionToken: "secret-token"}, m.loginErr
}
func (m *mockUser) RequestEmailVerification(_ context.Context, req *userv1.RequestEmailVerificationRequest, _ ...grpc.CallOption) (*userv1.RequestEmailVerificationResponse, error) {
	m.emailRequest = req
	return &userv1.RequestEmailVerificationResponse{}, nil
}
func (m *mockUser) ConfirmEmail(_ context.Context, req *userv1.ConfirmEmailRequest, _ ...grpc.CallOption) (*userv1.ConfirmEmailResponse, error) {
	m.confirmRequest = req
	return &userv1.ConfirmEmailResponse{}, nil
}
func (m *mockUser) GetEmailStatus(_ context.Context, req *userv1.GetEmailStatusRequest, _ ...grpc.CallOption) (*userv1.GetEmailStatusResponse, error) {
	return &userv1.GetEmailStatusResponse{Email: "a@example.org", Status: "pending"}, nil
}

func testRouter(auth *mockAuth, user *mockUser) *mux.Router {
	router := mux.NewRouter()
	Register(router, auth, user, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return router
}

func TestRegisterAndLoginRoutes(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{}
	router := testRouter(auth, user)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"login":"test_user","password":"long-password"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusCreated || user.registerRequest.GetLogin() != "test_user" || !strings.Contains(response.Body.String(), `"user_id"`) {
		t.Fatalf("register response %d %q", response.Code, response.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"login":"test_user","password":"long-password"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"access_token":"secret-token"`) {
		t.Fatalf("login response %d %q", response.Code, response.Body.String())
	}
}

func TestEmailVerificationRoutes(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{}
	router := testRouter(auth, user)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/email", strings.NewReader(`{"email":"a@example.org"}`))
	req.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusAccepted || user.emailRequest.GetUserId() != "4f9a4c95-6144-4ec8-89e8-3866207d7561" {
		t.Fatalf("request email response %d %q, request %#v", response.Code, response.Body.String(), user.emailRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/users/me/email", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"pending"`) {
		t.Fatalf("email status response %d %q", response.Code, response.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/email/confirm", strings.NewReader(`{"token":"verification-token"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || user.confirmRequest.GetToken() != "verification-token" {
		t.Fatalf("confirm email response %d %q", response.Code, response.Body.String())
	}
}

func TestSessionRoutesDelegateToAuth(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{}
	router := testRouter(auth, user)
	for _, tc := range []struct {
		method string
		want   int
	}{{http.MethodGet, http.StatusOK}, {http.MethodDelete, http.StatusNoContent}} {
		req := httptest.NewRequest(tc.method, "/api/v1/sessions/current", nil)
		req.Header.Set("Authorization", "Bearer session-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("%s: status %d, want %d", tc.method, response.Code, tc.want)
		}
	}
	if auth.validateRequest.GetToken() != "session-token" || auth.revokeRequest.GetToken() != "session-token" {
		t.Fatalf("session token was not delegated to Auth: validate %#v revoke %#v", auth.validateRequest, auth.revokeRequest)
	}
}

func TestAuthenticatedSubrouterAddsUserIDToContext(t *testing.T) {
	auth := &mockAuth{}
	router := mux.NewRouter()
	protected := AuthenticatedSubrouter(router, "/api/v1/projects", auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(AuthenticatedUserID(r.Context())))
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "4f9a4c95-6144-4ec8-89e8-3866207d7561") {
		t.Fatalf("protected response status %d body %q", response.Code, response.Body.String())
	}
}

func TestMapsServiceErrorsAndRejectsBadJSON(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{registerErr: status.Error(codes.AlreadyExists, "duplicate")}
	router := testRouter(auth, user)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"login":"test_user","password":"long-password"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate registration status %d", response.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status %d", response.Code)
	}
}

func TestMiddlewareRecoversAndAddsRequestID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("request ID missing from context")
		}
		panic("test panic")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status %d, request ID %q", response.Code, response.Header().Get("X-Request-ID"))
	}
}
