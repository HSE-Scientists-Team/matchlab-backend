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
	redisrepo "github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository/redis"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/usecase"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
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

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Address(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer func(client *redis.Client) {
		err := client.Close()
		if err != nil {
			logger.Error(fmt.Sprintf("%v", err))
		}
	}(client)
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err = client.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		return err
	}

	sessions := usecase.NewSessionService(redisrepo.NewSessionStore(redisrepo.NewClientAdapter(client)))
	router := mux.NewRouter()
	healthcheck.RegisterHTTP(router)
	delivery.Register(router, sessions, logger)
	server := &http.Server{
		Handler:           delivery.Middleware(logger, router),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.HTTP.Address())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("gateway started", "address", listener.Addr().String())

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("gateway shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
