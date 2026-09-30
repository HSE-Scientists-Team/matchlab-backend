//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestTrustedEmailDomains(t *testing.T) {
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
	dsn := fmt.Sprintf("postgres://matchlab:integration-only@%s/matchlab?sslmode=disable", net.JoinHostPort(host, port.Port()))
	db, err := sql.Open("pgx", dsn)
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
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	const userID = "00000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (id, login, password_hash) VALUES ($1, 'student', 'hash')`, userID); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgres(db)
	expiresAt := time.Now().Add(30 * time.Minute)
	if err := repo.SaveEmailVerification(ctx, userID, "student@hse.ru", fmt.Sprintf("%064x", 1), expiresAt); !errors.Is(err, ErrOrganizationUnavailable) {
		t.Fatalf("пустой список должен запрещать подтверждение: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.trusted_email_domain (domain, organization_name) VALUES ('hse.ru', 'НИУ ВШЭ'), ('*.hse.ru', 'НИУ ВШЭ')`); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		email   string
		allowed bool
	}{
		{"student@hse.ru", true},
		{"student@campus.hse.ru", true},
		{"student@lab.campus.hse.ru", true},
		{"student@bmstu.ru", false},
		{"student@not-hse.ru", false},
		{"student@hse.ru.example.org", false},
	} {
		err := repo.SaveEmailVerification(ctx, userID, tc.email, fmt.Sprintf("%064x", i+2), expiresAt)
		if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrOrganizationUnavailable) {
			t.Errorf("адрес %q: ошибка %v, разрешён %t", tc.email, err, tc.allowed)
		}
	}
	var pendingEmail string
	if err := db.QueryRowContext(ctx, `SELECT email FROM users.email_verification_request WHERE user_id = $1`, userID).Scan(&pendingEmail); err != nil {
		t.Fatal(err)
	}
	if pendingEmail != "student@lab.campus.hse.ru" {
		t.Fatalf("запрещённый адрес изменил ожидающий запрос: %q", pendingEmail)
	}
	const tokenHash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := repo.SaveEmailVerification(ctx, userID, "student@campus.hse.ru", tokenHash, expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users.trusted_email_domain SET is_active = false WHERE domain = '*.hse.ru'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmEmail(ctx, tokenHash, time.Now()); !errors.Is(err, ErrOrganizationUnavailable) {
		t.Fatalf("отключённый поддомен не должен подтверждаться по старому токену: %v", err)
	}
	var confirmed int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.user_email WHERE user_id = $1`, userID).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != 0 {
		t.Fatal("адрес был подтверждён после отключения домена")
	}
}
