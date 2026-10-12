//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresComplaints(t *testing.T) {
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
	author := uuid.NewString()
	target := uuid.NewString()
	fileA, fileB := uuid.NewString(), uuid.NewString()
	input := domain.NewComplaint{UserID: author, Subject: "Жалоба", Description: "Описание", TargetUserID: &target, FileIDs: []string{fileA, fileB}}
	complaint, err := repo.Create(ctx, input)
	if err != nil || complaint.Status != "open" || complaint.ID == "" || complaint.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", complaint, err)
	}
	var attachments int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM support.support_ticket_attachment WHERE support_ticket_id=$1`, complaint.ID).Scan(&attachments); err != nil || attachments != 2 {
		t.Fatalf("attachments: %d %v", attachments, err)
	}
	var assigned, resolved bool
	if err := db.QueryRowContext(ctx, `SELECT assigned_user_id IS NULL,resolved_at IS NULL FROM support.support_ticket WHERE id=$1`, complaint.ID).Scan(&assigned, &resolved); err != nil || !assigned || !resolved {
		t.Fatal("defaults", err)
	}
	input.TargetUserID = nil
	input.TargetProjectID = &target
	input.FileIDs = nil
	if _, err := repo.Create(ctx, input); err != nil {
		t.Fatal("project without attachments", err)
	}
	var before, after int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM support.support_ticket`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	input.FileIDs = []string{fileA, "bad-uuid"}
	if _, err := repo.Create(ctx, input); err == nil {
		t.Fatal("bad attachment accepted")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM support.support_ticket`).Scan(&after); err != nil || after != before {
		t.Fatalf("partial complaint committed: %d %d %v", before, after, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM support.support_ticket_attachment`).Scan(&attachments); err != nil || attachments != 2 {
		t.Fatal("partial attachments committed", err)
	}
	input.FileIDs = []string{fileA, fileA}
	if _, err := repo.Create(ctx, input); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	input.FileIDs = nil
	input.TargetProjectID = nil
	if _, err := repo.Create(ctx, input); err == nil {
		t.Fatal("missing target accepted")
	}
	input.TargetUserID = &target
	input.TargetProjectID = &target
	if _, err := repo.Create(ctx, input); err == nil {
		t.Fatal("two targets accepted")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM support.support_ticket WHERE id=$1`, complaint.ID); err == nil {
		t.Fatal("NO ACTION violated")
	}
	if _, err := db.ExecContext(ctx, `UPDATE support.support_ticket SET status='declined' WHERE id=$1`, complaint.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE support.support_ticket SET status='resolved' WHERE id=$1`, complaint.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE support.support_ticket SET status='closed' WHERE id=$1`, complaint.ID); err == nil {
		t.Fatal("unexpected status accepted")
	}
}
