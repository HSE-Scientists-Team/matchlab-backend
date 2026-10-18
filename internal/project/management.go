package project

import (
	"context"
	"database/sql"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
)

func (s *Service) GetForModeration(ctx context.Context, q *pb.ProjectID) (*pb.Project, error) {
	if _, e := s.actor(ctx, true, true); e != nil {
		return nil, e
	}
	if !validID(q.Id) {
		return nil, invalid()
	}
	v, e := read(ctx, s.Repo.DB, q.Id, false)
	return v, failure(e)
}
func (s *Service) PaginatedModerationHistory(ctx context.Context, q *mpb.HistoryQuery) (*pb.ModerationList, error) {
	if _, e := s.actor(ctx, true, true); e != nil {
		return nil, e
	}
	if !validID(q.Id) {
		return nil, invalid()
	}
	l, o, e := m.Page(q.Page)
	if e != nil {
		return nil, e
	}
	tx, e := s.Repo.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, failure(e)
	}
	defer tx.Rollback()
	out := &pb.ModerationList{Items: []*pb.ModerationRecord{}}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM projects.moderation WHERE project_id=$1`, q.Id).Scan(&out.Total); e != nil {
		return nil, failure(e)
	}
	rows, e := tx.QueryContext(ctx, `SELECT coalesce(actor_user_id::text,''),coalesce(actor_admin_id::text,''),hidden,reason,extract(epoch FROM created_at)::bigint FROM projects.moderation WHERE project_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, q.Id, l, o)
	if e != nil {
		return nil, failure(e)
	}
	defer rows.Close()
	for rows.Next() {
		v := &pb.ModerationRecord{}
		if e = rows.Scan(&v.ActorUserId, &v.ActorAdminId, &v.Hidden, &v.Reason, &v.CreatedAtUnix); e != nil {
			return nil, failure(e)
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, failure(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, failure(e)
	}
	return out, nil
}
func (s *Service) GetStatistics(ctx context.Context, q *mpb.StatisticsQuery) (*mpb.Statistics, error) {
	if _, e := s.actor(ctx, true, true); e != nil {
		return nil, e
	}
	return m.Statistics(ctx, s.Repo.DB, q, []m.StatQuery{
		{Category: "projects_by_status", SQL: `SELECT status,count(*) FROM projects.project GROUP BY status`},
		{Category: "projects_by_visibility", SQL: `SELECT CASE WHEN moderation_hidden THEN 'hidden' ELSE 'visible' END,count(*) FROM projects.project GROUP BY moderation_hidden`},
		{Category: "projects_created", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM projects.project WHERE created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
		{Category: "moderation_decisions", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM projects.moderation WHERE created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
	})
}
