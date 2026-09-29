package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

const versionTable = "users.goose_db_version"

// Up applies every pending migration embedded in this service binary.
func Up(ctx context.Context, db *sql.DB) (int64, error) {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS users`); err != nil {
		return 0, fmt.Errorf("ensure users schema: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, fmt.Errorf("configure goose postgres dialect: %w", err)
	}
	goose.SetTableName(versionTable)
	goose.SetBaseFS(Files)
	if _, err := goose.GetDBVersionContext(ctx, db); err != nil {
		return 0, fmt.Errorf("read current migration version: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return 0, fmt.Errorf("apply user migrations: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("read applied migration version: %w", err)
	}
	return version, nil
}
