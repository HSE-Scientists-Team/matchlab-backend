package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/config"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("миграции PostgreSQL не применены", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	path := flag.String("config", "/etc/app/config.yaml", "путь к конфигурации YAML")
	flag.Parse()
	p, err := config.Load(*path)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", p.URL())
	if err != nil {
		return fmt.Errorf("открытие соединения с PostgreSQL: %w", err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("подключение к PostgreSQL: %w", err)
	}
	version, err := migrations.Up(ctx, db)
	if err != nil {
		return err
	}
	logger.Info("миграции PostgreSQL применены", "version", version)
	return nil
}
