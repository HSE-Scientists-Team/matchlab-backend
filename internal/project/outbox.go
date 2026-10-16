package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc/metadata"
	"log/slog"
	"time"
)

// DispatchOnce retries durable snapshots independently: a poison record must not
// roll back successful deliveries for unrelated projects. Recipient revisions
// protect against duplicate and out-of-order delivery by concurrent replicas.
func (s *Service) DispatchOnce(ctx context.Context) error {
	rows, err := s.Repo.DB.QueryContext(ctx, `SELECT project_id::text,revision FROM projects.context_outbox WHERE delivered_at IS NULL ORDER BY created_at,project_id,revision LIMIT 20`)
	if err != nil {
		return err
	}
	type key struct {
		id       string
		revision int
	}
	keys := []key{}
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.id, &k.revision); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, k := range keys {
		if err = s.deliver(ctx, k.id, k.revision); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) deliver(ctx context.Context, id string, revision int) error {
	tx, err := s.Repo.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM projects.context_outbox WHERE project_id=$1 AND revision=$2 AND delivered_at IS NULL FOR UPDATE SKIP LOCKED`, id, revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p := &upb.ProjectContext{}
	if err = json.Unmarshal(raw, p); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("x-matchlab-project-token", s.SyncToken)), 3*time.Second)
	_, err = s.Users.RecordProjectContext(call, p)
	cancel()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects.context_outbox SET delivered_at=now() WHERE project_id=$1 AND revision=$2`, id, revision); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) RunOutbox(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
			logger.Error("project context delivery failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
