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
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/config"
	delivery "github.com/HSE-Scientists-Team/matchlab-backend/internal/media/delivery/grpc"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/storage/s3"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/usecase"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/observability"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	configPath := flag.String("config", "/etc/app/config.yaml", "путь к конфигурации YAML")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		metrics, closeMetrics, metricsErr := observability.Start("media", *configPath, logger)
		if metricsErr != nil {
			err = metricsErr
		} else {
			defer closeMetrics()
			err = runWithMetrics(ctx, cfg, logger, metrics)
		}
	}
	if err != nil {
		logger.Error("сервис Media остановлен", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return runWithMetrics(ctx, cfg, logger, observability.New("media"))
}

func runWithMetrics(ctx context.Context, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	version, err := migrations.Apply(ctx, cfg.DB)
	if err != nil {
		return err
	}
	logger.Info("миграции PostgreSQL применены", "version", version)

	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return fmt.Errorf("открытие соединения с PostgreSQL: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("закрытие соединения с PostgreSQL", "error", err)
		}
	}()
	metrics.Database(db, "media")
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(startupCtx)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("проверка PostgreSQL: %w", err)
	}
	storage, err := s3.New(cfg.S3)
	if err != nil {
		return err
	}
	if err := storage.Check(ctx); err != nil {
		return fmt.Errorf("проверка S3 bucket: %w", err)
	}
	media, err := usecase.NewService(repository.NewPostgres(db), storage, usecase.Options{
		Bucket: cfg.S3.Bucket, MaxSizeBytes: cfg.Upload.MaxSizeBytes,
		AllowedContentTypes: cfg.Upload.AllowedContentTypes,
		UploadTTL:           cfg.Upload.URLTTL, DownloadTTL: cfg.Download.URLTTL,
		MultipartPartSize: cfg.Multipart.PartSizeBytes, MultipartTTL: cfg.Multipart.SessionTTL,
	})
	if err != nil {
		return err
	}
	cleanupCtx, cancelCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		interval := cfg.Multipart.CleanupInterval
		if interval <= 0 {
			interval = 10 * time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := media.RecoverPending(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
				logger.Error("не удалось обработать устаревшие pending-файлы")
			}
			if err := media.RecoverDeletions(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
				logger.Error("не удалось завершить удаление файлов")
			}
			if err := media.RecoverMultipart(cleanupCtx); err != nil && cleanupCtx.Err() == nil {
				logger.Error("не удалось восстановить или очистить multipart-загрузки")
			}
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { cancelCleanup(); <-cleanupDone }()
	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("запуск прослушивания gRPC: %w", err)
	}
	defer listener.Close()
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(metrics.UnaryServerInterceptor, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		started := time.Now()
		response, err := handler(callCtx, req)
		// Не записываем запросы, ответы, ключи и подписанные URL.
		logger.InfoContext(ctx, "gRPC-запрос", "method", info.FullMethod, "grpc_code", status.Code(err), "duration", time.Since(started))
		return response, err
	}))
	health := healthcheck.RegisterGRPC(server)
	health.SetServingStatus(mediav1.MediaService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	mediav1.RegisterMediaServiceServer(server, delivery.NewServer(media))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("сервис Media запущен", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
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
