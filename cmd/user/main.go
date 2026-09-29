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

	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	mailv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/mail/v1"
	userv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	userconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/user/config"
	usergrpc "github.com/HSE-Scientists-Team/matchlab-backend/internal/user/delivery/grpc"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/usecase"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("user service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/app/config.yaml", "path to YAML configuration")
	flag.Parse()
	cfg, err := userconfig.Load(*configPath)
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
	logger.Info("user database ready", "migration_version", version)

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	authConn, err := grpc.DialContext(dialCtx, cfg.Auth.Address(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	cancelDial()
	if err != nil {
		return fmt.Errorf("connect to auth gRPC service: %w", err)
	}
	defer func() {
		if err := authConn.Close(); err != nil {
			logger.Error("close auth gRPC connection", "error", err)
		}
	}()
	mailCtx, cancelMail := context.WithTimeout(context.Background(), 5*time.Second)
	mailConn, err := grpc.DialContext(mailCtx, cfg.Mail.Address(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	cancelMail()
	if err != nil {
		return fmt.Errorf("connect to mail gRPC service: %w", err)
	}
	defer func() {
		if err := mailConn.Close(); err != nil {
			logger.Error("close mail gRPC connection", "error", err)
		}
	}()
	authClient := authv1.NewAuthServiceClient(authConn)
	users := usecase.NewService(repository.NewPostgres(db), authSessionClient{client: authClient}, mailEmailClient{client: mailv1.NewEmailServiceClient(mailConn)})

	listener, err := net.Listen("tcp", cfg.GRPC.Address())
	if err != nil {
		return fmt.Errorf("listen for gRPC: %w", err)
	}
	server := grpc.NewServer()
	health := healthcheck.RegisterGRPC(server)
	userv1.RegisterUserServiceServer(server, usergrpc.NewServer(users))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("user service started", "address", listener.Addr().String())
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

type mailEmailClient struct{ client mailv1.EmailServiceClient }

func (c mailEmailClient) SendVerification(ctx context.Context, recipient, token string, expiresAt time.Time) error {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := c.client.SendVerificationEmail(callCtx, &mailv1.SendVerificationEmailRequest{
		Recipient: recipient, Token: token, ExpiresAtUnix: expiresAt.Unix(),
	})
	if err != nil {
		return err
	}
	if !response.GetQueued() {
		return errors.New("mail service did not queue verification email")
	}
	return nil
}

type authSessionClient struct{ client authv1.AuthServiceClient }

func (c authSessionClient) Create(ctx context.Context, userID string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := c.client.CreateSession(callCtx, &authv1.CreateSessionRequest{UserId: userID})
	if err != nil {
		return "", err
	}
	return response.GetToken(), nil
}
