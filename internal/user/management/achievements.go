package management

import (
	"context"
	"database/sql"
	"encoding/json"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/profile"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
)

func (s *Service) GetAchievementForReview(ctx context.Context, q *mpb.ID) (*pb.Achievement, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	a, e := (&profile.Repository{DB: s.DB}).Achievement(ctx, q.Id)
	return a, profileError(e)
}
func profileError(e error) error {
	if e == profile.ErrNotFound {
		return m.Error(sql.ErrNoRows)
	}
	return m.Error(e)
}

const achievementSelect = `SELECT id::text,user_id::text,data,coalesce(evidence_file_id::text,''),status,revision,moderation_comment,extract(epoch from created_at)::bigint FROM users.achievement `

func (s *Service) ListAchievementsForReview(ctx context.Context, q *mpb.Query) (*pb.ManagedAchievementList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "status", "user_id", "kind", "created_from_unix", "created_to_unix"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Status != "" && q.Status != "unverified" && q.Status != "pending_review" && q.Status != "verified" && q.Status != "needs_evidence" {
		return nil, m.Invalid()
	}
	if q.Kind != "" && q.Kind != "education" && q.Kind != "certificate" && q.Kind != "scientific" && q.Kind != "project" {
		return nil, m.Invalid()
	}
	where := `WHERE deleted_at IS NULL AND ($1='' OR status=$1) AND ($2='' OR user_id::text=$2) AND ($3='' OR kind=$3) AND ($4='' OR position(lower($4) in lower(data->>'title'))>0) AND ($5::bigint=0 OR created_at>=to_timestamp($5)) AND ($6::bigint=0 OR created_at<to_timestamp($6))`
	args := []any{q.Status, q.UserId, q.Kind, q.Text, q.CreatedFromUnix, q.CreatedToUnix}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.ManagedAchievementList{Items: []*pb.Achievement{}}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users.achievement `+where, args...).Scan(&out.Total)
	if e != nil {
		return nil, m.Error(e)
	}
	args = append(args, l, o)
	rows, e := tx.QueryContext(ctx, achievementSelect+where+` ORDER BY CASE WHEN status='pending_review' THEN 0 ELSE 1 END,CASE WHEN status='pending_review' THEN submitted_at END,CASE WHEN status<>'pending_review' THEN updated_at END DESC,id LIMIT $7 OFFSET $8`, args...)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := profile.ScanAchievement(rows)
		if err != nil {
			return nil, profileError(err)
		}
		out.Items = append(out.Items, a)
	}
	if e = rows.Err(); e != nil {
		return nil, m.Error(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}
func (s *Service) AchievementHistory(ctx context.Context, q *mpb.HistoryQuery) (*pb.AchievementDecisionList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	l, o, e := m.Page(q.Page)
	if e != nil {
		return nil, e
	}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.AchievementDecisionList{Items: []*pb.AchievementDecision{}}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users.achievement_decision WHERE achievement_id=$1`, q.Id).Scan(&out.Total); e != nil {
		return nil, m.Error(e)
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,coalesce(reviewer_admin_id::text,''),coalesce(reviewer_user_id::text,''),revision,approved,comment,reviewed_data,coalesce(evidence_file_id::text,''),extract(epoch FROM created_at)::bigint FROM users.achievement_decision WHERE achievement_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, q.Id, l, o)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v := &pb.AchievementDecision{ReviewedDetails: &pb.AchievementInput{}}
		var raw []byte
		if e = rows.Scan(&v.Id, &v.ActorAdminId, &v.LegacyReviewerUserId, &v.Revision, &v.Approved, &v.Comment, &raw, &v.EvidenceFileId, &v.CreatedAtUnix); e != nil {
			return nil, m.Error(e)
		}
		if e = json.Unmarshal(raw, v.ReviewedDetails); e != nil {
			return nil, m.Error(e)
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, m.Error(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}
