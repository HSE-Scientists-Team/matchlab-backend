package repository

import (
	"context"
	"database/sql"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
)

type Postgres struct{ db *sql.DB }

func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

func (p *Postgres) Create(ctx context.Context, input domain.NewComplaint) (domain.Complaint, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Complaint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var result domain.Complaint
	err = tx.QueryRowContext(ctx, `INSERT INTO support.support_ticket
        (author_user_id, subject, description, target_user_id, target_project_id)
        VALUES ($1,$2,$3,$4,$5) RETURNING id,status,created_at`,
		input.UserID, input.Subject, input.Description, input.TargetUserID, input.TargetProjectID).
		Scan(&result.ID, &result.Status, &result.CreatedAt)
	if err != nil {
		return domain.Complaint{}, err
	}
	for _, id := range input.FileIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO support.support_ticket_attachment (support_ticket_id,file_id) VALUES ($1,$2)`, result.ID, id); err != nil {
			return domain.Complaint{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Complaint{}, err
	}
	return result, nil
}
