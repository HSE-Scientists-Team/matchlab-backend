package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/config"
	authgrpc "github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/delivery/grpc"
	redisrepo "github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/repository/redis"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/usecase"
	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("сервис Auth остановлен", "error", err)
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
	version, err := migrations.Apply(ctx, cfg.DB)
	if err != nil {
		return err
	}
	logger.Info("миграции PostgreSQL применены", "version", version)

	client := redis.NewClient(&redis.Options{Addr: cfg.Redis.Address(), Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("закрытие соединения с Redis", "error", err)
		}
	}()
	pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
	err = client.Ping(pingCtx).Err()
	cancelPing()
	if err != nil {
		return fmt.Errorf("подключение к Redis: %w", err)
	}

	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("запуск прослушивания gRPC: %w", err)
	}
	server := grpc.NewServer()
	health := healthcheck.RegisterGRPC(server)
	authv1.RegisterAuthServiceServer(server, authgrpc.NewServer(usecase.NewSessionService(redisrepo.NewSessionStore(redisrepo.NewClientAdapter(client)))))

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("сервис Auth запущен", "address", listener.Addr().String())

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
