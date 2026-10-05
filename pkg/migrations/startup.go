package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Apply открывает временный пул для миграций и закрывает его перед запуском API.
// Секреты и строка подключения не включаются в возвращаемые ошибки.
func Apply(parent context.Context, cfg postgres.Config) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, cfg.MigrationTimeout)
	defer cancel()
	db, err := sql.Open("pgx", cfg.URL())
	if err != nil {
		return 0, fmt.Errorf("открытие PostgreSQL для миграций: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("подключение к PostgreSQL для миграций: %w", err)
	}
	return Up(ctx, db)
}
