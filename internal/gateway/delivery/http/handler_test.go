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
	emailStatus           string
	emailStatusErr        error
	registerErr, loginErr error
	confirmErr            error
	registerRequest       *userv1.RegisterRequest
	loginRequest          *userv1.LoginRequest
	confirmRequest        *userv1.ConfirmEmailRequest
}

func (m *mockUser) Register(_ context.Context, req *userv1.RegisterRequest, _ ...grpc.CallOption) (*userv1.RegisterResponse, error) {
	m.registerRequest = req
	return &userv1.RegisterResponse{Status: "pending"}, m.registerErr
}
func (m *mockUser) Login(_ context.Context, req *userv1.LoginRequest, _ ...grpc.CallOption) (*userv1.LoginResponse, error) {
	m.loginRequest = req
	return &userv1.LoginResponse{UserId: "4f9a4c95-6144-4ec8-89e8-3866207d7561", SessionToken: "secret-token"}, m.loginErr
}
func (m *mockUser) ConfirmEmail(_ context.Context, req *userv1.ConfirmEmailRequest, _ ...grpc.CallOption) (*userv1.ConfirmEmailResponse, error) {
	m.confirmRequest = req
	return &userv1.ConfirmEmailResponse{}, m.confirmErr
}
func (m *mockUser) GetEmailStatus(_ context.Context, req *userv1.GetEmailStatusRequest, _ ...grpc.CallOption) (*userv1.GetEmailStatusResponse, error) {
	state := m.emailStatus
	if state == "" {
		state = "verified"
	}
	return &userv1.GetEmailStatusResponse{Email: "a@example.org", Status: state}, m.emailStatusErr
}

func testRouter(auth *mockAuth, user *mockUser) *mux.Router {
	router := mux.NewRouter()
	Register(router, auth, user, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return router
}

func TestRegisterAndLoginRoutes(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{}
	router := testRouter(auth, user)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"test@hse.ru","password":"long-password"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusCreated || user.registerRequest.GetEmail() != "test@hse.ru" || !strings.Contains(response.Body.String(), `"status":"pending"`) || strings.Contains(response.Body.String(), `"user_id"`) {
		t.Fatalf("ответ регистрации %d %q", response.Code, response.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"test@hse.ru","password":"long-password"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"access_token":"secret-token"`) {
		t.Fatalf("ответ входа %d %q", response.Code, response.Body.String())
	}
}

func TestEmailVerificationRoutes(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{}
	router := testRouter(auth, user)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/email", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"verified"`) {
		t.Fatalf("ответ о состоянии адреса %d %q", response.Code, response.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/email/confirm", strings.NewReader(`{"token":"verification-token"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || user.confirmRequest.GetToken() != "verification-token" {
		t.Fatalf("ответ подтверждения адреса %d %q", response.Code, response.Body.String())
	}
}

func TestEmailOrganizationUnavailable(t *testing.T) {
	user := &mockUser{registerErr: status.Error(codes.PermissionDenied, "подтверждение почты для этой организации недоступно"), confirmErr: status.Error(codes.PermissionDenied, "organization unavailable")}
	router := testRouter(&mockAuth{}, user)
	for _, tc := range []struct {
		path string
		body string
		auth bool
	}{
		{"/api/v1/auth/register", `{"email":"student@bmstu.ru","password":"long-password"}`, false},
		{"/api/v1/auth/email/confirm", `{"token":"verification-token"}`, false},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		if tc.auth {
			req.Header.Set("Authorization", "Bearer session-token")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "организации недоступно") {
			t.Errorf("%s: статус %d, тело %q", tc.path, response.Code, response.Body.String())
		}
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
		t.Fatalf("токен сеанса не передан в Auth: проверка %#v, отзыв %#v", auth.validateRequest, auth.revokeRequest)
	}
}

func TestAuthenticatedSubrouterAddsUserIDToContext(t *testing.T) {
	auth := &mockAuth{}
	router := mux.NewRouter()
	protected := AuthenticatedSubrouter(router, "/api/v1/projects", auth, &mockUser{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(AuthenticatedUserID(r.Context())))
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "4f9a4c95-6144-4ec8-89e8-3866207d7561") {
		t.Fatalf("ответ защищённого маршрута: статус %d, тело %q", response.Code, response.Body.String())
	}
}

func TestMapsServiceErrorsAndRejectsBadJSON(t *testing.T) {
	auth, user := &mockAuth{}, &mockUser{registerErr: status.Error(codes.AlreadyExists, "duplicate")}
	router := testRouter(auth, user)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"test@hse.ru","password":"long-password"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("статус повторной регистрации %d", response.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("статус некорректного JSON %d", response.Code)
	}
}

func TestMiddlewareRecoversAndAddsRequestID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("в контексте отсутствует идентификатор запроса")
		}
		panic("тестовая паника")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("статус %d, идентификатор запроса %q", response.Code, response.Header().Get("X-Request-ID"))
	}
}

func TestPendingRegistrationAndLoginBeforeConfirmation(t *testing.T) {
	user := &mockUser{loginErr: status.Error(codes.Unauthenticated, "неверный email или пароль")}
	router := testRouter(&mockAuth{}, user)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"student@hse.ru","password":"long-password"}`)))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"pending"`) || strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("pending registration: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"student@hse.ru","password":"long-password"}`)))
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("pending login: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"login":"old_login","password":"long-password"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatal("legacy login field accepted")
	}
}

func TestOldUnverifiedSessionsCannotUseProtectedRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		err         error
		want        int
	}{
		{"pending", "pending", nil, http.StatusForbidden},
		{"legacy without email", "not_set", nil, http.StatusForbidden},
		{"User unavailable", "", status.Error(codes.Unavailable, "unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, auth := &mockUser{emailStatus: tc.state, emailStatusErr: tc.err}, &mockAuth{}
			router := testRouter(auth, user)
			protected := AuthenticatedSubrouter(router, "/private", auth, user, slog.New(slog.NewTextHandler(io.Discard, nil)))
			called := false
			protected.HandleFunc("/test", func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusOK) })
			for _, path := range []string{"/private/test", "/api/v1/users/me/email", "/api/v1/sessions/current"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer old-session")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != tc.want || called {
					t.Fatalf("%s: status %d, called %t", path, response.Code, called)
				}
			}
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/current", nil)
			req.Header.Set("Authorization", "Bearer old-session")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusNoContent {
				t.Fatal("old session cannot be revoked")
			}
		})
	}
}
