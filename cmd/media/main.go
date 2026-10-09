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
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("сервис Media остановлен", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return fmt.Errorf("открытие соединения с PostgreSQL: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("закрытие соединения с PostgreSQL", "error", err)
		}
	}()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(startupCtx)
	if err == nil {
		// Проверка схемы не применяет миграции: это ответственность Migrator.
		var exists bool
		err = db.QueryRowContext(startupCtx, `SELECT to_regclass('media.file') IS NOT NULL`).Scan(&exists)
		if err == nil && !exists {
			err = fmt.Errorf("таблица media.file отсутствует: запустите Migrator")
		}
	}
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
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("запуск прослушивания gRPC: %w", err)
	}
	defer listener.Close()
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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
