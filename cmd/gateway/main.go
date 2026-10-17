package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/config"
	delivery "github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/delivery/http"
	adminv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	projectv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	recommendationv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	searchv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	userv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/observability"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("Gateway остановлен", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "путь к конфигурации YAML")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	metrics, closeMetrics, err := observability.Start("gateway", *configPath, logger)
	if err != nil {
		return err
	}
	defer closeMetrics()
	version, err := migrations.Apply(ctx, cfg.DB)
	if err != nil {
		return err
	}
	logger.Info("миграции PostgreSQL применены", "version", version)

	dial := func(address string) (*grpc.ClientConn, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, err := grpc.DialContext(ctx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err != nil {
			return nil, fmt.Errorf("подключение к %s: %w", address, err)
		}
		return conn, nil
	}
	authConn, err := dial(cfg.Auth.Address())
	if err != nil {
		return err
	}
	defer func() {
		if err := authConn.Close(); err != nil {
			logger.Error("закрытие соединения с Auth", "error", err)
		}
	}()
	userConn, err := dial(cfg.User.Address())
	if err != nil {
		return err
	}
	defer func() {
		if err := userConn.Close(); err != nil {
			logger.Error("закрытие соединения с User", "error", err)
		}
	}()

	mediaConn, err := dial(cfg.Media.Address())
	if err != nil {
		return err
	}
	defer func() { _ = mediaConn.Close() }()
	supportConn, err := dial(cfg.Support.Address())
	if err != nil {
		return err
	}
	defer func() { _ = supportConn.Close() }()
	projectConn, err := dial(cfg.Project.Address())
	if err != nil {
		return err
	}
	defer projectConn.Close()
	searchConn, err := dial(cfg.Search.Address())
	if err != nil {
		return err
	}
	defer searchConn.Close()
	recommendationConn, err := dial(cfg.Recommendation.Address())
	if err != nil {
		return err
	}
	defer recommendationConn.Close()
	var adminConn *grpc.ClientConn
	if cfg.Admin.Host != "" {
		adminConn, err = grpc.NewClient(cfg.Admin.Address(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer adminConn.Close()
	}
	router := mux.NewRouter()
	router.Use(metrics.HTTP)
	router.NotFoundHandler = metrics.HTTP(http.NotFoundHandler())
	router.MethodNotAllowedHandler = metrics.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) }))
	healthcheck.RegisterHTTP(router)
	if adminConn != nil {
		delivery.RegisterAdmin(router, adminv1.NewAdminServiceClient(adminConn), logger)
	}
	delivery.Register(router, authv1.NewAuthServiceClient(authConn), userv1.NewUserServiceClient(userConn), logger)
	delivery.RegisterProfiles(router, userv1.NewProfileServiceClient(userConn), logger)
	delivery.RegisterMedia(router, mediav1.NewMediaServiceClient(mediaConn), authv1.NewAuthServiceClient(authConn), userv1.NewUserServiceClient(userConn), logger)
	delivery.RegisterSupport(router, supportv1.NewSupportServiceClient(supportConn), authv1.NewAuthServiceClient(authConn), userv1.NewUserServiceClient(userConn), logger)
	delivery.RegisterProjects(router, projectv1.NewProjectServiceClient(projectConn), searchv1.NewSearchServiceClient(searchConn), recommendationv1.NewRecommendationServiceClient(recommendationConn), logger)
	server := &http.Server{
		Handler:           delivery.Middleware(logger, delivery.CORS(cfg.CORS.AllowedOrigins, router)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.HTTP.Address())
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("Gateway запущен", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("Gateway завершает работу")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
