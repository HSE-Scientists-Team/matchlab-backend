package management

import (
	"context"
	"database/sql"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	"time"
)

// SQL is defined by the owning service; no client-supplied identifiers are used.
type StatQuery struct {
	Category, SQL string
	Period, Daily bool
}

func Statistics(ctx context.Context, db *sql.DB, q *pb.StatisticsQuery, queries []StatQuery) (*pb.Statistics, error) {
	from, to, err := Range(q)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, Error(err)
	}
	defer tx.Rollback()
	out := &pb.Statistics{Counters: []*pb.Counter{}, Daily: []*pb.DailyCount{}, GeneratedAtUnix: time.Now().Unix()}
	for _, spec := range queries {
		var args []any
		if spec.Period {
			args = []any{from, to}
		}
		rows, e := tx.QueryContext(ctx, spec.SQL, args...)
		if e != nil {
			return nil, Error(e)
		}
		for rows.Next() {
			if spec.Daily {
				v := &pb.DailyCount{Category: spec.Category}
				e = rows.Scan(&v.Date, &v.Count)
				out.Daily = append(out.Daily, v)
			} else {
				v := &pb.Counter{Category: spec.Category}
				e = rows.Scan(&v.Value, &v.Count)
				out.Counters = append(out.Counters, v)
			}
			if e != nil {
				rows.Close()
				return nil, Error(e)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, Error(e)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, Error(err)
	}
	return out, nil
}
func History(ctx context.Context, db *sql.DB, q *pb.HistoryQuery, source string, args ...any) (*pb.EventList, error) {
	if !ID(q.GetId()) {
		return nil, Invalid()
	}
	limit, offset, e := Page(q.GetPage())
	if e != nil {
		return nil, e
	}
	out := &pb.EventList{Items: []*pb.Event{}}
	// Source is a hard-coded SELECT yielding the common event columns and resource_id.
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, Error(e)
	}
	defer tx.Rollback()
	e = tx.QueryRowContext(ctx, "SELECT count(*) FROM ("+source+") x WHERE resource_id=$1", q.Id).Scan(&out.Total)
	if e != nil {
		return nil, Error(e)
	}
	rows, e := tx.QueryContext(ctx, "SELECT id,actor_admin_id,action,resource_id,comment,previous_status,status,revision,created_at_unix,previous_assigned_admin_id,assigned_admin_id FROM ("+source+") x WHERE resource_id=$1 ORDER BY created_at_unix DESC,id DESC LIMIT $2 OFFSET $3", q.Id, limit, offset)
	if e != nil {
		return nil, Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v := &pb.Event{Result: "succeeded"}
		if e = rows.Scan(&v.Id, &v.ActorAdminId, &v.Action, &v.ResourceId, &v.Comment, &v.PreviousStatus, &v.Status, &v.Revision, &v.CreatedAtUnix, &v.PreviousAssignedAdminId, &v.AssignedAdminId); e != nil {
			return nil, Error(e)
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, Error(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, Error(e)
	}
	return out, nil
}
