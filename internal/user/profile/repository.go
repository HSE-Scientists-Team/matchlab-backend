package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/jackc/pgx/v5/pgconn"
	"time"
)

var (
	ErrNotFound  = errors.New("не найдено")
	ErrConflict  = errors.New("состояние изменилось или операция уже выполнена")
	ErrForbidden = errors.New("операция запрещена")
)

type Repository struct{ DB *sql.DB }

func dbError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		}
	}
	return err
}
func (r *Repository) Identity(ctx context.Context, id string) (role, email string, err error) {
	err = r.DB.QueryRowContext(ctx, `SELECT a.role::text,e.email FROM users.user_account a JOIN users.user_email e ON e.user_id=a.id WHERE a.id=$1 AND a.status='active'`, id).Scan(&role, &email)
	return role, email, dbError(err)
}
func (r *Repository) Profile(ctx context.Context, id string) (*pb.Profile, *pb.PrivacySettings, error) {
	p := &pb.Profile{Details: &pb.ProfileInput{}}
	privacy := &pb.PrivacySettings{}
	var raw []byte
	err := r.DB.QueryRowContext(ctx, `SELECT p.user_id::text,p.data,p.show_contacts,p.show_participating_projects,EXISTS(SELECT 1 FROM users.user_email e WHERE e.user_id=p.user_id AND split_part(e.email,'@',2) IN ('hse.ru','edu.hse.ru')),(SELECT coalesce(avg(rating),0) FROM users.profile_review WHERE target_user_id=p.user_id),(SELECT count(*) FROM users.profile_review WHERE target_user_id=p.user_id),(SELECT count(*) FROM users.profile_review WHERE target_user_id=p.user_id AND rating>=4),(SELECT count(*) FROM users.project_context_member m JOIN users.project_context x ON x.project_id=m.project_id WHERE m.user_id=p.user_id AND x.state='completed') FROM users.user_profile p JOIN users.user_account a ON a.id=p.user_id WHERE p.user_id=$1 AND a.status='active'`, id).Scan(&p.UserId, &raw, &privacy.ShowContacts, &privacy.ShowParticipatingProjects, &p.Verified, &p.Rating, &p.ReviewCount, &p.PositiveReviewCount, &p.CompletedProjectCount)
	if err != nil {
		return nil, nil, dbError(err)
	}
	if err = json.Unmarshal(raw, p.Details); err != nil {
		return nil, nil, err
	}
	return p, privacy, nil
}
func (r *Repository) SaveProfile(ctx context.Context, id string, p *pb.ProfileInput) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, `UPDATE users.user_profile SET data=$2,updated_at=now() WHERE user_id=$1`, id, raw)
	return dbError(err)
}
func (r *Repository) Settings(ctx context.Context, id string, p *pb.PrivacySettings) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE users.user_profile SET show_contacts=$2,show_participating_projects=$3,updated_at=now() WHERE user_id=$1`, id, p.ShowContacts, p.ShowParticipatingProjects)
	return dbError(err)
}

const achievementSelect = `SELECT id::text,user_id::text,data,coalesce(evidence_file_id::text,''),status,revision,moderation_comment,extract(epoch from created_at)::bigint FROM users.achievement `

type scanner interface{ Scan(...any) error }

func scanAchievement(row scanner) (*pb.Achievement, error) {
	a := &pb.Achievement{Details: &pb.AchievementInput{}}
	var raw []byte
	var evidence string
	err := row.Scan(&a.Id, &a.UserId, &raw, &evidence, &a.Status, &a.Revision, &a.ModerationComment, &a.CreatedAtUnix)
	if err != nil {
		return nil, dbError(err)
	}
	if err = json.Unmarshal(raw, a.Details); err != nil {
		return nil, err
	}
	a.Details.Id = a.Id
	a.Details.EvidenceFileId = evidence
	a.Details.ExpectedRevision = a.Revision
	return a, nil
}
func (r *Repository) Achievement(ctx context.Context, id string) (*pb.Achievement, error) {
	return scanAchievement(r.DB.QueryRowContext(ctx, achievementSelect+`WHERE id=$1 AND deleted_at IS NULL`, id))
}
func (r *Repository) Achievements(ctx context.Context, owner, kind string, queue bool, limit, offset int32) (*pb.AchievementList, error) {
	rows, err := r.DB.QueryContext(ctx, achievementSelect+`WHERE deleted_at IS NULL AND (($1::boolean AND status='pending_review') OR (NOT $1 AND user_id::text=$2 AND ($3='' OR kind=$3))) ORDER BY CASE WHEN $1 THEN submitted_at ELSE created_at END,id LIMIT $4 OFFSET $5`, queue, owner, kind, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &pb.AchievementList{Items: []*pb.Achievement{}}
	for rows.Next() {
		a, err := scanAchievement(rows)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, a)
	}
	return result, rows.Err()
}
func (r *Repository) SaveAchievement(ctx context.Context, owner string, input *pb.AchievementInput) (*pb.Achievement, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	id := input.Id
	if id == "" {
		err = r.DB.QueryRowContext(ctx, `INSERT INTO users.achievement(user_id,kind,data,evidence_file_id) VALUES($1,$2,$3,nullif($4,'')::uuid) RETURNING id::text`, owner, input.Kind, raw, input.EvidenceFileId).Scan(&id)
	} else {
		var result sql.Result
		result, err = r.DB.ExecContext(ctx, `UPDATE users.achievement SET kind=$3,data=$4,evidence_file_id=nullif($5,'')::uuid,status='unverified',revision=revision+1,moderation_comment='',submitted_at=NULL,updated_at=now() WHERE id=$1 AND user_id=$2 AND revision=$6 AND deleted_at IS NULL`, id, owner, input.Kind, raw, input.EvidenceFileId, input.ExpectedRevision)
		if err == nil {
			count, _ := result.RowsAffected()
			if count == 0 {
				return nil, ErrConflict
			}
		}
	}
	if err != nil {
		return nil, dbError(err)
	}
	return r.Achievement(ctx, id)
}
func (r *Repository) DeleteAchievement(ctx context.Context, id, owner string) error {
	// Preserve immutable moderation audit while removing the public item.
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned string
	if err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM users.achievement WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&owned); err != nil {
		return dbError(err)
	}
	if owned != owner {
		return ErrNotFound
	}
	// Audit rows are retained; deleted items are omitted by the service.
	_, err = tx.ExecContext(ctx, `UPDATE users.achievement SET deleted_at=now(),revision=revision+1,updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repository) Submit(ctx context.Context, id, owner string, revision int32) (*pb.Achievement, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE users.achievement SET status='pending_review',revision=revision+1,submitted_at=now(),moderation_comment='',updated_at=now() WHERE id=$1 AND user_id=$2 AND revision=$3 AND status IN ('unverified','needs_evidence') AND deleted_at IS NULL`, id, owner, revision)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrConflict
	}
	return r.Achievement(ctx, id)
}
func (r *Repository) Decide(ctx context.Context, reviewer string, d *pb.VerificationDecision) (*pb.Achievement, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := scanAchievement(tx.QueryRowContext(ctx, achievementSelect+`WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, d.Id))
	if err != nil {
		return nil, err
	}
	if a.UserId == reviewer {
		return nil, ErrForbidden
	}
	if a.Revision != d.ExpectedRevision || a.Status != "pending_review" {
		return nil, ErrConflict
	}
	state := "needs_evidence"
	if d.Approve {
		state = "verified"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users.achievement_decision(achievement_id,revision,reviewer_user_id,approved,comment,reviewed_data,evidence_file_id) SELECT $1,$2,$3,$4,$5,data,evidence_file_id FROM users.achievement WHERE id=$1`, d.Id, d.ExpectedRevision, reviewer, d.Approve, d.Comment)
	if err != nil {
		return nil, dbError(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE users.achievement SET status=$2,moderation_comment=$3,updated_at=now() WHERE id=$1`, d.Id, state, d.Comment)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.Status = state
	a.ModerationComment = d.Comment
	return a, nil
}
func (r *Repository) Blocks(ctx context.Context, owner string, limit, offset int32) (*pb.BlockList, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT b.blocked_user_id::text,coalesce(p.data->>'display_name',''),extract(epoch FROM b.created_at)::bigint FROM users.user_block b LEFT JOIN users.user_profile p ON p.user_id=b.blocked_user_id WHERE blocker_user_id=$1 ORDER BY b.created_at,b.blocked_user_id LIMIT $2 OFFSET $3`, owner, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &pb.BlockList{Items: []*pb.Block{}}
	for rows.Next() {
		b := &pb.Block{}
		if err = rows.Scan(&b.UserId, &b.DisplayName, &b.CreatedAtUnix); err != nil {
			return nil, err
		}
		res.Items = append(res.Items, b)
	}
	return res, rows.Err()
}
func (r *Repository) Block(ctx context.Context, owner, target string, remove bool) error {
	query := `INSERT INTO users.user_block(blocker_user_id,blocked_user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`
	if remove {
		query = `DELETE FROM users.user_block WHERE blocker_user_id=$1 AND blocked_user_id=$2`
	}
	_, err := r.DB.ExecContext(ctx, query, owner, target)
	return dbError(err)
}
func (r *Repository) Blocked(ctx context.Context, a, b string) (bool, error) {
	var blocked bool
	err := r.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users.user_block WHERE (blocker_user_id=$1 AND blocked_user_id=$2) OR (blocker_user_id=$2 AND blocked_user_id=$1))`, a, b).Scan(&blocked)
	return blocked, err
}
func (r *Repository) RecordProject(ctx context.Context, p *pb.ProjectContext) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,38))`, p.ProjectId); err != nil {
		return err
	}
	var completed sql.NullTime
	var title, initiator string
	var source int64
	var previousState string
	err = tx.QueryRowContext(ctx, `SELECT completed_at,title,initiator_user_id::text,source_revision,state FROM users.project_context WHERE project_id=$1 FOR UPDATE`, p.ProjectId).Scan(&completed, &title, &initiator, &source, &previousState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && source >= p.SourceRevision {
		return tx.Commit()
	}
	if err == nil && previousState == "cancelled" && p.State != "cancelled" {
		return ErrConflict
	}
	if completed.Valid {
		if title != p.Title || initiator != p.InitiatorUserId || completed.Time.Unix() != p.CompletedAtUnix {
			return ErrConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT user_id::text FROM users.project_context_member WHERE project_id=$1`, p.ProjectId)
		if err != nil {
			return err
		}
		members := map[string]bool{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			members[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(members) != len(p.ParticipantUserIds) {
			return ErrConflict
		}
		for _, id := range p.ParticipantUserIds {
			if !members[id] {
				return ErrConflict
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users.project_context SET source_revision=$2 WHERE project_id=$1`, p.ProjectId, p.SourceRevision); err != nil {
			return err
		}
		return tx.Commit()
	}
	var when any
	if p.CompletedAtUnix > 0 {
		when = time.Unix(p.CompletedAtUnix, 0).UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users.project_context(project_id,title,initiator_user_id,completed_at,source_revision,state) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(project_id) DO UPDATE SET title=EXCLUDED.title,initiator_user_id=EXCLUDED.initiator_user_id,completed_at=EXCLUDED.completed_at,source_revision=EXCLUDED.source_revision,state=EXCLUDED.state`, p.ProjectId, p.Title, p.InitiatorUserId, when, p.SourceRevision, p.State)
	if err != nil {
		return dbError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM users.project_context_member WHERE project_id=$1`, p.ProjectId); err != nil {
		return err
	}
	members, err := json.Marshal(p.ParticipantUserIds)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO users.project_context_member(project_id,user_id) SELECT $1,value::uuid FROM jsonb_array_elements_text($2::jsonb)`, p.ProjectId, string(members)); err != nil {
		return dbError(err)
	}

	return tx.Commit()
}

const reviewSelect = `SELECT r.id::text,r.author_user_id::text,coalesce(u.data->>'display_name',''),CASE WHEN r.author_user_id=p.initiator_user_id THEN 'initiator' ELSE 'participant' END,r.project_id::text,p.title,r.target_user_id::text,r.title,r.text,r.rating,extract(epoch FROM r.created_at)::bigint,extract(epoch FROM p.completed_at)::bigint FROM users.profile_review r JOIN users.project_context p USING(project_id) LEFT JOIN users.user_profile u ON u.user_id=r.author_user_id `

func scanReview(row scanner) (*pb.Review, error) {
	r := &pb.Review{}
	err := row.Scan(&r.Id, &r.AuthorUserId, &r.AuthorDisplayName, &r.AuthorRole, &r.ProjectId, &r.ProjectTitle, &r.TargetUserId, &r.Title, &r.Text, &r.Rating, &r.CreatedAtUnix, &r.ProjectCompletedAtUnix)
	return r, dbError(err)
}
func (r *Repository) Reviews(ctx context.Context, owner string, limit, offset int32) (*pb.ReviewList, error) {
	rows, err := r.DB.QueryContext(ctx, reviewSelect+`WHERE target_user_id=$1 ORDER BY r.created_at DESC,r.id LIMIT $2 OFFSET $3`, owner, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &pb.ReviewList{Items: []*pb.Review{}}
	for rows.Next() {
		v, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, v)
	}
	return res, rows.Err()
}
func (r *Repository) CreateReview(ctx context.Context, author string, input *pb.ReviewInput) (*pb.Review, error) {
	var id string
	err := r.DB.QueryRowContext(ctx, `INSERT INTO users.profile_review(project_id,author_user_id,target_user_id,title,text,rating) SELECT $1,$2,$3,$4,$5,$6 FROM users.project_context WHERE project_id=$1 AND completed_at<=now() RETURNING id::text`, input.ProjectId, author, input.TargetUserId, input.Title, input.Text, input.Rating).Scan(&id)
	if err != nil {
		return nil, dbError(err)
	}
	return scanReview(r.DB.QueryRowContext(ctx, reviewSelect+`WHERE r.id=$1`, id))
}

// Only current teams grant contact visibility; historical collaboration does not.
func (r *Repository) CanSeeContacts(ctx context.Context, owner, viewer string, public bool) (bool, error) {
	if owner == viewer {
		return true, nil
	}
	if viewer != "" {
		blocked, err := r.Blocked(ctx, owner, viewer)
		if err != nil {
			return false, err
		}
		if blocked {
			return false, nil
		}
	}
	if public {
		return true, nil
	}
	if viewer == "" {
		return false, nil
	}
	var shared bool
	err := r.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users.project_context_member a JOIN users.project_context_member b ON b.project_id=a.project_id JOIN users.project_context p ON p.project_id=a.project_id WHERE a.user_id=$1 AND b.user_id=$2 AND p.state='active')`, owner, viewer).Scan(&shared)
	return shared, err
}
