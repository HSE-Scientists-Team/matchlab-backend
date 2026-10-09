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

	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRegistrationRepository(t *testing.T) {
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

	repo := NewPostgres(db)
	expiresAt := time.Now().Add(30 * time.Minute)
	token := func(n int) string { return fmt.Sprintf("%064x", n) }
	assertNoAccount := func(t *testing.T, email string) {
		t.Helper()
		if _, err := repo.FindByEmail(ctx, email); !errors.Is(err, ErrNotFound) {
			t.Fatalf("account must not exist for %s: %v", email, err)
		}
	}
	if err := repo.SaveRegistration(ctx, "student@hse.ru", "hash", token(1), expiresAt); !errors.Is(err, ErrOrganizationUnavailable) {
		t.Fatalf("empty domain rules: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.trusted_email_domain (domain, organization_name) VALUES ('hse.ru', 'НИУ ВШЭ'), ('*.hse.ru', 'НИУ ВШЭ')`); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		email   string
		allowed bool
	}{
		{"student@hse.ru", true}, {"student@campus.hse.ru", true}, {"student@lab.campus.hse.ru", true},
		{"student@bmstu.ru", false}, {"student@not-hse.ru", false}, {"student@hse.ru.example.org", false},
	} {
		err := repo.SaveRegistration(ctx, tc.email, "hash", token(i+2), expiresAt)
		if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrOrganizationUnavailable) {
			t.Fatalf("%s: allowed %t, error %v", tc.email, tc.allowed, err)
		}
		assertNoAccount(t, tc.email)
	}
	t.Run("owner replaces stranger's pending credentials", func(t *testing.T) {
		email := "owner@hse.ru"
		if err := repo.SaveRegistration(ctx, email, "stranger-hash", token(100), expiresAt); err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveRegistration(ctx, email, "owner-hash", token(101), expiresAt); err != nil {
			t.Fatal(err)
		}
		assertNoAccount(t, email)
		if err := repo.ConfirmEmail(ctx, token(100), time.Now()); !errors.Is(err, ErrVerificationNotFound) {
			t.Fatalf("old token: %v", err)
		}
		if err := repo.ConfirmEmail(ctx, token(101), expiresAt); !errors.Is(err, ErrVerificationExpired) {
			t.Fatalf("expired token: %v", err)
		}
		assertNoAccount(t, email)
		if err := repo.ConfirmEmail(ctx, token(101), time.Now()); err != nil {
			t.Fatal(err)
		}
		account, err := repo.FindByEmail(ctx, email)
		if err != nil || !account.Verified || account.PasswordHash != "owner-hash" {
			t.Fatalf("owner account: %+v, %v", account, err)
		}
		state, err := repo.GetEmailStatus(ctx, account.ID)
		if err != nil || state.Status != "verified" || state.Email != email || state.PendingEmail != "" {
			t.Fatalf("email status: %+v, %v", state, err)
		}
		var pending int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.registration_request WHERE email = $1`, email).Scan(&pending); err != nil || pending != 0 {
			t.Fatalf("request after confirmation: count %d, %v", pending, err)
		}
		if err := repo.ConfirmEmail(ctx, token(101), time.Now()); !errors.Is(err, ErrVerificationNotFound) {
			t.Fatalf("reused token: %v", err)
		}
		if err := repo.SaveRegistration(ctx, email, "replacement-hash", token(102), expiresAt); !errors.Is(err, ErrEmailTaken) {
			t.Fatalf("registered email: %v", err)
		}
		after, err := repo.FindByEmail(ctx, email)
		if err != nil || after.ID != account.ID || after.PasswordHash != "owner-hash" {
			t.Fatalf("existing account changed: %+v, %v", after, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE users.user_account SET status = 'blocked' WHERE id = $1`, account.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveRegistration(ctx, email, "replacement-hash", token(103), expiresAt); !errors.Is(err, ErrEmailTaken) {
			t.Fatalf("blocked account: %v", err)
		}
	})
	t.Run("disabled domain rolls back account creation", func(t *testing.T) {
		email := "disabled@campus.hse.ru"
		if err := repo.SaveRegistration(ctx, email, "hash", token(200), expiresAt); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE users.trusted_email_domain SET is_active = false WHERE domain = '*.hse.ru'`); err != nil {
			t.Fatal(err)
		}
		if err := repo.ConfirmEmail(ctx, token(200), time.Now()); !errors.Is(err, ErrOrganizationUnavailable) {
			t.Fatalf("disabled domain: %v", err)
		}
		assertNoAccount(t, email)
		if _, err := db.ExecContext(ctx, `UPDATE users.trusted_email_domain SET is_active = true WHERE domain = '*.hse.ru'`); err != nil {
			t.Fatal(err)
		}
		if err := repo.ConfirmEmail(ctx, token(200), time.Now()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed confirmation rolls back account and preserves request", func(t *testing.T) {
		email := "rollback@hse.ru"
		if err := repo.SaveRegistration(ctx, email, "owner-hash", token(250), expiresAt); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE users.user_email ADD CONSTRAINT test_confirmation_failure CHECK (email <> 'rollback@hse.ru')`); err != nil {
			t.Fatal(err)
		}
		if err := repo.ConfirmEmail(ctx, token(250), time.Now()); err == nil {
			t.Fatal("confirmation unexpectedly succeeded")
		}
		assertNoAccount(t, email)
		if _, err := db.ExecContext(ctx, `ALTER TABLE users.user_email DROP CONSTRAINT test_confirmation_failure`); err != nil {
			t.Fatal(err)
		}
		if err := repo.ConfirmEmail(ctx, token(250), time.Now()); err != nil {
			t.Fatalf("request lost after rollback: %v", err)
		}
	})
	t.Run("concurrent confirmations create exactly one account", func(t *testing.T) {
		email := "concurrent@hse.ru"
		if err := repo.SaveRegistration(ctx, email, "hash", token(300), expiresAt); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			go func() { results <- repo.ConfirmEmail(ctx, token(300), time.Now()) }()
		}
		successes := 0
		for i := 0; i < 8; i++ {
			err := <-results
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrVerificationNotFound) {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("confirmed %d times", successes)
		}
	})
	t.Run("replacement racing with confirmation preserves winning credentials", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			email := fmt.Sprintf("race%d@hse.ru", i)
			oldToken, newToken := token(400+i*2), token(401+i*2)
			if err := repo.SaveRegistration(ctx, email, "stranger-hash", oldToken, expiresAt); err != nil {
				t.Fatal(err)
			}
			saved, confirmed := make(chan error, 1), make(chan error, 1)
			go func() { saved <- repo.SaveRegistration(ctx, email, "owner-hash", newToken, expiresAt) }()
			go func() { confirmed <- repo.ConfirmEmail(ctx, oldToken, time.Now()) }()
			saveErr, confirmErr := <-saved, <-confirmed
			if saveErr == nil {
				if !errors.Is(confirmErr, ErrVerificationNotFound) {
					t.Fatalf("old token accepted after replacement: %v", confirmErr)
				}
				if err := repo.ConfirmEmail(ctx, newToken, time.Now()); err != nil {
					t.Fatal(err)
				}
				account, err := repo.FindByEmail(ctx, email)
				if err != nil || account.PasswordHash != "owner-hash" {
					t.Fatalf("wrong winning credentials: %+v, %v", account, err)
				}
			} else if !errors.Is(saveErr, ErrEmailTaken) || confirmErr != nil {
				t.Fatalf("race: save %v, confirm %v", saveErr, confirmErr)
			}
		}
	})
}
