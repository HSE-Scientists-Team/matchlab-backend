// Package observability exposes bounded-label metrics on a dedicated internal listener.
package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Version and Revision are set by the image build. Local builds use VCS metadata.
var Version = "dev"
var Revision = "unknown"

type Metrics struct {
	Registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight *prometheus.GaugeVec
}

func New(service string) *Metrics {
	registry := prometheus.NewRegistry()
	register := prometheus.WrapRegistererWith(prometheus.Labels{"service": service}, registry)
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "matchlab_requests_total", Help: "Completed application requests; result is HTTP status or gRPC code."}, []string{"protocol", "method", "route", "result"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "matchlab_request_duration_seconds", Help: "Application request latency.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"protocol", "method", "route"})
	active := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "matchlab_requests_in_flight", Help: "Currently executing application requests."}, []string{"protocol"})
	revision := Revision
	if revision == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					revision = s.Value
				}
			}
		}
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "matchlab_build_info", Help: "Running binary version and source revision."}, []string{"version", "revision"})
	register.MustRegister(requests, duration, active, build, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	build.WithLabelValues(Version, revision).Set(1)
	return &Metrics{Registry: registry, requests: requests, duration: duration, inFlight: active}
}

// Start binds before returning, so configuration and port errors fail startup.
// An omitted metrics.address defaults to loopback; an empty address disables it.
func Start(service, configFile string, logger *slog.Logger) (*Metrics, func(), error) {
	v := viper.New()
	v.SetConfigFile(configFile)
	v.SetDefault("metrics.address", "127.0.0.1:9090")
	if err := v.ReadInConfig(); err != nil {
		return nil, nil, err
	}
	m := New(service)
	address := v.GetString("metrics.address")
	if address == "" {
		return m, func() {}, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics listener: %w", err)
	}
	router := http.NewServeMux()
	router.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 5 * time.Second}))
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server stopped", "error", err)
		}
	}()
	closeServer := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
		<-done
	}
	return m, closeServer, nil
}

func (m *Metrics) Database(db *sql.DB, name string) {
	prometheus.WrapRegistererWith(prometheus.Labels{"service": name}, m.Registry).MustRegister(collectors.NewDBStatsCollector(db, "postgres"))
}

func (m *Metrics) UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.") {
		return next(ctx, req)
	}
	started := time.Now()
	m.inFlight.WithLabelValues("grpc").Inc()
	defer m.inFlight.WithLabelValues("grpc").Dec()
	response, err := next(ctx, req)
	m.requests.WithLabelValues("grpc", "unary", info.FullMethod, status.Code(err).String()).Inc()
	m.duration.WithLabelValues("grpc", "unary", info.FullMethod).Observe(time.Since(started).Seconds())
	return response, err
}

// HTTP must be registered with Router.Use: mux resolves the route template before
// invoking middleware. Raw URLs, query strings, identifiers and credentials are never labels.
func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		route := "unmatched"
		if current := mux.CurrentRoute(r); current != nil {
			if template, err := current.GetPathTemplate(); err == nil {
				route = template
			}
		}
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			method = "OTHER"
		}
		m.inFlight.WithLabelValues("http").Inc()
		defer m.inFlight.WithLabelValues("http").Dec()
		result := httpsnoop.CaptureMetrics(next, w, r)
		m.requests.WithLabelValues("http", method, route, strconv.Itoa(result.Code)).Inc()
		m.duration.WithLabelValues("http", method, route).Observe(result.Duration.Seconds())
	})
}
