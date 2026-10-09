package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Files = (*Postgres)(nil)

const fileColumns = `id::text, owner_user_id::text, bucket_name, object_key,
	original_name, content_type, size_bytes, etag, checksum_sha256,
	status::text, created_at, updated_at, uploaded_at, deleted_at, is_public`

type scanner interface {
	Scan(...any) error
}

// FindReadable allows an empty userID for anonymous access to public files.
func (p *Postgres) FindReadable(ctx context.Context, fileID, userID string) (domain.File, error) {
	file, err := scanFile(p.db.QueryRowContext(ctx, `SELECT `+fileColumns+`
		FROM media.file WHERE id = $1 AND (owner_user_id = NULLIF($2, '')::uuid OR is_public)`, fileID, userID))
	if err != nil {
		return domain.File{}, fmt.Errorf("find readable file: %w", err)
	}
	return file, nil
}

func scanFile(row scanner) (domain.File, error) {
	var file domain.File
	err := row.Scan(&file.ID, &file.OwnerUserID, &file.BucketName, &file.ObjectKey,
		&file.OriginalName, &file.ContentType, &file.SizeBytes, &file.ETag,
		&file.ChecksumSHA256, &file.Status, &file.CreatedAt, &file.UpdatedAt,
		&file.UploadedAt, &file.DeletedAt, &file.IsPublic)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.File{}, domain.ErrNotFound
	}
	return file, err
}

// CreatePending сохраняет файл без фактических метаданных ещё не загруженного объекта.
func (p *Postgres) CreatePending(ctx context.Context, input domain.NewFile) (domain.File, error) {
	file, err := scanFile(p.db.QueryRowContext(ctx, `
		INSERT INTO media.file (id, owner_user_id, bucket_name, object_key, original_name, content_type, is_public)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+fileColumns, input.ID, input.OwnerUserID, input.BucketName,
		input.ObjectKey, input.OriginalName, input.ContentType, input.IsPublic))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.File{}, domain.ErrAlreadyExists
		}
		return domain.File{}, fmt.Errorf("создание файла: %w", err)
	}
	return file, nil
}

// FindOwned не раскрывает существование файла другому пользователю.
func (p *Postgres) FindOwned(ctx context.Context, fileID, userID string) (domain.File, error) {
	file, err := scanFile(p.db.QueryRowContext(ctx, `SELECT `+fileColumns+`
		FROM media.file WHERE id = $1 AND owner_user_id = $2`, fileID, userID))
	if err != nil {
		return domain.File{}, fmt.Errorf("поиск файла владельца: %w", err)
	}
	return file, nil
}

// MarkReady атомарно переводит pending в ready. Повтор возвращает сохранённые
// метаданные, не перезаписывая их. Запрос к S3 выполняется до этого метода.
func (p *Postgres) MarkReady(ctx context.Context, fileID, userID string, object domain.ObjectInfo) (domain.File, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.File{}, fmt.Errorf("начало подтверждения файла: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	file, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileColumns+`
		FROM media.file WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`, fileID, userID))
	if err != nil {
		return domain.File{}, fmt.Errorf("чтение подтверждаемого файла: %w", err)
	}
	switch file.Status {
	case domain.FileStatusReady:
		// Первый успешный вызов уже сохранил метаданные.
	case domain.FileStatusPending:
		file, err = scanFile(tx.QueryRowContext(ctx, `UPDATE media.file
			SET status = 'ready', size_bytes = $3, content_type = $4, etag = $5,
			    checksum_sha256 = $6, uploaded_at = now(), updated_at = now()
			WHERE id = $1 AND owner_user_id = $2 AND status = 'pending'
			RETURNING `+fileColumns, fileID, userID, object.SizeBytes,
			object.ContentType, object.ETag, object.ChecksumSHA256))
		if err != nil {
			return domain.File{}, fmt.Errorf("сохранение метаданных загруженного файла: %w", err)
		}
	default:
		return domain.File{}, domain.ErrInvalidStatus
	}
	if err := tx.Commit(); err != nil {
		return domain.File{}, fmt.Errorf("фиксация подтверждения файла: %w", err)
	}
	return file, nil
}
