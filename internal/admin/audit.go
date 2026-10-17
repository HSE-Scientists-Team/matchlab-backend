package admin

import (
	"context"
	"database/sql"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
	"time"
)

func audit(ctx context.Context, tx *sql.Tx, actor, action, id, comment, result string) (string, error) {
	var event string
	e := tx.QueryRowContext(ctx, `INSERT INTO admin.audit_event(actor_admin_id,action,resource_id,comment,result,completed_at) VALUES(nullif($1,'')::uuid,$2,$3,$4,$5,CASE WHEN $5='started' THEN NULL ELSE now() END) RETURNING id::text`, actor, action, id, comment, result).Scan(&event)
	return event, m.Error(e)
}
func (s *Service) ListAudit(ctx context.Context, q *mpb.Query) (*mpb.EventList, error) {
	if _, e := self(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "actor_admin_id", "action", "result", "created_from_unix", "created_to_unix"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Result != "" && q.Result != "started" && q.Result != "succeeded" && q.Result != "failed" && q.Result != "unknown" {
		return nil, m.Invalid()
	}
	where := `WHERE ($1='' OR actor_admin_id::text=$1) AND ($2='' OR action=$2) AND ($3='' OR result=$3) AND ($4::bigint=0 OR created_at>=to_timestamp($4)) AND ($5::bigint=0 OR created_at<to_timestamp($5)) AND ($6='' OR position(lower($6) in lower(action||' '||resource_id||' '||comment))>0)`
	args := []any{q.ActorAdminId, q.Action, q.Result, q.CreatedFromUnix, q.CreatedToUnix, q.Text}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &mpb.EventList{Items: []*mpb.Event{}}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin.audit_event `+where, args...).Scan(&out.Total); e != nil {
		return nil, m.Error(e)
	}
	args = append(args, l, o)
	rows, e := tx.QueryContext(ctx, `SELECT id::text,coalesce(actor_admin_id::text,''),action,resource_id,comment,result,extract(epoch FROM created_at)::bigint FROM admin.audit_event `+where+` ORDER BY created_at DESC,id DESC LIMIT $7 OFFSET $8`, args...)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v := &mpb.Event{}
		if e = rows.Scan(&v.Id, &v.ActorAdminId, &v.Action, &v.ResourceId, &v.Comment, &v.Result, &v.CreatedAtUnix); e != nil {
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

// Cleanup removes expired credentials and classifies stale cross-service intents as unknown.
func (s *Service) Cleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = s.DB.ExecContext(call, `DELETE FROM admin.session WHERE expires_at<now()`)
			_, _ = s.DB.ExecContext(call, `DELETE FROM admin.login_throttle WHERE window_started_at<now()-interval '1 day'`)
			_, _ = s.DB.ExecContext(call, `UPDATE admin.audit_event SET result='unknown',completed_at=now() WHERE result='started' AND created_at<now()-interval '1 minute'`)
			cancel()
		}
	}
}
