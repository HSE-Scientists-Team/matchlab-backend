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

	mailv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/mail/v1"
	mailconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/config"
	mailgrpc "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/delivery/grpc"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/service"
	mailsmtp "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/smtp"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("сервис Mail остановлен", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "путь к конфигурации YAML")
	flag.Parse()
	cfg, err := mailconfig.Load(*configPath)
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

	sender := mailsmtp.NewSender(cfg.SMTP.SenderConfig())
	mailService := service.NewService(sender)
	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("запуск прослушивания gRPC: %w", err)
	}
	server := grpc.NewServer()
	health := healthcheck.RegisterGRPC(server)
	mailv1.RegisterEmailServiceServer(server, mailgrpc.NewServer(mailService))

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("сервис Mail запущен", "address", listener.Addr().String())
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
