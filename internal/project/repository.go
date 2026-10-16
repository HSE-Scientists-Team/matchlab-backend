package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrMissing   = errors.New("project not found")
	ErrConflict  = errors.New("state conflict")
	ErrDenied    = errors.New("forbidden")
	errUnchanged = errors.New("unchanged")
)

type Repository struct{ DB *sql.DB }
type queries interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func dbError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMissing
	}
	var e *pgconn.PgError
	if errors.As(err, &e) {
		if e.Code == "23505" {
			return ErrConflict
		}
		if e.Code == "23503" {
			return ErrMissing
		}
	}
	return err
}
func read(ctx context.Context, q queries, id string, lock bool) (*pb.Project, error) {
	p := &pb.Project{Details: &pb.ProjectInput{}, Roles: []*pb.Role{}, Members: []*pb.Member{}}
	var raw []byte
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, `SELECT id::text,owner_user_id::text,data,status,revision,extract(epoch FROM created_at)::bigint,coalesce(extract(epoch FROM completed_at)::bigint,0),moderation_hidden FROM projects.project WHERE id=$1`+suffix, id).Scan(&p.Id, &p.OwnerUserId, &raw, &p.Status, &p.Revision, &p.CreatedAtUnix, &p.CompletedAtUnix, &p.ModerationHidden)
	if err != nil {
		return nil, dbError(err)
	}
	if err = json.Unmarshal(raw, p.Details); err != nil {
		return nil, err
	}
	p.Details.Id = p.Id
	p.Details.ExpectedRevision = p.Revision
	rows, err := q.QueryContext(ctx, `SELECT r.id::text,r.title,r.description,r.skills,r.slots,(SELECT count(*) FROM projects.member m WHERE m.project_id=r.project_id AND m.role_id=r.id AND m.status='active') FROM projects.role r WHERE project_id=$1 AND r.deleted_at IS NULL ORDER BY r.id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		r := &pb.Role{}
		var skills []byte
		if err = rows.Scan(&r.Id, &r.Title, &r.Description, &skills, &r.Slots, &r.Occupied); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(skills, &r.Skills); err != nil {
			rows.Close()
			return nil, err
		}
		p.Roles = append(p.Roles, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT m.user_id::text,coalesce(m.role_id::text,''),coalesce(r.title,'initiator'),m.status FROM projects.member m LEFT JOIN projects.role r ON r.id=m.role_id WHERE m.project_id=$1 ORDER BY m.user_id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		m := &pb.Member{}
		if err = rows.Scan(&m.UserId, &m.RoleId, &m.RoleTitle, &m.Status); err != nil {
			rows.Close()
			return nil, err
		}
		p.Members = append(p.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM projects.join_request WHERE project_id=$1 AND status='pending'`, id).Scan(&p.PendingJoinCount)
	return p, err
}
func (r *Repository) Get(ctx context.Context, id string) (*pb.Project, error) {
	return read(ctx, r.DB, id, false)
}
func snapshot(ctx context.Context, tx *sql.Tx, p *pb.Project) error {
	state := "active"
	if p.Status == "completed" {
		state = "completed"
	}
	if p.Status == "cancelled" {
		state = "cancelled"
	}
	v := &upb.ProjectContext{ProjectId: p.Id, Title: p.Details.Title, InitiatorUserId: p.OwnerUserId, CompletedAtUnix: p.CompletedAtUnix, State: state, SourceRevision: int64(p.Revision)}
	for _, m := range p.Members {
		if m.Status == "active" {
			v.ParticipantUserIds = append(v.ParticipantUserIds, m.UserId)
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects.context_outbox(project_id,revision,payload) VALUES($1,$2,$3)`, p.Id, p.Revision, raw)
	return err
}
func insertRole(ctx context.Context, tx *sql.Tx, project string, input *pb.RoleInput) error {
	skills, err := json.Marshal(input.Skills)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects.role(project_id,title,description,skills,slots) VALUES($1,$2,$3,$4,$5)`, project, input.Title, input.Description, skills, input.Slots)
	return err
}
func (r *Repository) Create(ctx context.Context, owner string, input *pb.ProjectInput) (*pb.Project, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	roles := input.Roles
	input.Roles = nil
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO projects.project(owner_user_id,data) VALUES($1,$2) RETURNING id::text`, owner, raw).Scan(&id)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects.member(project_id,user_id) VALUES($1,$2)`, id, owner); err != nil {
		return nil, err
	}
	for _, role := range roles {
		if err = insertRole(ctx, tx, id, role); err != nil {
			return nil, err
		}
	}
	p, err := read(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err = snapshot(ctx, tx, p); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}
func (r *Repository) Change(ctx context.Context, id string, fn func(*sql.Tx, *pb.Project) error) (*pb.Project, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := read(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if err = fn(tx, p); err != nil {
		if errors.Is(err, errUnchanged) {
			return p, nil
		}
		return nil, dbError(err)
	}
	p.Revision++
	raw, err := json.Marshal(p.Details)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE projects.project SET data=$2,status=$3,revision=$4,moderation_hidden=$5,completed_at=CASE WHEN $6::bigint=0 THEN NULL ELSE to_timestamp($6) END,updated_at=now() WHERE id=$1`, id, raw, p.Status, p.Revision, p.ModerationHidden, p.CompletedAtUnix)
	if err != nil {
		return nil, err
	}
	p, err = read(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err = snapshot(ctx, tx, p); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

const joinSelect = `SELECT id::text,project_id::text,role_id::text,candidate_user_id::text,type,status,message,extract(epoch FROM created_at)::bigint FROM projects.join_request `

func scanJoin(row interface{ Scan(...any) error }) (*pb.JoinRequest, error) {
	j := &pb.JoinRequest{}
	err := row.Scan(&j.Id, &j.ProjectId, &j.RoleId, &j.CandidateUserId, &j.Type, &j.Status, &j.Message, &j.CreatedAtUnix)
	return j, dbError(err)
}
func (r *Repository) Join(ctx context.Context, id string) (*pb.JoinRequest, error) {
	return scanJoin(r.DB.QueryRowContext(ctx, joinSelect+`WHERE id=$1`, id))
}

// Public catalog predicate is shared by ordinary and random candidate queries.
func (r *Repository) List(ctx context.Context, q *pb.ProjectQuery, viewer string, showParticipation bool, limit, offset int32, admin, hiddenOnly bool) (*pb.ProjectList, error) {
	tagValues := q.Tags
	if tagValues == nil {
		tagValues = []string{}
	}
	tags, _ := json.Marshal(tagValues)
	where := ` WHERE ($1::boolean OR (NOT p.moderation_hidden AND p.status IN ('recruiting','active','completed')) OR ($2<>'' AND (p.owner_user_id::text=$2 OR EXISTS(SELECT 1 FROM projects.member m WHERE m.project_id=p.id AND m.user_id::text=$2 AND m.status='active')))) AND ($3='' OR p.data->>'type'=$3) AND ($4='' OR lower(p.data->>'field')=lower($4)) AND ($5='' OR p.status=$5) AND ($6='' OR to_tsvector('simple',(p.data->>'title')||' '||(p.data->>'problem')||' '||(p.data->>'goal')||' '||coalesce((p.data->'tags')::text,'')) @@ plainto_tsquery('simple',$6)) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text($7::jsonb) wanted WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(p.data->'tags','[]'::jsonb)) owned WHERE lower(owned)=lower(wanted))) AND ($8='' OR ($8='owner' AND p.owner_user_id::text=$2) OR ($8='participant' AND p.owner_user_id::text<>$2 AND EXISTS(SELECT 1 FROM projects.member m WHERE m.project_id=p.id AND m.user_id::text=$2 AND m.status='active'))) AND ($9='' OR EXISTS(SELECT 1 FROM projects.member m WHERE m.project_id=p.id AND m.user_id::text=$9 AND m.status='active' AND ($10::boolean OR $9=$2 OR p.owner_user_id::text=$2))) AND ($11='' OR ($11='up_to_3m' AND (p.data->>'planned_end')::date-(p.data->>'planned_start')::date<=92) OR ($11='3_to_6m' AND (p.data->>'planned_end')::date-(p.data->>'planned_start')::date>92 AND (p.data->>'planned_end')::date-(p.data->>'planned_start')::date<=183) OR ($11='over_6m' AND (p.data->>'planned_end')::date-(p.data->>'planned_start')::date>183))`
	if hiddenOnly {
		where += ` AND p.moderation_hidden`
	}
	if q.RandomOrder {
		where += ` AND NOT p.moderation_hidden AND p.status='recruiting' AND p.owner_user_id::text<>$2 AND NOT EXISTS(SELECT 1 FROM projects.member m WHERE m.project_id=p.id AND m.user_id::text=$2 AND m.status='active')`
	}
	args := []any{admin, viewer, q.Type, q.Field, q.Status, q.Text, string(tags), q.Mine, q.ProfileUserId, showParticipation, q.Duration}
	var total int64
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*) FROM projects.project p`+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	order := ` ORDER BY p.created_at DESC,p.id`
	if q.RandomOrder {
		order = ` ORDER BY random(),p.id`
	}
	args = append(args, limit, offset)
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id::text FROM projects.project p`+where+order+` LIMIT $12 OFFSET $13`, args...)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	res := &pb.ProjectList{Items: []*pb.Project{}, Total: total}
	for _, id := range ids {
		p, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, p)
	}
	return res, nil
}
func (r *Repository) Joins(ctx context.Context, q *pb.JoinQuery, viewer string, limit, offset int32) (*pb.JoinList, error) {
	where := ` WHERE (($1='' AND candidate_user_id::text=$2) OR project_id::text=$1) AND ($3='' OR role_id::text=$3) AND ($4='' OR status=$4)`
	args := []any{q.ProjectId, viewer, q.RoleId, q.Status}
	res := &pb.JoinList{Items: []*pb.JoinRequest{}}
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*) FROM projects.join_request`+where, args...).Scan(&res.Total); err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	rows, err := r.DB.QueryContext(ctx, joinSelect+where+` ORDER BY created_at DESC,id LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		j, err := scanJoin(rows)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, j)
	}
	return res, rows.Err()
}
