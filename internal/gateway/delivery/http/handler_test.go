package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository"
	"github.com/gorilla/mux"
)

type mockSessions struct {
	getToken, revokeToken string
	getErr, revokeErr     error
}

func (m *mockSessions) Create(context.Context, string) (string, error) { panic("unexpected Create") }
func (m *mockSessions) Get(_ context.Context, token string) (string, error) {
	m.getToken = token
	return "user-1", m.getErr
}
func (m *mockSessions) Revoke(_ context.Context, token string) error {
	m.revokeToken = token
	return m.revokeErr
}

func TestSessionRoutes(t *testing.T) {
	mock := &mockSessions{}
	router := mux.NewRouter()
	Register(router, mock, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, tc := range []struct {
		method, authorization string
		wantStatus            int
	}{
		{http.MethodGet, "Bearer token", http.StatusOK},
		{http.MethodDelete, "Bearer token", http.StatusNoContent},
		{http.MethodGet, "", http.StatusUnauthorized},
		{http.MethodGet, "Basic token", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(tc.method, "/sessions/current", nil)
		req.Header.Set("Authorization", tc.authorization)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.wantStatus {
			t.Fatalf("%s %q: status %d, want %d", tc.method, tc.authorization, response.Code, tc.wantStatus)
		}
		if tc.method == http.MethodGet && tc.wantStatus == http.StatusOK && !strings.Contains(response.Body.String(), `"user_id":"user-1"`) {
			t.Fatalf("GET body: %q", response.Body.String())
		}
	}
	if mock.getToken != "token" || mock.revokeToken != "token" {
		t.Fatalf("tokens passed to usecase: get %q, revoke %q", mock.getToken, mock.revokeToken)
	}
	mock.getErr = repository.ErrNotFound
	req := httptest.NewRequest(http.MethodGet, "/sessions/current", nil)
	req.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing session: status %d", response.Code)
	}
	mock.getErr = errors.New("redis down")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("store error: status %d", response.Code)
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
