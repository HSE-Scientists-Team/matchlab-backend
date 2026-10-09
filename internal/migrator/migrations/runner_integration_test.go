//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/config"
	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Each test gets its own database. Goose settings are global: do not use t.Parallel.
func newMigrationDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:17-alpine",
			Env:          map[string]string{"POSTGRES_DB": "matchlab", "POSTGRES_USER": "matchlab", "POSTGRES_PASSWORD": "integration-only"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port nat.Port) string {
				return (config.Postgres{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}).URL()
			}).WithStartupTimeout(90 * time.Second),
		}, Started: true,
	})
	if container != nil {
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
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
	return ctx, db
}

func applyMigrations(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if version, err := Up(ctx, db); err != nil || version != 2 {
		t.Fatalf("apply migrations: version=%d, want=2, error=%v", version, err)
	}
}

func requireSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("SQLSTATE: want=%s, error=%v", code, err)
	}
}

func TestUsersSchema(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	applyMigrations(t, ctx, db)
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'users' AND table_name IN ('user_account', 'trusted_email_domain', 'user_email', 'registration_request')`).Scan(&tableCount); err != nil || tableCount != 4 {
		t.Fatalf("users tables: count=%d, error=%v", tableCount, err)
	}
	userA := "00000000-0000-4000-8000-000000000001"
	userB := "00000000-0000-4000-8000-000000000002"
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (id, email, password_hash) VALUES ($1, 'a@example.org', 'hash'), ($2, 'b@example.org', 'hash')`, userA, userB); err != nil {
		t.Fatal(err)
	}
	t.Run("account email constraints", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `INSERT INTO users.user_account (email, password_hash) VALUES ('a@example.org', 'hash')`)
		requireSQLState(t, err, "23505")
		_, err = db.ExecContext(ctx, `INSERT INTO users.user_account (email, password_hash) VALUES ('A@example.org', 'hash')`)
		if err == nil {
			t.Fatal("uppercase duplicate email accepted")
		}
		_, err = db.ExecContext(ctx, `INSERT INTO users.user_account (password_hash) VALUES ('hash')`)
		requireSQLState(t, err, "23502")
	})
	t.Run("registration without account", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `INSERT INTO users.registration_request (email, password_hash, token_hash, expires_at) VALUES ('pending@example.org', 'hash', $1, now() + interval '30 minutes')`, fmt.Sprintf("%064x", 1)); err != nil {
			t.Fatal(err)
		}
		_, err := db.ExecContext(ctx, `INSERT INTO users.registration_request (email, password_hash, token_hash, expires_at) VALUES ('pending@example.org', 'other-hash', $1, now() + interval '30 minutes')`, fmt.Sprintf("%064x", 2))
		requireSQLState(t, err, "23505")
	})
	t.Run("confirmed email belongs to account", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'a@example.org')`, userA); err != nil {
			t.Fatal(err)
		}
		_, err := db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'a@example.org')`, userB)
		requireSQLState(t, err, "23505")
		_, err = db.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email) VALUES ($1, 'unrelated@example.org')`, userB)
		requireSQLState(t, err, "23503")
	})
	t.Run("no automatic domain seed", func(t *testing.T) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.trusted_email_domain`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("domain seed: count=%d, error=%v", count, err)
		}
	})
}

func TestMediaSchema(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	applyMigrations(t, ctx, db)
	const owner = "00000000-0000-4000-8000-000000000001"
	const insertFile = `INSERT INTO media.file (owner_user_id, bucket_name, object_key, original_name, content_type) VALUES ($1, 'matchlab-media', $2, 'avatar.png', 'image/png')`
	if _, err := db.ExecContext(ctx, insertFile, owner, "constraint-target"); err != nil {
		t.Fatal(err)
	}
	t.Run("private pending defaults", func(t *testing.T) {
		var id, state string
		var public bool
		var size, uploaded sql.NullInt64
		var created, updated time.Time
		err := db.QueryRowContext(ctx, insertFile+` RETURNING id::text, status::text, is_public, size_bytes, extract(epoch FROM uploaded_at)::bigint, created_at, updated_at`, owner, "defaults").Scan(&id, &state, &public, &size, &uploaded, &created, &updated)
		if err != nil || id == "" || state != "pending" || public || size.Valid || uploaded.Valid || created.IsZero() || updated.IsZero() {
			t.Fatalf("defaults: id=%s state=%s public=%t size=%v uploaded=%v error=%v", id, state, public, size, uploaded, err)
		}
	})
	t.Run("public flag persists", func(t *testing.T) {
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO media.file (owner_user_id, bucket_name, object_key, original_name, content_type, is_public) VALUES ($1, 'matchlab-media', 'public', 'avatar.png', 'image/png', true) RETURNING id::text`, owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		var public bool
		if err := db.QueryRowContext(ctx, `SELECT is_public FROM media.file WHERE id=$1`, id).Scan(&public); err != nil || !public {
			t.Fatalf("stored public flag: %t, %v", public, err)
		}
		_, err := db.ExecContext(ctx, `UPDATE media.file SET is_public=NULL WHERE id=$1`, id)
		requireSQLState(t, err, "23502")
	})
	t.Run("storage location is unique", func(t *testing.T) {
		_, err := db.ExecContext(ctx, insertFile, owner, "constraint-target")
		requireSQLState(t, err, "23505")
	})
	t.Run("object metadata constraints", func(t *testing.T) {
		for _, tc := range []struct{ name, expression, code string }{
			{"negative size", "size_bytes=-1", "23514"},
			{"empty key", "object_key=''", "23514"},
			{"long key", "object_key=repeat('x',1025)", "23514"},
			{"invalid checksum", "checksum_sha256='invalid'", "23514"},
			{"invalid state", "status='unknown'", "22P02"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := db.ExecContext(ctx, `UPDATE media.file SET `+tc.expression+` WHERE object_key='constraint-target'`)
				requireSQLState(t, err, tc.code)
			})
		}
	})
}

func TestMigrationsApplyAndStayCurrent(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	applyMigrations(t, ctx, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (email,password_hash) VALUES ('preserve@example.org','hash'); INSERT INTO media.file (owner_user_id,bucket_name,object_key,original_name,content_type,is_public) VALUES ('00000000-0000-4000-8000-000000000001','matchlab-media','preserve','avatar.png','image/png',true)`); err != nil {
		t.Fatal(err)
	}
	requireCount := func(t *testing.T, query string, want int) {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != want {
			t.Fatalf("count: want=%d got=%d query=%s error=%v", want, count, query, err)
		}
	}
	requireVersion := func(t *testing.T, want int64) {
		t.Helper()
		if version, err := goose.GetDBVersionContext(ctx, db); err != nil || version != want {
			t.Fatalf("version: want=%d got=%d error=%v", want, version, err)
		}
	}
	down := func(t *testing.T) {
		t.Helper()
		if err := goose.DownContext(ctx, db, "."); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("repeat preserves data and history", func(t *testing.T) {
		applyMigrations(t, ctx, db)
		requireCount(t, `SELECT count(*) FROM users.user_account WHERE email='preserve@example.org'`, 1)
		requireCount(t, `SELECT count(*) FROM media.file WHERE object_key='preserve' AND is_public`, 1)
		requireCount(t, `SELECT count(*) FROM users.goose_db_version WHERE version_id IN (1,2) AND is_applied`, 2)
	})
	t.Run("media rollback preserves users", func(t *testing.T) {
		down(t)
		requireVersion(t, 1)
		requireCount(t, `SELECT count(*) FROM pg_tables WHERE schemaname='media' AND tablename='file'`, 0)
		requireCount(t, `SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='media' AND t.typname='file_status'`, 0)
		requireCount(t, `SELECT count(*) FROM users.user_account WHERE email='preserve@example.org'`, 1)
		applyMigrations(t, ctx, db)
		requireCount(t, `SELECT count(*) FROM media.file`, 0)
		requireCount(t, `SELECT count(*) FROM users.user_account WHERE email='preserve@example.org'`, 1)
	})
	t.Run("full rollback and reapply", func(t *testing.T) {
		down(t)
		down(t)
		requireVersion(t, 0)
		requireCount(t, `SELECT count(*) FROM pg_tables WHERE schemaname='users' AND tablename IN ('user_account','user_email','registration_request','trusted_email_domain')`, 0)
		requireCount(t, `SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='users' AND t.typname IN ('user_account_status','system_role_code')`, 0)
		applyMigrations(t, ctx, db)
		requireCount(t, `SELECT count(*) FROM users.user_account`, 0)
		requireCount(t, `SELECT count(*) FROM media.file`, 0)
		var public bool
		if err := db.QueryRowContext(ctx, `INSERT INTO media.file (owner_user_id,bucket_name,object_key,original_name,content_type) VALUES ('00000000-0000-4000-8000-000000000001','matchlab-media','reapplied','avatar.png','image/png') RETURNING is_public`).Scan(&public); err != nil || public {
			t.Fatalf("visibility after reapply: public=%t error=%v", public, err)
		}
	})
}
