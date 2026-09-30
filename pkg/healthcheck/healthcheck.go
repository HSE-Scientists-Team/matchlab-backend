// Package healthcheck регистрирует проверки работоспособности в существующем
// HTTP-маршрутизаторе или gRPC-сервере. Процесс выбирает свой транспорт.
package healthcheck

import (
	"net/http"

	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// RegisterHTTP добавляет GET /health в маршрутизатор. Успешный ответ означает,
// что HTTP-сервер принимает и обрабатывает запросы; зависимости не проверяются.
func RegisterHTTP(router *mux.Router) {
	router.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}).Methods(http.MethodGet)
}

// RegisterGRPC добавляет стандартный сервис grpc.health.v1.Health и возвращает
// средство управления состоянием. Пустое имя сервиса сначала имеет состояние SERVING.
// Когда процесс больше не может обрабатывать запросы, вызовите
// SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING), а при
// корректном завершении работы — Shutdown.
func RegisterGRPC(server *grpc.Server) *health.Server {
	status := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, status)
	return status
}
