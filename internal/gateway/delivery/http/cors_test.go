package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowsScalarToCallGateway(t *testing.T) {
	handler := CORS([]string{"http://localhost:8084"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/users/me/email", nil)
	preflight.Header.Set("Origin", "http://localhost:8084")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, preflight)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8084" || response.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("предварительный запрос Scalar: статус %d, заголовки %v", response.Code, response.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/email", nil)
	request.Header.Set("Origin", "http://localhost:8084")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8084" {
		t.Fatalf("запрос Scalar: статус %d, заголовки %v", response.Code, response.Header())
	}
}

func TestCORSRejectsOtherBrowserOrigins(t *testing.T) {
	handler := CORS([]string{"http://localhost:8084"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	request.Header.Set("Origin", "https://untrusted.example")
	request.Header.Set("Access-Control-Request-Method", "POST")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("чужой источник: статус %d, заголовки %v", response.Code, response.Header())
	}
}
