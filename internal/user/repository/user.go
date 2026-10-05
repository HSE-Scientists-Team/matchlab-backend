package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrEmailTaken              = errors.New("email уже зарегистрирован")
	ErrNotFound                = errors.New("пользователь не найден")
	ErrVerificationNotFound    = errors.New("запрос на подтверждение адреса не найден")
	ErrVerificationExpired     = errors.New("срок действия запроса на подтверждение адреса истёк")
	ErrOrganizationUnavailable = errors.New("подтверждение почты для этой организации недоступно")
)

type Account struct {
	ID           string
	PasswordHash string
	Verified     bool
	Status       string
}

type EmailStatus struct {
	Email        string
	PendingEmail string
	Status       string
}

type Users interface {
	FindByEmail(context.Context, string) (Account, error)
	MarkLogin(context.Context, string) error
	SaveRegistration(context.Context, string, string, string, time.Time) error
	ConfirmEmail(context.Context, string, time.Time) error
	GetEmailStatus(context.Context, string) (EmailStatus, error)
}

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Users = (*Postgres)(nil)

func (p *Postgres) FindByEmail(ctx context.Context, email string) (Account, error) {
	var account Account
	err := p.db.QueryRowContext(ctx, `
		SELECT account.id::text, account.password_hash, account.status::text,
            EXISTS (SELECT 1 FROM users.user_email verified WHERE verified.user_id = account.id AND verified.email = account.email)
		FROM users.user_account account
		WHERE lower(account.email) = $1`, email).Scan(&account.ID, &account.PasswordHash, &account.Status, &account.Verified)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("поиск учётной записи по email: %w", err)
	}
	return account, nil
}

func (p *Postgres) MarkLogin(ctx context.Context, userID string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE users.user_account SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("обновление времени последнего входа: %w", err)
	}
	return nil
}

// SaveRegistration заменяет только заявку, не затрагивая существующие аккаунты.
func (p *Postgres) SaveRegistration(ctx context.Context, email, passwordHash, tokenHash string, expiresAt time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начало регистрации: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockRegistrationEmail(ctx, tx, email); err != nil {
		return err
	}
	if err := checkTrustedEmailDomain(ctx, tx, email); err != nil {
		return err
	}
	var registered bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users.user_account WHERE lower(email) = $1)`, email).Scan(&registered); err != nil {
		return fmt.Errorf("проверка регистрации адреса: %w", err)
	}
	if registered {
		return ErrEmailTaken
	}
	_, err = tx.ExecContext(ctx, `
        INSERT INTO users.registration_request (email, password_hash, token_hash, expires_at)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (email) DO UPDATE
        SET password_hash = EXCLUDED.password_hash,
            token_hash = EXCLUDED.token_hash,
            expires_at = EXCLUDED.expires_at,
            created_at = now()`, email, passwordHash, tokenHash, expiresAt)
	if err != nil {
		return fmt.Errorf("сохранение заявки на регистрацию: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("фиксация заявки на регистрацию: %w", err)
	}
	return nil
}

func (p *Postgres) ConfirmEmail(ctx context.Context, tokenHash string, now time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начало подтверждения адреса: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Находим email, затем сериализуем подтверждение и замену заявки.
	// После блокировки повторно проверяем токен: он мог быть заменён.
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM users.registration_request WHERE token_hash = $1`, tokenHash).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerificationNotFound
	}
	if err != nil {
		return fmt.Errorf("поиск заявки на регистрацию: %w", err)
	}
	if err := lockRegistrationEmail(ctx, tx, email); err != nil {
		return err
	}
	var passwordHash string
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT password_hash, expires_at FROM users.registration_request WHERE email = $1 AND token_hash = $2 FOR UPDATE`, email, tokenHash).Scan(&passwordHash, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerificationNotFound
	}
	if err != nil {
		return fmt.Errorf("чтение заявки на регистрацию: %w", err)
	}
	if !expiresAt.After(now) {
		return ErrVerificationExpired
	}
	if err := checkTrustedEmailDomain(ctx, tx, email); err != nil {
		return err
	}
	var userID string
	err = tx.QueryRowContext(ctx, `INSERT INTO users.user_account (email, password_hash) VALUES ($1, $2) RETURNING id::text`, email, passwordHash).Scan(&userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return fmt.Errorf("создание подтверждённой учётной записи: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users.user_email (user_id, email, verified_at) VALUES ($1, $2, $3)`, userID, email, now); err != nil {
		return fmt.Errorf("сохранение подтверждения адреса: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users.registration_request WHERE email = $1`, email); err != nil {
		return fmt.Errorf("использование заявки на регистрацию: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("фиксация подтверждения регистрации: %w", err)
	}
	return nil
}

// Одинаковый порядок блокировок исключает создание аккаунта одновременно
// с заменой его заявки. Коллизия хешей лишь сериализует разные email.
func lockRegistrationEmail(ctx context.Context, tx *sql.Tx, email string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, email); err != nil {
		return fmt.Errorf("блокировка регистрации адреса: %w", err)
	}
	return nil
}

type domainQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// checkTrustedEmailDomain проверяет точное правило и правила вида *.example.org.
// Звёздочка охватывает один или несколько уровней поддоменов, но не корень.
func checkTrustedEmailDomain(ctx context.Context, db domainQuerier, email string) error {
	separator := strings.LastIndexByte(email, '@')
	if separator < 0 {
		return fmt.Errorf("проверка доверенного домена: адрес не содержит домен")
	}
	domain := strings.ToLower(email[separator+1:])
	var allowed bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM users.trusted_email_domain
			WHERE is_active AND (
				lower(domain) = $1
				OR (left(domain, 2) = '*.'
					AND length($1) > length(domain) - 1
					AND right($1, length(domain) - 1) = lower(substring(domain from 2)))
			)
		)`, domain).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("проверка доступности организации: %w", err)
	}
	if !allowed {
		return ErrOrganizationUnavailable
	}
	return nil
}

func (p *Postgres) GetEmailStatus(ctx context.Context, userID string) (EmailStatus, error) {
	var result EmailStatus
	err := p.db.QueryRowContext(ctx, `
		SELECT COALESCE(verified.email, ''), CASE WHEN verified.email IS NULL THEN account.email ELSE '' END,
		       CASE WHEN verified.email IS NOT NULL THEN 'verified'
		            ELSE 'pending' END
		FROM users.user_account AS account
		LEFT JOIN users.user_email AS verified ON verified.user_id = account.id
		WHERE account.id = $1`, userID).Scan(&result.Email, &result.PendingEmail, &result.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return EmailStatus{}, ErrNotFound
	}
	if err != nil {
		return EmailStatus{}, fmt.Errorf("получение состояния адреса: %w", err)
	}
	return result, nil
}
