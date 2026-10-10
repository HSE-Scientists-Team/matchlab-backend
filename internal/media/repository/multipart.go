package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

type UploadOwner struct{ FileID, UserID string }

type Multiparts interface {
	CreateMultipart(context.Context, domain.NewFile, domain.Multipart) (domain.File, error)
	// WithMultipart locks file and upload across replicas. Mutations commit together.
	WithMultipart(context.Context, string, string, func(*domain.File, *domain.Multipart) error) error
	IsMultipart(context.Context, string) (bool, error)
	RecoverableUploads(context.Context) ([]UploadOwner, error)
}

func (p *Postgres) CreateMultipart(ctx context.Context, input domain.NewFile, upload domain.Multipart) (domain.File, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.File{}, err
	}
	defer tx.Rollback()
	file, err := scanFile(tx.QueryRowContext(ctx, `INSERT INTO media.file
		(id, owner_user_id, bucket_name, object_key, original_name, content_type, is_public, expected_size_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+fileColumns,
		input.ID, input.OwnerUserID, input.BucketName, input.ObjectKey, input.OriginalName, input.ContentType, input.IsPublic, upload.ExpectedSize))
	if err != nil {
		return domain.File{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO media.multipart_upload
		(file_id,s3_upload_id,expected_size_bytes,part_size_bytes,expires_at)
		VALUES ($1,$2,$3,$4,$5)`, file.ID, upload.UploadID, upload.ExpectedSize, upload.PartSize, upload.ExpiresAt)
	if err != nil {
		return domain.File{}, err
	}
	return file, tx.Commit()
}

func (p *Postgres) WithMultipart(ctx context.Context, id, owner string, fn func(*domain.File, *domain.Multipart) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	file, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM media.file
		WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return err
	}
	var upload domain.Multipart
	var manifest []byte
	err = tx.QueryRowContext(ctx, `SELECT file_id::text,s3_upload_id,expected_size_bytes,part_size_bytes,status,expires_at,completion_manifest
		FROM media.multipart_upload WHERE file_id=$1 FOR UPDATE`, id).Scan(&upload.FileID, &upload.UploadID, &upload.ExpectedSize, &upload.PartSize, &upload.Status, &upload.ExpiresAt, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(manifest, &upload.Manifest); err != nil {
		return err
	}
	originalFileStatus, originalUploadStatus := file.Status, upload.Status
	if err = fn(&file, &upload); err != nil {
		return err
	}
	// Reads and link issuance do not change lifecycle state.
	if file.Status == originalFileStatus && upload.Status == originalUploadStatus {
		return tx.Commit()
	}
	if upload.Manifest == nil {
		upload.Manifest = []domain.Part{}
	}
	manifest, err = json.Marshal(upload.Manifest)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE media.multipart_upload SET status=$2,completion_manifest=$3,updated_at=now() WHERE file_id=$1`, id, upload.Status, string(manifest))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE media.file SET status=$2,size_bytes=$3,etag=$4,checksum_sha256=$5,uploaded_at=$6,updated_at=now() WHERE id=$1`, id, file.Status, file.SizeBytes, file.ETag, file.ChecksumSHA256, file.UploadedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) IsMultipart(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media.multipart_upload WHERE file_id=$1)`, id).Scan(&exists)
	return exists, err
}

func (p *Postgres) RecoverableUploads(ctx context.Context) ([]UploadOwner, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT m.file_id::text,f.owner_user_id::text
		FROM media.multipart_upload m JOIN media.file f ON f.id=m.file_id
		WHERE (m.status='uploading' AND m.expires_at<=now()) OR m.status='completing'
		ORDER BY m.updated_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UploadOwner
	for rows.Next() {
		var entry UploadOwner
		if err := rows.Scan(&entry.FileID, &entry.UserID); err != nil {
			return nil, fmt.Errorf("read recoverable upload: %w", err)
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
