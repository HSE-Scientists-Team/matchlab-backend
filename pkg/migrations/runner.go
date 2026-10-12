package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

const versionTable = "users.goose_db_version"

// Общий ключ всех сервисов и релизов в одной базе PostgreSQL.
const advisoryLockID int64 = 836174251409

// Up применяет весь встроенный набор. Уже применённые миграции не повторяются.
func Up(ctx context.Context, db *sql.DB) (int64, error) { return up(ctx, db, Files) }

func up(ctx context.Context, db *sql.DB, files fs.FS) (int64, error) {
	hashes, err := migrationHashes(files)
	if err != nil {
		return 0, err
	}
	checked, err := withChecksums(files, hashes)
	if err != nil {
		return 0, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, checked, goose.WithTableName(versionTable), goose.WithDisableGlobalRegistry(true), goose.WithSessionLocker(&migrationLocker{hashes: hashes}))
	if err != nil {
		return 0, fmt.Errorf("настройка Goose: %w", err)
	}
	// Goose Up сначала вызывает HasPending без session locker. Инициализируем
	// схему и историю через GetDBVersion, который захватывает тот же lock,
	// чтобы первый запуск на пустой БД и параллельное создание истории были безопасны.
	if _, err := provider.GetDBVersion(ctx); err != nil {
		return 0, fmt.Errorf("подготовка истории миграций: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return 0, fmt.Errorf("применение миграций: %w", err)
	}
	return provider.GetDBVersion(ctx)
}

type migrationLocker struct{ hashes map[int64]string }

// SessionLock Блокировка удерживается тем же соединением, которое выполняет SQL-миграции.
// Потеря соединения одновременно освобождает lock и прерывает текущую DDL-транзакцию.
func (l *migrationLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		// При отмене ответ сервера может потеряться уже после получения lock.
		// Закрываем физическую сессию, чтобы исключить утечку блокировки.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return fmt.Errorf("блокировка миграций: %w", err)
	}
	if err := l.validate(ctx, conn); err != nil {
		return errors.Join(err, l.SessionUnlock(ctx, conn))
	}
	return nil
}
func (l *migrationLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock($1)`, advisoryLockID).Scan(&unlocked)
	if err == nil && !unlocked {
		err = fmt.Errorf("блокировка миграций уже потеряна")
	}
	if err != nil {
		// Не возвращаем сессию с возможным lock обратно в пул.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err
}
func (l *migrationLocker) validate(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS users`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS users.migration_checksum (version bigint PRIMARY KEY, sha256 char(64) NOT NULL)`); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT version,sha256 FROM users.migration_checksum`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int64
		var hash string
		if err := rows.Scan(&v, &hash); err != nil {
			rows.Close()
			return err
		}
		expected, known := l.hashes[v]
		if !known {
			rows.Close()
			return fmt.Errorf("схема БД содержит миграцию %d, отсутствующую в этом релизе", v)
		}
		if hash != expected {
			rows.Close()
			return fmt.Errorf("SQL миграции %d отличается от ранее применённого", v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Поддерживаем существующую историю Goose при переходе со старого запуска.
	// Её исходные SQL-отпечатки нельзя восстановить: первый запуск фиксирует baseline.
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, versionTable).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err = conn.QueryContext(ctx, `SELECT DISTINCT version_id FROM users.goose_db_version WHERE is_applied AND version_id > 0`)
	if err != nil {
		return err
	}
	var applied []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied = append(applied, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range applied {
		hash, known := l.hashes[v]
		if !known {
			return fmt.Errorf("версия схемы %d отсутствует в этом релизе", v)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO users.migration_checksum (version,sha256) VALUES ($1,$2) ON CONFLICT (version) DO NOTHING`, v, hash); err != nil {
			return err
		}
	}
	return nil
}

func migrationHashes(files fs.FS) (map[int64]string, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, err
	}
	hashes := make(map[int64]string, len(names))
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("неверное имя миграции %q", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("неверная версия миграции %q", name)
		}
		if _, exists := hashes[version]; exists {
			return nil, fmt.Errorf("повторный номер миграции %d", version)
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		hashes[version] = hex.EncodeToString(hash[:])
	}
	return hashes, nil
}
