//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mailconfig "github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/config"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/repository"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestMailMigrationsAndOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:17-alpine",
			Env:          map[string]string{"POSTGRES_DB": "matchlab", "POSTGRES_USER": "matchlab", "POSTGRES_PASSWORD": "integration-only"},
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
	cfg := mailconfig.Postgres{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}
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
	if err != nil || version != 1 {
		t.Fatalf("применение миграции Mail версии %d: %v", version, err)
	}
	version, err = Up(ctx, db)
	if err != nil || version != 1 {
		t.Fatalf("повторное применение миграции Mail версии %d: %v", version, err)
	}

	outbox := repository.NewPostgres(db)
	expires := time.Now().UTC().Add(30 * time.Minute)
	if err := outbox.Enqueue(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test@example.org", []byte("encrypted"), []byte("nonce"), expires); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test@example.org", []byte("encrypted"), []byte("nonce"), expires); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mail.email_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("идемпотентная постановка в очередь создала %d строк, ожидалась 1", count)
	}
	job, err := outbox.Claim(ctx, time.Minute)
	if err != nil || job == nil || job.Attempts != 1 {
		t.Fatalf("получение задания очереди %#v: %v", job, err)
	}
	if err := outbox.MarkSent(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	var tokenIsErased bool
	if err := db.QueryRowContext(ctx, `SELECT token_ciphertext IS NULL AND token_nonce IS NULL FROM mail.email_outbox WHERE id = $1`, job.ID).Scan(&tokenIsErased); err != nil || !tokenIsErased {
		t.Fatalf("токен отправленного письма не удалён: %v", err)
	}
}
