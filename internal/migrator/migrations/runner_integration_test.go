//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestMigrationsApplyAndStayCurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
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
	cfg := config.Postgres{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}
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
	if err := goose.DownContext(ctx, db, "."); err != nil {
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
