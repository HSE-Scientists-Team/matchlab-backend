// Package healthcheck registers liveness endpoints on an existing HTTP router
// or gRPC server. Each process chooses the transport it serves.
package healthcheck

import (
	"net/http"

	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// RegisterHTTP adds GET /health to router. A successful response confirms that
// the HTTP server can accept and handle requests; it does not check dependencies.
func RegisterHTTP(router *mux.Router) {
	router.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}).Methods(http.MethodGet)
}

// RegisterGRPC adds the standard grpc.health.v1.Health service to server and
// returns its status manager. The empty service name starts as SERVING. Call
// SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING) when the
// process cannot serve traffic, or Shutdown during graceful shutdown.
func RegisterGRPC(server *grpc.Server) *health.Server {
	status := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, status)
	return status
}
