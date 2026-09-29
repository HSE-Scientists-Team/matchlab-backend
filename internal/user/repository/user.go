package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrLoginTaken           = errors.New("login is already registered")
	ErrNotFound             = errors.New("user not found")
	ErrEmailAlreadyVerified = errors.New("email is already verified for this user")
	ErrVerificationNotFound = errors.New("email verification request not found")
	ErrVerificationExpired  = errors.New("email verification request expired")
	ErrEmailClaimed         = errors.New("email is already verified by another user")
)

type Account struct {
	ID           string
	PasswordHash string
	Status       string
}

type EmailStatus struct {
	Email        string
	PendingEmail string
	Status       string
}

type Users interface {
	Create(context.Context, string, string) (string, error)
	FindByLogin(context.Context, string) (Account, error)
	MarkLogin(context.Context, string) error
	SaveEmailVerification(context.Context, string, string, string, time.Time) error
	ConfirmEmail(context.Context, string, time.Time) error
	GetEmailStatus(context.Context, string) (EmailStatus, error)
}

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Users = (*Postgres)(nil)

func (p *Postgres) Create(ctx context.Context, login, passwordHash string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO users.user_account (login, password_hash, status)
		VALUES ($1, $2, 'active')
		RETURNING id::text`, login, passwordHash).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", ErrLoginTaken
		}
		return "", fmt.Errorf("insert account: %w", err)
	}
	return id, nil
}

func (p *Postgres) FindByLogin(ctx context.Context, login string) (Account, error) {
	var account Account
	err := p.db.QueryRowContext(ctx, `
		SELECT id::text, password_hash, status::text
		FROM users.user_account
		WHERE lower(login) = $1`, login).Scan(&account.ID, &account.PasswordHash, &account.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("select account by login: %w", err)
	}
	return account, nil
}

func (p *Postgres) MarkLogin(ctx context.Context, userID string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE users.user_account SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("update last login: %w", err)
	}
	return nil
}

func (p *Postgres) SaveEmailVerification(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time) error {
	var alreadyVerified bool
	err := p.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM users.user_email
			WHERE user_id = $1 AND lower(email) = $2
		)`, userID, email).Scan(&alreadyVerified)
	if err != nil {
		return fmt.Errorf("check current verified email: %w", err)
	}
	if alreadyVerified {
		return ErrEmailAlreadyVerified
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO users.email_verification_request (user_id, email, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		SET email = EXCLUDED.email,
		    token_hash = EXCLUDED.token_hash,
		    expires_at = EXCLUDED.expires_at,
		    created_at = now()`, userID, email, tokenHash, expiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrNotFound
		}
		return fmt.Errorf("save email verification request: %w", err)
	}
	return nil
}

func (p *Postgres) ConfirmEmail(ctx context.Context, tokenHash string, now time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin email confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var userID, email string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT user_id::text, email, expires_at
		FROM users.email_verification_request
		WHERE token_hash = $1
		FOR UPDATE`, tokenHash).Scan(&userID, &email, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerificationNotFound
	}
	if err != nil {
		return fmt.Errorf("find email verification request: %w", err)
	}

	if !expiresAt.After(now) {
		return ErrVerificationExpired
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO users.user_email (user_id, email, verified_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		SET email = EXCLUDED.email, verified_at = EXCLUDED.verified_at`, userID, email, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailClaimed
		}
		return fmt.Errorf("assign verified email: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users.email_verification_request WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("consume email verification request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit email confirmation: %w", err)
	}
	return nil
}

func (p *Postgres) GetEmailStatus(ctx context.Context, userID string) (EmailStatus, error) {
	var result EmailStatus
	err := p.db.QueryRowContext(ctx, `
		SELECT COALESCE(verified.email, ''), COALESCE(pending.email, ''),
		       CASE WHEN verified.email IS NOT NULL THEN 'verified'
		            WHEN pending.email IS NOT NULL THEN 'pending'
		            ELSE 'not_set' END
		FROM users.user_account AS account
		LEFT JOIN users.user_email AS verified ON verified.user_id = account.id
		LEFT JOIN users.email_verification_request AS pending
		  ON pending.user_id = account.id AND pending.expires_at > now()
		WHERE account.id = $1`, userID).Scan(&result.Email, &result.PendingEmail, &result.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return EmailStatus{}, ErrNotFound
	}
	if err != nil {
		return EmailStatus{}, fmt.Errorf("get email status: %w", err)
	}
	return result, nil
}
