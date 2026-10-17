package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Body.String()
}
func TestHTTPUsesTemplateAndPreservesStatus(t *testing.T) {
	m := New("gateway")
	r := mux.NewRouter()
	r.Use(m.HTTP)
	r.HandleFunc("/profiles/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }).Methods("GET")
	for _, id := range []string{"private-user-1", "private-user-2"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/profiles/"+id+"?token=secret-query", nil))
	}
	text := scrape(t, m)
	if strings.Contains(text, "private-user") || strings.Contains(text, "secret-query") {
		t.Fatal("sensitive labels", text)
	}
	if !strings.Contains(text, `matchlab_requests_total{method="GET",protocol="http",result="403",route="/profiles/{id}",service="gateway"} 2`) {
		t.Fatal(text)
	}
	if !strings.Contains(text, `matchlab_requests_in_flight{protocol="http",service="gateway"} 0`) {
		t.Fatal(text)
	}
}
func TestGRPCErrorsAndHealthExclusion(t *testing.T) {
	m := New("project")
	call := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.PermissionDenied, "sensitive-reason")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/matchlab.project.v1.ProjectService/CreateProject"}
	_, err := m.UnaryServerInterceptor(context.Background(), nil, info, call)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	_, _ = m.UnaryServerInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, call)
	text := scrape(t, m)
	if !strings.Contains(text, `result="PermissionDenied"`) {
		t.Fatal(text)
	}
	if strings.Contains(text, "sensitive-reason") || strings.Contains(text, "/grpc.health.") {
		t.Fatal(text)
	}
	if !strings.Contains(text, `matchlab_requests_in_flight{protocol="grpc",service="project"} 0`) {
		t.Fatal(text)
	}
}
func TestListenerLifecycleAndConflict(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(fmt.Sprintf("metrics:\n  address: %s\n", address)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Start("auth", file, slog.Default()); err == nil {
		t.Fatal("occupied listener must fail")
	}
	_ = listener.Close()
	m, closeServer, err := Start("auth", file, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	response, err := http.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if !strings.Contains(scrape(t, m), "matchlab_build_info") {
		t.Fatal("no build info")
	}
}
