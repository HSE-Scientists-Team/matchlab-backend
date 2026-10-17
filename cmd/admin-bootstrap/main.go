// admin-bootstrap is an offline operator command; there is no public registration endpoint.
package main

import (
	"context"
	"database/sql"
	"flag"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/admin"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"log/slog"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("admin bootstrap failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("config", "/etc/app/config.yaml", "configuration YAML")
	email := flag.String("email", "", "first administrator email")
	flag.Parse()
	cfg, err := grpcapp.Load(*file, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err = migrations.Apply(ctx, cfg.DB); err != nil {
		return err
	}
	db, err := sql.Open("pgx", cfg.DB.URL())
	if err != nil {
		return err
	}
	defer db.Close()
	id, err := admin.Bootstrap(ctx, db, *email, os.Getenv("ADMIN_BOOTSTRAP_PASSWORD"))
	if err != nil {
		return err
	}
	slog.Info("first administrator created", "admin_id", id)
	return nil
}
