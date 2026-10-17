//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
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
				return (postgres.Config{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}).URL()
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
	cfg := postgres.Config{Host: host, Port: port.Int(), Database: "matchlab", User: "matchlab", Password: "integration-only", SSLMode: "disable"}
	db, err := sql.Open("pgx", cfg.URL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, db
}

func applyMigrations(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if version, err := Up(ctx, db); err != nil || version != 7 {
		t.Fatalf("apply migrations: version=%d, want=7, error=%v", version, err)
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
	t.Run("default HSE domain rules", func(t *testing.T) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.trusted_email_domain WHERE domain IN ('hse.ru','edu.hse.ru') AND is_active`).Scan(&count); err != nil || count != 2 {
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
		var size, uploaded, expected sql.NullInt64
		var created, updated time.Time
		err := db.QueryRowContext(ctx, insertFile+` RETURNING id::text, status::text, is_public, size_bytes, extract(epoch FROM uploaded_at)::bigint, created_at, updated_at, expected_size_bytes`, owner, "defaults").Scan(&id, &state, &public, &size, &uploaded, &created, &updated, &expected)
		if err != nil || id == "" || state != "pending" || public || size.Valid || uploaded.Valid || expected.Valid || created.IsZero() || updated.IsZero() {
			t.Fatalf("defaults: id=%s state=%s public=%t size=%v uploaded=%v error=%v", id, state, public, size, uploaded, err)
		}
	})
	t.Run("expected size persists and pending index exists", func(t *testing.T) {
		var expected int64
		if err := db.QueryRowContext(ctx, `UPDATE media.file SET expected_size_bytes=42 WHERE object_key='constraint-target' RETURNING expected_size_bytes`).Scan(&expected); err != nil || expected != 42 {
			t.Fatalf("expected size: %d, %v", expected, err)
		}
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='media' AND indexname='media_file_pending_updated_idx'`).Scan(&definition); err != nil || !strings.Contains(definition, "(updated_at, id)") || !strings.Contains(definition, "pending") {
			t.Fatalf("pending index: %s, %v", definition, err)
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
			{"negative expected size", "expected_size_bytes=-1", "23514"},
			{"zero expected size", "expected_size_bytes=0", "23514"},
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
		if version, err := migrationVersion(ctx, db); err != nil || version != want {
			t.Fatalf("version: want=%d got=%d error=%v", want, version, err)
		}
	}
	down := func(t *testing.T) {
		t.Helper()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, Files, goose.WithTableName(versionTable), goose.WithDisableGlobalRegistry(true))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Down(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("repeat preserves data and history", func(t *testing.T) {
		applyMigrations(t, ctx, db)
		requireCount(t, `SELECT count(*) FROM users.user_account WHERE email='preserve@example.org'`, 1)
		requireCount(t, `SELECT count(*) FROM media.file WHERE object_key='preserve' AND is_public`, 1)
		requireCount(t, `SELECT count(*) FROM users.goose_db_version WHERE version_id IN (1,2,3,4,5,6,7) AND is_applied`, 7)
	})
	t.Run("support rollback preserves users and media", func(t *testing.T) {
		down(t) // administration
		down(t) // projects
		down(t) // profiles
		if _, err := db.ExecContext(ctx, `INSERT INTO support.support_ticket (author_user_id,subject,description,target_user_id) VALUES ('00000000-0000-4000-8000-000000000001','complaint','description','00000000-0000-4000-8000-000000000002')`); err != nil {
			t.Fatal(err)
		}
		down(t)
		requireVersion(t, 3)
		requireCount(t, `SELECT count(*) FROM pg_namespace WHERE nspname='support'`, 0)
		requireCount(t, `SELECT count(*) FROM users.user_account WHERE email='preserve@example.org'`, 1)
		requireCount(t, `SELECT count(*) FROM media.file WHERE object_key='preserve'`, 1)
		applyMigrations(t, ctx, db)
		requireCount(t, `SELECT count(*) FROM support.support_ticket`, 0)
	})
	t.Run("multipart rollback preserves files", func(t *testing.T) {
		down(t) // administration
		down(t) // projects
		down(t) // profiles
		down(t)
		down(t)
		requireVersion(t, 2)
		requireCount(t, `SELECT count(*) FROM pg_tables WHERE schemaname='media' AND tablename='multipart_upload'`, 0)
		requireCount(t, `SELECT count(*) FROM media.file WHERE object_key='preserve'`, 1)
		requireCount(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema='media' AND table_name='file' AND column_name='expected_size_bytes'`, 1)
		requireCount(t, `SELECT count(*) FROM pg_indexes WHERE schemaname='media' AND indexname='media_file_pending_updated_idx'`, 1)
		applyMigrations(t, ctx, db)
	})
	t.Run("media rollback preserves users", func(t *testing.T) {
		down(t) // administration
		down(t) // projects
		down(t) // profiles
		down(t)
		down(t)
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
		down(t) // administration
		down(t) // projects
		down(t) // profiles
		down(t)
		down(t)
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

func TestParallelReleasesApplyMigrationsExactlyOnce(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
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
	ctx, db := newMigrationDatabase(t)
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
	ctx, db := newMigrationDatabase(t)
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
	ctx, db := newMigrationDatabase(t)
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

func migrationVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var version int64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id),0) FROM users.goose_db_version WHERE is_applied`).Scan(&version)
	return version, err
}

func TestUpgradeAccountsSchemaToMediaPreservesUsers(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	sql, err := Files.ReadFile("00001_create_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	initial := fstest.MapFS{"00001_create_accounts.sql": {Data: sql}}
	if version, err := up(ctx, db, initial); err != nil || version != 1 {
		t.Fatalf("initial: %d %v", version, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users.user_account (email,password_hash) VALUES ('upgrade@example.org','hash')`); err != nil {
		t.Fatal(err)
	}
	if version, err := Up(ctx, db); err != nil || version != 7 {
		t.Fatalf("upgrade: %d %v", version, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users.user_account WHERE email='upgrade@example.org'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("account lost: %d %v", count, err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('media.file') IS NOT NULL AND to_regclass('media.multipart_upload') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("media tables: %t %v", exists, err)
	}
}

func TestProjectRollbackRevokesContactGrantsButKeepsReviewHistory(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	applyMigrations(t, ctx, db)
	_, err := db.ExecContext(ctx, `
 INSERT INTO users.user_account(id,email,password_hash) VALUES
 ('00000000-0000-4000-8000-000000000001','owner@example.org','hash'),
 ('00000000-0000-4000-8000-000000000002','member@example.org','hash');
 INSERT INTO users.project_context(project_id,title,initiator_user_id,state,source_revision,completed_at) VALUES
 ('00000000-0000-4000-8000-000000000101','Active','00000000-0000-4000-8000-000000000001','active',1,NULL),
 ('00000000-0000-4000-8000-000000000102','Cancelled','00000000-0000-4000-8000-000000000001','cancelled',2,NULL),
 ('00000000-0000-4000-8000-000000000103','Complete','00000000-0000-4000-8000-000000000001','completed',3,now()),
 ('00000000-0000-4000-8000-000000000104','Legacy','00000000-0000-4000-8000-000000000001','active',0,NULL);
 INSERT INTO users.project_context_member(project_id,user_id) SELECT project_id,'00000000-0000-4000-8000-000000000001'::uuid FROM users.project_context;
 INSERT INTO users.project_context_member(project_id,user_id) VALUES ('00000000-0000-4000-8000-000000000103','00000000-0000-4000-8000-000000000002');
 INSERT INTO users.profile_review(project_id,author_user_id,target_user_id,title,text,rating) VALUES ('00000000-0000-4000-8000-000000000103','00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002','Review','Text',5);`)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, Files, goose.WithTableName(versionTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{7, 6} {
		if _, err = provider.Down(ctx); err != nil {
			t.Fatalf("rollback %d: %v", version, err)
		}
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM users.project_context`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unsafe retained team grants: %d %v", count, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM users.profile_review`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("review history lost: %d %v", count, err)
	}
}
