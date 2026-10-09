//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/migrations"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:17-alpine",
			Env: map[string]string{
				"POSTGRES_DB": "matchlab", "POSTGRES_USER": "matchlab", "POSTGRES_PASSWORD": "integration-only",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port nat.Port) string {
				return fmt.Sprintf("postgres://matchlab:integration-only@%s/matchlab?sslmode=disable", net.JoinHostPort(host, port.Port()))
			}).WithStartupTimeout(90 * time.Second),
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
	db, err := sql.Open("pgx", fmt.Sprintf("postgres://matchlab:integration-only@%s/matchlab?sslmode=disable", net.JoinHostPort(host, port.Port())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgres(db)
	newInput := func() domain.NewFile {
		id := uuid.NewString()
		return domain.NewFile{
			ID: id, OwnerUserID: uuid.NewString(), BucketName: "matchlab-media",
			ObjectKey: "files/" + id, OriginalName: "report.pdf", ContentType: "application/pdf",
		}
	}
	createPending := func(t *testing.T) domain.NewFile {
		t.Helper()
		input := newInput()
		if _, err := repo.CreatePending(ctx, input); err != nil {
			t.Fatal(err)
		}
		return input
	}
	etag := "object-etag"
	checksum := fmt.Sprintf("%064x", 1)
	object := domain.ObjectInfo{SizeBytes: 42, ContentType: "application/pdf", ETag: &etag, ChecksumSHA256: &checksum}

	t.Run("private by database default", func(t *testing.T) {
		input := newInput()
		var public bool
		err := db.QueryRowContext(ctx, `INSERT INTO media.file
			(id, owner_user_id, bucket_name, object_key, original_name, content_type)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING is_public`, input.ID, input.OwnerUserID,
			input.BucketName, input.ObjectKey, input.OriginalName, input.ContentType).Scan(&public)
		if err != nil || public {
			t.Fatalf("database default: public=%t, error=%v", public, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE media.file SET is_public = NULL WHERE id = $1`, input.ID); err == nil {
			t.Fatal("nullable visibility accepted")
		}
	})

	for _, public := range []bool{false, true} {
		name := "private access"
		if public {
			name = "public access"
		}
		t.Run(name, func(t *testing.T) {
			input := newInput()
			input.IsPublic = public
			created, err := repo.CreatePending(ctx, input)
			if err != nil || created.IsPublic != public {
				t.Fatalf("visibility not saved: %+v, %v", created, err)
			}
			stranger := uuid.NewString()
			for _, reader := range []string{input.OwnerUserID, stranger, ""} {
				file, err := repo.FindReadable(ctx, input.ID, reader)
				if public || reader == input.OwnerUserID {
					if err != nil || file.IsPublic != public || file.ID != input.ID {
						t.Fatalf("read as %q: %+v, %v", reader, file, err)
					}
				} else if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("private read as %q: %v", reader, err)
				}
			}
			if _, err := repo.FindOwned(ctx, input.ID, stranger); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("ownership bypassed: %v", err)
			}
			if _, err := repo.MarkReady(ctx, input.ID, stranger, object); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("stranger confirmation: %v", err)
			}
			ready, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, object)
			if err != nil || ready.IsPublic != public {
				t.Fatalf("confirmation changed visibility: %+v, %v", ready, err)
			}
			if _, err := repo.MarkReady(ctx, input.ID, stranger, object); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("stranger repeat: %v", err)
			}
		})
	}

	t.Run("create pending file", func(t *testing.T) {
		input := newInput()
		file, err := repo.CreatePending(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if file.ID != input.ID || file.OwnerUserID != input.OwnerUserID || file.BucketName != input.BucketName || file.ObjectKey != input.ObjectKey || file.OriginalName != input.OriginalName || file.ContentType != input.ContentType {
			t.Fatalf("unexpected file metadata: %+v", file)
		}
		if file.Status != domain.FileStatusPending || file.SizeBytes != nil || file.ETag != nil || file.ChecksumSHA256 != nil || file.UploadedAt != nil || file.DeletedAt != nil || file.CreatedAt.IsZero() || file.UpdatedAt.IsZero() {
			t.Fatalf("unexpected pending file: %+v", file)
		}
	})

	t.Run("reject duplicate ID", func(t *testing.T) {
		input := createPending(t)
		duplicate := newInput()
		duplicate.ID = input.ID
		if _, err := repo.CreatePending(ctx, duplicate); !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("duplicate ID: %v", err)
		}
	})

	t.Run("reject duplicate storage location", func(t *testing.T) {
		input := createPending(t)
		duplicate := newInput()
		duplicate.BucketName, duplicate.ObjectKey = input.BucketName, input.ObjectKey
		if _, err := repo.CreatePending(ctx, duplicate); !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("duplicate storage location: %v", err)
		}
	})

	t.Run("read file as owner", func(t *testing.T) {
		input := createPending(t)
		file, err := repo.FindOwned(ctx, input.ID, input.OwnerUserID)
		if err != nil || file.ID != input.ID || file.OwnerUserID != input.OwnerUserID || file.Status != domain.FileStatusPending {
			t.Fatalf("owner read: %+v, %v", file, err)
		}
	})

	t.Run("deny access to another owner", func(t *testing.T) {
		input := createPending(t)
		stranger := uuid.NewString()
		if _, err := repo.FindOwned(ctx, input.ID, stranger); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("stranger read: %v", err)
		}
		if _, err := repo.MarkReady(ctx, input.ID, stranger, object); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("stranger confirmation: %v", err)
		}
		file, err := repo.FindOwned(ctx, input.ID, input.OwnerUserID)
		if err != nil || file.Status != domain.FileStatusPending {
			t.Fatalf("stranger changed file: %+v, %v", file, err)
		}
	})

	t.Run("return not found for missing file", func(t *testing.T) {
		if _, err := repo.FindOwned(ctx, uuid.NewString(), uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("missing file: %v", err)
		}
	})

	t.Run("keep pending after invalid metadata", func(t *testing.T) {
		input := createPending(t)
		if _, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, domain.ObjectInfo{SizeBytes: -1, ContentType: input.ContentType}); err == nil {
			t.Fatal("negative size accepted")
		}
		file, err := repo.FindOwned(ctx, input.ID, input.OwnerUserID)
		if err != nil || file.Status != domain.FileStatusPending || file.SizeBytes != nil || file.UploadedAt != nil {
			t.Fatalf("failed confirmation changed file: %+v, %v", file, err)
		}
	})

	t.Run("confirm upload idempotently", func(t *testing.T) {
		input := createPending(t)
		ready, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, object)
		if err != nil {
			t.Fatal(err)
		}
		if ready.Status != domain.FileStatusReady || ready.SizeBytes == nil || *ready.SizeBytes != 42 || ready.UploadedAt == nil || ready.ETag == nil || *ready.ETag != etag || ready.ChecksumSHA256 == nil || *ready.ChecksumSHA256 != checksum {
			t.Fatalf("unexpected ready file: %+v", ready)
		}
		stored, err := repo.FindOwned(ctx, input.ID, input.OwnerUserID)
		if err != nil || stored.Status != domain.FileStatusReady || stored.ETag == nil || *stored.ETag != etag || stored.ChecksumSHA256 == nil || *stored.ChecksumSHA256 != checksum {
			t.Fatalf("stored metadata: %+v, %v", stored, err)
		}
		repeated, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, domain.ObjectInfo{SizeBytes: 999, ContentType: "text/plain"})
		if err != nil || repeated.SizeBytes == nil || *repeated.SizeBytes != 42 || repeated.ContentType != input.ContentType || repeated.ETag == nil || *repeated.ETag != etag || repeated.ChecksumSHA256 == nil || *repeated.ChecksumSHA256 != checksum || !repeated.UpdatedAt.Equal(ready.UpdatedAt) || repeated.UploadedAt == nil || !repeated.UploadedAt.Equal(*ready.UploadedAt) {
			t.Fatalf("repeat changed metadata: %+v, %v", repeated, err)
		}
	})

	t.Run("confirm upload concurrently", func(t *testing.T) {
		input := createPending(t)
		start := make(chan struct{})
		results := make(chan domain.File, 4)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ready, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, object)
				if err != nil {
					t.Error(err)
					return
				}
				results <- ready
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		stored, err := repo.FindOwned(ctx, input.ID, input.OwnerUserID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for ready := range results {
			count++
			if ready.Status != domain.FileStatusReady || ready.SizeBytes == nil || *ready.SizeBytes != 42 || ready.UploadedAt == nil || stored.UploadedAt == nil || !ready.UploadedAt.Equal(*stored.UploadedAt) || !ready.UpdatedAt.Equal(stored.UpdatedAt) {
				t.Errorf("concurrent confirmation returned inconsistent file: %+v", ready)
			}
		}
		if count != 4 {
			t.Errorf("successful confirmations = %d, want 4", count)
		}
	})

	t.Run("reject confirmation in invalid state", func(t *testing.T) {
		for _, state := range []domain.FileStatus{domain.FileStatusDeleting, domain.FileStatusDeleted, domain.FileStatusFailed} {
			t.Run(string(state), func(t *testing.T) {
				input := createPending(t)
				if _, err := db.ExecContext(ctx, `UPDATE media.file SET status = $2 WHERE id = $1`, input.ID, state); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.MarkReady(ctx, input.ID, input.OwnerUserID, object); !errors.Is(err, domain.ErrInvalidStatus) {
					t.Fatalf("confirmation in %s: %v", state, err)
				}
			})
		}
	})
}
