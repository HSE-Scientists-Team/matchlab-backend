package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

const versionTable = "users.goose_db_version"

// Up применяет все ожидающие миграции, встроенные в исполняемый файл сервиса.
func Up(ctx context.Context, db *sql.DB) (int64, error) {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS users`); err != nil {
		return 0, fmt.Errorf("создание схемы users: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, fmt.Errorf("настройка диалекта PostgreSQL для Goose: %w", err)
	}
	goose.SetTableName(versionTable)
	goose.SetBaseFS(Files)
	if _, err := goose.GetDBVersionContext(ctx, db); err != nil {
		return 0, fmt.Errorf("чтение текущей версии миграции: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return 0, fmt.Errorf("применение миграций User: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("чтение применённой версии миграции: %w", err)
	}
	return version, nil
}
