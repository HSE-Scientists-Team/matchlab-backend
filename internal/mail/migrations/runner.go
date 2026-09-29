package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

const versionTable = "mail.goose_db_version"

func Up(ctx context.Context, db *sql.DB) (int64, error) {
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS mail`); err != nil {
		return 0, fmt.Errorf("создание схемы mail: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, fmt.Errorf("настройка диалекта PostgreSQL для Goose: %w", err)
	}
	goose.SetTableName(versionTable)
	goose.SetBaseFS(Files)
	if _, err := goose.GetDBVersionContext(ctx, db); err != nil {
		return 0, fmt.Errorf("чтение текущей версии миграции Mail: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return 0, fmt.Errorf("применение миграций Mail: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("чтение применённой версии миграции Mail: %w", err)
	}
	return version, nil
}
