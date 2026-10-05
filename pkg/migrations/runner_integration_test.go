//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func integrationDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:17-alpine",
			Env: map[string]string{
				"POSTGRES_DB":       "matchlab",
				"POSTGRES_USER":     "matchlab",
				"POSTGRES_PASSWORD": "integration-only",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForListeningPort("5432/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	cfg := postgres.Config{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}
	db, err := sql.Open("pgx", cfg.URL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for attempt := 0; attempt < 30; attempt++ {
		if err := db.PingContext(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("PostgreSQL не стал готовым: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	return ctx, db
}

func TestMigrationsApplyAndStayCurrent(t *testing.T) {
	ctx, db := integrationDB(t)

	version, err := Up(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("версия миграции = %d, ожидалась 1", version)
	}
	version, err = Up(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("версия после повтора миграции = %d, ожидалась 1", version)
	}
	var tableCount int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'users' AND table_name IN ('user_account', 'trusted_email_domain', 'user_email', 'registration_request')`).Scan(&tableCount)
	if err != nil {
		t.Fatal(err)
	}
	if tableCount != 4 {
		t.Fatalf("created %d tables, want 4", tableCount)
	}
	userA := "00000000-0000-4000-8000-000000000001"
	userB := "00000000-0000-4000-8000-000000000002"

	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (id, email, password_hash) VALUES ($1, 'a@example.org', 'hash'), ($2, 'b@example.org', 'hash')`, userA, userB); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"a@example.org", "A@example.org"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (email, password_hash) VALUES ($1, 'hash')`, address); err == nil {
			t.Fatalf("duplicate email accepted: %s", address)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (password_hash) VALUES ('hash')`); err == nil {
		t.Fatal("missing email accepted")
	}

	// Заявка существует без аккаунта и содержит собственный хеш пароля.
	if _, err := db.ExecContext(ctx, `INSERT INTO users.registration_request (email, password_hash, token_hash, expires_at) VALUES ('pending@example.org', 'hash', $1, now() + interval '30 minutes')`, fmt.Sprintf("%064x", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.registration_request (email, password_hash, token_hash, expires_at) VALUES ('pending@example.org', 'other-hash', $1, now() + interval '30 minutes')`, fmt.Sprintf("%064x", 2)); err == nil {
		t.Fatal("duplicate registration request accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'a@example.org')`, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'a@example.org')`, userB); err == nil {
		t.Fatal("confirmation for another account email accepted")
	}
	var versionTableExists bool
	err = db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, fmt.Sprint("users.goose_db_version")).Scan(&versionTableExists)
	if err != nil {
		t.Fatal(err)
	}
	if !versionTableExists {
		t.Fatal("таблица версий Goose не создана в схеме users")
	}
	// Полный откат удаляет таблицы; повторное применение создаёт пустую схему.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, Files, goose.WithTableName(versionTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	var accountTableExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('users.user_account') IS NOT NULL`).Scan(&accountTableExists); err != nil || accountTableExists {
		t.Fatalf("account table after rollback: exists %t, error %v", accountTableExists, err)
	}
	if version, err := Up(ctx, db); err != nil || version != 1 {
		t.Fatalf("reapply migration: version %d, error %v", version, err)
	}
	var accountCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.user_account`).Scan(&accountCount); err != nil || accountCount != 0 {
		t.Fatalf("accounts after reapply: count %d, error %v", accountCount, err)
	}
}

func TestParallelReleasesApplyMigrationsExactlyOnce(t *testing.T) {
	ctx, db := integrationDB(t)
	db.SetMaxOpenConns(8)
	files := fstest.MapFS{
		"00001_once.sql": {Data: []byte("-- +goose Up\nCREATE TABLE users.execution (id integer PRIMARY KEY);\nINSERT INTO users.execution VALUES (1);\nSELECT pg_sleep(0.1);\n-- +goose Down\nDROP TABLE users.execution;\n")},
	}
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			version, err := up(ctx, db, files)
			if err == nil && version != 1 {
				err = fmt.Errorf("version %d", version)
			}
			results <- err
		}()
	}
	for i := 0; i < 12; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.execution`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d, error %v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.migration_checksum`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fingerprints %d, error %v", count, err)
	}
}

func TestOlderOrConflictingReleaseCannotChangeSchema(t *testing.T) {
	ctx, db := integrationDB(t)
	old := fstest.MapFS{"00001_test.sql": {Data: []byte("-- +goose Up\nCREATE TABLE users.release_guard(id integer);\n-- +goose Down\nDROP TABLE users.release_guard;\n")}}
	newer := fstest.MapFS{"00001_test.sql": old["00001_test.sql"], "00002_new.sql": {Data: []byte("-- +goose Up\nALTER TABLE users.release_guard ADD COLUMN value text;\n-- +goose Down\nALTER TABLE users.release_guard DROP COLUMN value;\n")}}
	if _, err := up(ctx, db, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := up(ctx, db, old); err == nil {
		t.Fatal("older release accepted newer schema")
	}
	conflict := fstest.MapFS{"00001_test.sql": {Data: append(append([]byte{}, old["00001_test.sql"].Data...), []byte("\n-- different SQL")...)}, "00002_new.sql": newer["00002_new.sql"]}
	if _, err := up(ctx, db, conflict); err == nil {
		t.Fatal("different SQL with same version accepted")
	}
	if version, err := up(ctx, db, newer); err != nil || version != 2 {
		t.Fatalf("failed startup leaked lock: version %d error %v", version, err)
	}
}

func TestMigrationLockCancellationAndRecovery(t *testing.T) {
	ctx, db := integrationDB(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = Up(waiting, db)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancellation: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockID); err != nil {
		t.Fatal(err)
	}
	if _, err := Up(ctx, db); err != nil {
		t.Fatalf("could not migrate after canceled wait: %v", err)
	}
}

func TestFailedSQLRollsBackFingerprintAndDDL(t *testing.T) {
	ctx, db := integrationDB(t)
	files := fstest.MapFS{"00001_test.sql": {Data: []byte("-- +goose Up\nCREATE TABLE users.failed(id integer);\nSELECT 1/0;\n-- +goose Down\nDROP TABLE users.failed;\n")}}
	if _, err := up(ctx, db, files); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('users.failed') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("partial DDL persisted: %t %v", exists, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.migration_checksum`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration fingerprint persisted: %d %v", count, err)
	}
	files["00001_test.sql"].Data = []byte("-- +goose Up\nCREATE TABLE users.failed(id integer);\n-- +goose Down\nDROP TABLE users.failed;\n")
	if _, err := up(ctx, db, files); err != nil {
		t.Fatalf("retry after transactional rollback failed: %v", err)
	}
}
