package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Files = (*Postgres)(nil)

// StalePendingFiles uses an ID cursor so a failed row cannot starve later batches.
func (p *Postgres) StalePendingFiles(ctx context.Context, cutoff time.Time, after string) ([]UploadOwner, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id::text,owner_user_id::text FROM media.file
		WHERE status='pending' AND updated_at<=$1 AND ($2='' OR id>NULLIF($2,'')::uuid)
		ORDER BY id LIMIT 100`, cutoff, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []UploadOwner
	for rows.Next() {
		var file UploadOwner
		if err := rows.Scan(&file.FileID, &file.UserID); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// WithStalePending rechecks eligibility under the same locks as client completion.
// S3 cleanup must succeed before the callback's terminal state is committed.
func (p *Postgres) WithStalePending(ctx context.Context, id, owner string, cutoff time.Time, fn func(*domain.File, *domain.Multipart) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	file, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM media.file
		WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, owner))
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if file.Status != domain.FileStatusPending || file.UpdatedAt.After(cutoff) {
		return nil
	}
	var upload domain.Multipart
	var manifest []byte
	err = tx.QueryRowContext(ctx, `SELECT file_id::text,s3_upload_id,expected_size_bytes,part_size_bytes,status,expires_at,completion_manifest
		FROM media.multipart_upload WHERE file_id=$1 FOR UPDATE`, id).Scan(&upload.FileID, &upload.UploadID, &upload.ExpectedSize, &upload.PartSize, &upload.Status, &upload.ExpiresAt, &manifest)
	var multipart *domain.Multipart
	if err == nil {
		if err := json.Unmarshal(manifest, &upload.Manifest); err != nil {
			return err
		}
		multipart = &upload
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := fn(&file, multipart); err != nil {
		return err
	}
	if multipart != nil {
		if upload.Manifest == nil {
			upload.Manifest = []domain.Part{}
		}
		manifest, err = json.Marshal(upload.Manifest)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media.multipart_upload SET status=$2,completion_manifest=$3,updated_at=now() WHERE file_id=$1`, id, upload.Status, string(manifest)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media.file SET status=$2,size_bytes=$3,etag=$4,checksum_sha256=$5,uploaded_at=$6,updated_at=now() WHERE id=$1`, id, file.Status, file.SizeBytes, file.ETag, file.ChecksumSHA256, file.UploadedAt); err != nil {
		return err
	}
	return tx.Commit()
}

const fileColumns = `id::text, owner_user_id::text, bucket_name, object_key,
	original_name, content_type, size_bytes, etag, checksum_sha256,
	status::text, created_at, updated_at, uploaded_at, deleted_at, is_public, expected_size_bytes`

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
		&file.UploadedAt, &file.DeletedAt, &file.IsPublic, &file.ExpectedSizeBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.File{}, domain.ErrNotFound
	}
	return file, err
}

// CreatePending сохраняет файл без фактических метаданных ещё не загруженного объекта.
func (p *Postgres) CreatePending(ctx context.Context, input domain.NewFile) (domain.File, error) {
	file, err := scanFile(p.db.QueryRowContext(ctx, `
		INSERT INTO media.file (id, owner_user_id, bucket_name, object_key, original_name, content_type, is_public, expected_size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,0))
		RETURNING `+fileColumns, input.ID, input.OwnerUserID, input.BucketName,
		input.ObjectKey, input.OriginalName, input.ContentType, input.IsPublic, input.ExpectedSizeBytes))
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

// BeginDeletion serializes deletion with upload completion and other deletes.
// Persist the intent before calling S3 so a worker can recover a lost request.
func (p *Postgres) BeginDeletion(ctx context.Context, id, owner string) (domain.File, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.File{}, err
	}
	defer tx.Rollback()
	file, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM media.file
		WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return domain.File{}, err
	}
	switch file.Status {
	case domain.FileStatusReady:
		file, err = scanFile(tx.QueryRowContext(ctx, `UPDATE media.file SET status='deleting',updated_at=now()
			WHERE id=$1 AND owner_user_id=$2 RETURNING `+fileColumns, id, owner))
		if err != nil {
			return domain.File{}, err
		}
	case domain.FileStatusDeleting, domain.FileStatusDeleted:
	default:
		return domain.File{}, domain.ErrInvalidStatus
	}
	return file, tx.Commit()
}

func (p *Postgres) FinishDeletion(ctx context.Context, id, owner string) error {
	result, err := p.db.ExecContext(ctx, `UPDATE media.file SET status='deleted',
		deleted_at=COALESCE(deleted_at,now()),updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND status IN ('deleting','deleted')`, id, owner)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrInvalidStatus
	}
	return nil
}

func (p *Postgres) DeletingFiles(ctx context.Context) ([]UploadOwner, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id::text,owner_user_id::text FROM media.file
		WHERE status='deleting' ORDER BY updated_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []UploadOwner
	for rows.Next() {
		var file UploadOwner
		if err := rows.Scan(&file.FileID, &file.UserID); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}
