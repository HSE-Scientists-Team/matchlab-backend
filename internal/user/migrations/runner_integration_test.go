//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	userconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/user/config"
	_ "github.com/jackc/pgx/v5/stdlib"
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
	cfg := userconfig.Postgres{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}
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
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'users' AND table_name IN ('user_account', 'trusted_email_domain', 'user_email', 'email_verification_request')`).Scan(&tableCount)
	if err != nil {
		t.Fatal(err)
	}
	if tableCount != 4 {
		t.Fatalf("created %d tables, want 4", tableCount)
	}
	userA := "00000000-0000-4000-8000-000000000001"
	userB := "00000000-0000-4000-8000-000000000002"
	for _, account := range []struct{ id, login string }{{userA, "user_a"}, {userB, "user_b"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (id, login, password_hash, status) VALUES ($1, $2, 'hash', 'active')`, account.id, account.login); err != nil {
			t.Fatal(err)
		}
	}
	for i, userID := range []string{userA, userB} {
		tokenHash := fmt.Sprintf("%064x", i+1)
		if _, err := db.ExecContext(ctx, `INSERT INTO users.email_verification_request (user_id, email, token_hash, expires_at) VALUES ($1, 'shared@example.org', $2, now() + interval '30 minutes')`, userID, tokenHash); err != nil {
			t.Fatalf("для одного адреса должны допускаться несколько ожидающих запросов: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'shared@example.org')`, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users.email_verification_request WHERE user_id = $1`, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'SHARED@example.org')`, userB); err == nil {
		t.Fatal("база допустила второго владельца одного адреса")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (login, password_hash, status) VALUES ('USER_A', 'hash', 'active')`); err == nil {
		t.Fatal("база допустила одинаковые логины в разном регистре")
	}
	var versionTableExists bool
	err = db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, fmt.Sprint("users.goose_db_version")).Scan(&versionTableExists)
	if err != nil {
		t.Fatal(err)
	}
	if !versionTableExists {
		t.Fatal("таблица версий Goose не создана в схеме users")
	}
}
