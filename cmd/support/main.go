package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/client"
	supportconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/support/config"
	supportgrpc "github.com/HSE-Scientists-Team/matchlab-backend/internal/support/delivery/grpc"
	supportmanagement "github.com/HSE-Scientists-Team/matchlab-backend/internal/support/management"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/usecase"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/observability"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("сервис Support остановлен", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "путь к конфигурации YAML")
	flag.Parse()
	cfg, err := supportconfig.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	metrics, closeMetrics, err := observability.Start("support", *configPath, logger)
	if err != nil {
		return err
	}
	defer closeMetrics()
	version, err := migrations.Apply(ctx, cfg.DB)
	if err != nil {
		return err
	}
	logger.Info("миграции PostgreSQL применены", "version", version)

	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	metrics.Database(db, "support")
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(startupCtx)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("подключение к PostgreSQL: %w", err)
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	mediaConn, err := grpc.DialContext(dialCtx, cfg.Media.Address(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	cancelDial()
	if err != nil {
		return fmt.Errorf("подключение к Media: %w", err)
	}
	defer func() { _ = mediaConn.Close() }()
	complaints := usecase.NewService(repository.NewPostgres(db), client.NewMedia(mediav1.NewMediaServiceClient(mediaConn)))
	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("запуск прослушивания gRPC: %w", err)
	}
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(metrics.UnaryServerInterceptor))
	health := healthcheck.RegisterGRPC(server)
	supportv1.RegisterSupportManagementServiceServer(server, &supportmanagement.Service{DB: db, Media: mediav1.NewMediaServiceClient(mediaConn)})
	supportv1.RegisterSupportServiceServer(server, supportgrpc.NewServer(complaints, logger))
	health.SetServingStatus("matchlab.support.v1.SupportService", grpc_health_v1.HealthCheckResponse_SERVING)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("сервис Support запущен", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		stop()
		return err
	case <-ctx.Done():
		health.Shutdown()
		stopped := make(chan struct{})
		go func() { server.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			server.Stop()
		}
		return nil
	}
}
