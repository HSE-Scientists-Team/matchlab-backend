// Package management implements administrative Support operations and their atomic history.
package management

import (
	"context"
	"database/sql"
	"encoding/json"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	mediapb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
	"google.golang.org/grpc"
	"time"
)

type Media interface {
	CreateDownloadURL(context.Context, *mediapb.CreateDownloadURLRequest, ...grpc.CallOption) (*mediapb.CreateDownloadURLResponse, error)
}
type Service struct {
	pb.UnimplementedSupportManagementServiceServer
	DB    *sql.DB
	Media Media
}

func actor(ctx context.Context) (adminauth.Actor, error) { return adminauth.Verify(ctx, "support") }

const ticketSelect = `SELECT t.id::text,t.author_user_id::text,coalesce(t.assigned_admin_id::text,''),t.status::text,t.subject,t.description,coalesce(t.target_user_id::text,''),coalesce(t.target_project_id::text,''),coalesce((SELECT jsonb_agg(a.file_id::text ORDER BY a.id) FROM support.support_ticket_attachment a WHERE a.support_ticket_id=t.id),'[]'::jsonb),t.revision,t.resolution_comment,extract(epoch FROM t.created_at)::bigint,extract(epoch FROM t.updated_at)::bigint,coalesce(extract(epoch FROM t.resolved_at)::bigint,0) FROM support.support_ticket t `

type scanner interface{ Scan(...any) error }
type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ticket(row scanner) (*pb.Ticket, error) {
	v := &pb.Ticket{}
	var raw []byte
	e := row.Scan(&v.Id, &v.AuthorUserId, &v.AssignedAdminId, &v.Status, &v.Subject, &v.Description, &v.TargetUserId, &v.TargetProjectId, &raw, &v.Revision, &v.ResolutionComment, &v.CreatedAtUnix, &v.UpdatedAtUnix, &v.ResolvedAtUnix)
	if e != nil {
		return nil, m.Error(e)
	}
	if e = json.Unmarshal(raw, &v.FileIds); e != nil {
		return nil, m.Error(e)
	}
	return v, nil
}
func load(ctx context.Context, q querier, id, suffix string) (*pb.Ticket, error) {
	return ticket(q.QueryRowContext(ctx, ticketSelect+`WHERE t.id=$1 `+suffix, id))
}
func (s *Service) GetTicket(ctx context.Context, q *mpb.ID) (*pb.Ticket, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	return load(ctx, s.DB, q.Id, "")
}
func (s *Service) ListTickets(ctx context.Context, q *mpb.Query) (*pb.TicketList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "status", "user_id", "assigned_admin_id", "unassigned_only", "created_from_unix", "created_to_unix", "target_user_id", "target_project_id"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Status != "" && q.Status != "open" && q.Status != "resolved" && q.Status != "declined" && q.Status != "closed" {
		return nil, m.Invalid()
	}
	if q.UnassignedOnly && q.AssignedAdminId != "" {
		return nil, m.Invalid()
	}
	where := `WHERE ($1='' OR t.status::text=$1 OR ($1='closed' AND t.status IN ('resolved','declined'))) AND ($2='' OR t.author_user_id::text=$2) AND ($3='' OR t.assigned_admin_id::text=$3) AND (NOT $4::boolean OR t.assigned_admin_id IS NULL) AND ($5='' OR position(lower($5) in lower(t.subject||' '||t.description))>0) AND ($6::bigint=0 OR t.created_at>=to_timestamp($6)) AND ($7::bigint=0 OR t.created_at<to_timestamp($7)) AND ($8='' OR t.target_user_id::text=$8) AND ($9='' OR t.target_project_id::text=$9)`
	args := []any{q.Status, q.UserId, q.AssignedAdminId, q.UnassignedOnly, q.Text, q.CreatedFromUnix, q.CreatedToUnix, q.TargetUserId, q.TargetProjectId}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.TicketList{Items: []*pb.Ticket{}}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM support.support_ticket t `+where, args...).Scan(&out.Total); e != nil {
		return nil, m.Error(e)
	}
	args = append(args, l, o)
	rows, e := tx.QueryContext(ctx, ticketSelect+where+` ORDER BY CASE WHEN t.status='open' THEN 0 ELSE 1 END,CASE WHEN t.status='open' THEN t.created_at END,CASE WHEN t.status<>'open' THEN t.resolved_at END DESC,t.id LIMIT $10 OFFSET $11`, args...)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := ticket(rows)
		if err != nil {
			return nil, err
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
func event(ctx context.Context, tx *sql.Tx, actor, action, id, comment, before, after string, revision int32, beforeAssignee, afterAssignee string) (*mpb.Event, error) {
	v := &mpb.Event{ActorAdminId: actor, Action: action, ResourceId: id, Comment: comment, PreviousStatus: before, Status: after, Revision: revision, Result: "succeeded", PreviousAssignedAdminId: beforeAssignee, AssignedAdminId: afterAssignee}
	e := tx.QueryRowContext(ctx, `INSERT INTO support.ticket_event(actor_admin_id,action,ticket_id,comment,previous_status,status,revision,previous_assigned_admin_id,assigned_admin_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid,nullif($9,'')::uuid) RETURNING id::text,extract(epoch FROM created_at)::bigint`, actor, action, id, comment, before, after, revision, beforeAssignee, afterAssignee).Scan(&v.Id, &v.CreatedAtUnix)
	return v, m.Error(e)
}
func (s *Service) UpdateTicket(ctx context.Context, q *pb.TicketChange) (*pb.Ticket, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) || q.ExpectedRevision < 1 || !m.Text(q.Comment, 4000, false) {
		return nil, m.Invalid()
	}
	if q.Action != "assign" && q.Action != "unassign" && q.Action != "resolve" && q.Action != "decline" && q.Action != "reopen" {
		return nil, m.Invalid()
	}
	if q.Action == "assign" {
		if !m.ID(q.AssignedAdminId) {
			return nil, m.Invalid()
		}
	} else if q.AssignedAdminId != "" {
		return nil, m.Invalid()
	}
	if (q.Action == "resolve" || q.Action == "decline" || q.Action == "reopen") && !m.Text(q.Comment, 4000, true) {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	v, e := load(ctx, tx, q.Id, "FOR UPDATE OF t")
	if e != nil {
		return nil, e
	}
	if v.Revision != q.ExpectedRevision {
		return nil, m.Conflict()
	}
	before := v.Status
	beforeAssignee := v.AssignedAdminId
	switch q.Action {
	case "assign", "unassign":
		if v.Status != "open" {
			return nil, m.Conflict()
		}
		v.AssignedAdminId = q.AssignedAdminId
	case "resolve":
		if v.Status != "open" {
			return nil, m.Conflict()
		}
		v.Status = "resolved"
		v.ResolutionComment = q.Comment
	case "decline":
		if v.Status != "open" {
			return nil, m.Conflict()
		}
		v.Status = "declined"
		v.ResolutionComment = q.Comment
	case "reopen":
		if v.Status == "open" {
			return nil, m.Conflict()
		}
		v.Status = "open"
		v.ResolutionComment = ""
	}
	_, e = tx.ExecContext(ctx, `UPDATE support.support_ticket SET status=$2::support.support_ticket_status,assigned_admin_id=nullif($3,'')::uuid,resolution_comment=$4,resolved_at=CASE WHEN $2::text='open' THEN NULL WHEN $5='open' THEN now() ELSE resolved_at END,revision=revision+1,updated_at=now() WHERE id=$1`, v.Id, v.Status, v.AssignedAdminId, v.ResolutionComment, before)
	if e != nil {
		return nil, m.Error(e)
	}
	if _, e = event(ctx, tx, a.ID, q.Action, v.Id, q.Comment, before, v.Status, v.Revision+1, beforeAssignee, v.AssignedAdminId); e != nil {
		return nil, e
	}
	out, e := load(ctx, tx, v.Id, "")
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}
func (s *Service) AddNote(ctx context.Context, q *pb.TicketNote) (*mpb.Event, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) || q.ExpectedRevision < 1 || !m.Text(q.Comment, 4000, true) {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	v, e := load(ctx, tx, q.Id, "FOR UPDATE OF t")
	if e != nil {
		return nil, e
	}
	if v.Revision != q.ExpectedRevision {
		return nil, m.Conflict()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE support.support_ticket SET revision=revision+1,updated_at=now() WHERE id=$1`, v.Id); e != nil {
		return nil, m.Error(e)
	}
	out, e := event(ctx, tx, a.ID, "note", v.Id, q.Comment, v.Status, v.Status, v.Revision+1, v.AssignedAdminId, v.AssignedAdminId)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}
func (s *Service) TicketHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	return m.History(ctx, s.DB, q, `SELECT id::text,actor_admin_id::text,action,ticket_id::text resource_id,comment,previous_status,status,revision,extract(epoch FROM created_at)::bigint created_at_unix,coalesce(previous_assigned_admin_id::text,'') previous_assigned_admin_id,coalesce(assigned_admin_id::text,'') assigned_admin_id FROM support.ticket_event`)
}
func (s *Service) DownloadAttachment(ctx context.Context, q *pb.AttachmentQuery) (*mpb.Download, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.TicketId) || !m.ID(q.FileId) {
		return nil, m.Invalid()
	}
	var owner string
	e := s.DB.QueryRowContext(ctx, `SELECT t.author_user_id::text FROM support.support_ticket t JOIN support.support_ticket_attachment a ON a.support_ticket_id=t.id WHERE t.id=$1 AND a.file_id=$2`, q.TicketId, q.FileId).Scan(&owner)
	if e != nil {
		return nil, m.Error(e)
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, e := s.Media.CreateDownloadURL(call, &mediapb.CreateDownloadURLRequest{UserId: owner, FileId: q.FileId})
	if e != nil {
		return nil, e
	}
	return &mpb.Download{DownloadUrl: res.DownloadUrl, ExpiresAtUnix: res.ExpiresAtUnix}, nil
}
func (s *Service) GetStatistics(ctx context.Context, q *mpb.StatisticsQuery) (*mpb.Statistics, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	return m.Statistics(ctx, s.DB, q, []m.StatQuery{
		{Category: "tickets_by_status", SQL: `SELECT status::text,count(*) FROM support.support_ticket GROUP BY status`},
		{Category: "open_by_assignment", SQL: `SELECT CASE WHEN assigned_admin_id IS NULL THEN 'unassigned' ELSE 'assigned' END,count(*) FROM support.support_ticket WHERE status='open' GROUP BY 1`},
		{Category: "tickets_by_target", SQL: `SELECT CASE WHEN target_user_id IS NOT NULL THEN 'user' ELSE 'project' END,count(*) FROM support.support_ticket GROUP BY 1`},
		{Category: "tickets_created", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM support.support_ticket WHERE created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
		{Category: "resolutions", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM support.ticket_event WHERE action IN ('resolve','decline') AND created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
	})
}
