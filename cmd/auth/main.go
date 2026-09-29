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
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("auth service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "path to YAML configuration")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	client := redis.NewClient(&redis.Options{Addr: cfg.Redis.Address(), Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("close Redis", "error", err)
		}
	}()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 3*time.Second)
	err = client.Ping(pingCtx).Err()
	cancelPing()
	if err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}

	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("listen for gRPC: %w", err)
	}
	server := grpc.NewServer()
	health := healthcheck.RegisterGRPC(server)
	authv1.RegisterAuthServiceServer(server, authgrpc.NewServer(usecase.NewSessionService(redisrepo.NewSessionStore(redisrepo.NewClientAdapter(client)))))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("auth service started", "address", listener.Addr().String())

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
