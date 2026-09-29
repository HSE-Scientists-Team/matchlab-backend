package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestRegisterHTTP(t *testing.T) {
	router := mux.NewRouter()
	RegisterHTTP(router)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("GET /health: status %d, body %q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/health", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health: status %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestRegisterGRPC(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	status := RegisterGRPC(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(
		func(context.Context, string) (net.Conn, error) { return listener.Dial() },
	))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := grpc_health_v1.NewHealthClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, want := range []grpc_health_v1.HealthCheckResponse_ServingStatus{
		grpc_health_v1.HealthCheckResponse_SERVING,
		grpc_health_v1.HealthCheckResponse_NOT_SERVING,
	} {
		if want == grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			status.SetServingStatus("", want)
		}
		response, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if response.Status != want {
			t.Fatalf("состояние здоровья = %s, ожидалось %s", response.Status, want)
		}
	}
}
