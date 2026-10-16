// Package grpcapp provides the common lifecycle for new internal gRPC services.
package grpcapp

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/healthcheck"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	GRPC  string            `mapstructure:"grpc"`
	Peers map[string]string `mapstructure:"peers"`
	DB    postgres.Config   `mapstructure:"postgres"`
}

func Load(path string, peers []string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var c Config
	if err := v.ReadInConfig(); err != nil {
		return c, err
	}
	if err := v.Unmarshal(&c); err != nil {
		return c, err
	}
	valid := func(a string) bool {
		h, p, e := net.SplitHostPort(a)
		n, _ := strconv.Atoi(p)
		return e == nil && h != "" && n > 0 && n <= 65535
	}
	if !valid(c.GRPC) {
		return c, fmt.Errorf("invalid grpc address")
	}
	for _, name := range peers {
		if !valid(c.Peers[name]) {
			return c, fmt.Errorf("invalid %s peer address", name)
		}
	}
	var err error
	c.DB, err = postgres.Load(v)
	return c, err
}

type Setup func(context.Context, *sql.DB, Config, *grpc.Server, *slog.Logger) (func(), error)

func Run(name, healthName string, peers []string, setup Setup) error {
	path := flag.String("config", "/etc/app/config.yaml", "configuration YAML")
	flag.Parse()
	cfg, err := Load(*path, peers)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	version, err := migrations.Apply(ctx, cfg.DB)
	if err != nil {
		return err
	}
	logger.Info("migrations applied", "version", version, "service", name)
	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return h(call, req)
	}))
	health := healthcheck.RegisterGRPC(server)
	cleanup, err := setup(ctx, db, cfg, server, logger)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	health.SetServingStatus(healthName, grpc_health_v1.HealthCheckResponse_SERVING)
	listener, err := net.Listen("tcp", cfg.GRPC)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
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
