package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Job struct {
	ID             int64
	IdempotencyKey string
	Recipient      string
	Ciphertext     []byte
	Nonce          []byte
	ExpiresAt      time.Time
	Attempts       int
}

type Outbox interface {
	Enqueue(context.Context, string, string, []byte, []byte, time.Time) error
	Claim(context.Context, time.Duration) (*Job, error)
	MarkSent(context.Context, int64) error
	MarkFailed(context.Context, int64, int, time.Duration, string) error
	Cleanup(context.Context, time.Duration) error
}

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Outbox = (*Postgres)(nil)

func (p *Postgres) Enqueue(ctx context.Context, key, recipient string, ciphertext, nonce []byte, expiresAt time.Time) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO mail.email_outbox (idempotency_key, recipient, token_ciphertext, token_nonce, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) DO NOTHING`, key, recipient, ciphertext, nonce, expiresAt)
	if err != nil {
		return fmt.Errorf("enqueue email: %w", err)
	}
	return nil
}

func (p *Postgres) Claim(ctx context.Context, lease time.Duration) (*Job, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE mail.email_outbox
		SET status = 'dead', token_ciphertext = NULL, token_nonce = NULL,
		    lease_until = NULL, last_error = 'verification token expired'
		WHERE expires_at <= now() AND status IN ('pending', 'sending')`); err != nil {
		return nil, fmt.Errorf("expire outbox jobs: %w", err)
	}
	var job Job
	err = tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM mail.email_outbox
			WHERE (status = 'pending' AND next_attempt_at <= now())
			   OR (status = 'sending' AND lease_until <= now())
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE mail.email_outbox AS outbox
		SET status = 'sending', lease_until = now() + $1 * interval '1 second', attempts = attempts + 1
		FROM candidate
		WHERE outbox.id = candidate.id
		RETURNING outbox.id, outbox.idempotency_key, outbox.recipient, outbox.token_ciphertext,
		          outbox.token_nonce, outbox.expires_at, outbox.attempts`, lease.Seconds()).
		Scan(&job.ID, &job.IdempotencyKey, &job.Recipient, &job.Ciphertext, &job.Nonce, &job.ExpiresAt, &job.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty outbox claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next email: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return &job, nil
}

func (p *Postgres) MarkSent(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `
		UPDATE mail.email_outbox
		SET status = 'sent', sent_at = now(), lease_until = NULL,
		    token_ciphertext = NULL, token_nonce = NULL, last_error = NULL
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark email sent: %w", err)
	}
	return nil
}

func (p *Postgres) MarkFailed(ctx context.Context, id int64, attempts int, retryAfter time.Duration, message string) error {
	message = truncate(message, 1000)
	_, err := p.db.ExecContext(ctx, `
		UPDATE mail.email_outbox
		SET status = CASE WHEN attempts >= $2 THEN 'dead' ELSE 'pending' END,
		    next_attempt_at = now() + $3 * interval '1 second',
		    lease_until = NULL,
		    last_error = $4,
		    token_ciphertext = CASE WHEN attempts >= $2 THEN NULL ELSE token_ciphertext END,
		    token_nonce = CASE WHEN attempts >= $2 THEN NULL ELSE token_nonce END
		WHERE id = $1 AND status = 'sending'`, id, attempts, retryAfter.Seconds(), message)
	if err != nil {
		return fmt.Errorf("mark email failed: %w", err)
	}
	return nil
}

func (p *Postgres) Cleanup(ctx context.Context, retention time.Duration) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM mail.email_outbox
		WHERE status IN ('sent', 'dead') AND created_at < now() - $1 * interval '1 second'`, retention.Seconds()); err != nil {
		return fmt.Errorf("clean outbox history: %w", err)
	}
	return nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
