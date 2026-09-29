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

	mailv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/mail/v1"
	mailconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/config"
	mailgrpc "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/delivery/grpc"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/service"
	mailsmtp "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/smtp"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("mail service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "path to YAML configuration")
	flag.Parse()
	cfg, err := mailconfig.Load(*configPath)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("close PostgreSQL", "error", err)
		}
	}()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.PingContext(startupCtx); err != nil {
		cancelStartup()
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	version, err := migrations.Up(startupCtx, db)
	cancelStartup()
	if err != nil {
		return err
	}
	logger.Info("mail database ready", "migration_version", version)

	queueRepo := repository.NewPostgres(db)
	queue, err := service.NewQueue(queueRepo, cfg.CipherKey)
	if err != nil {
		return err
	}
	sender := mailsmtp.NewSender(cfg.SMTP.SenderConfig())
	worker := service.NewWorker(queueRepo, queue, sender, service.WorkerConfig{
		PollInterval: cfg.Worker.PollInterval,
		Lease:        cfg.Worker.Lease,
		MaxAttempts:  cfg.Worker.MaxAttempts,
		Retention:    cfg.Worker.Retention,
	}, logger)
	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("listen for gRPC: %w", err)
	}
	server := grpc.NewServer()
	health := healthcheck.RegisterGRPC(server)
	mailv1.RegisterEmailServiceServer(server, mailgrpc.NewServer(queue))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	workerDone := make(chan struct{})
	go func() { serveErr <- server.Serve(listener) }()
	go func() { worker.Run(ctx); close(workerDone) }()
	logger.Info("mail service started", "address", listener.Addr().String())
	select {
	case err := <-serveErr:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		stop()
		<-workerDone
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
		select {
		case <-workerDone:
		case <-time.After(10 * time.Second):
		}
		return nil
	}
}
