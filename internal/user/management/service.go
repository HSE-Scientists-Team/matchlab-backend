// Package management owns administrative operations on User data.
package management

import (
	"context"
	"database/sql"
	"fmt"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"

	"strings"
)

type Service struct {
	pb.UnimplementedUserManagementServiceServer
	DB *sql.DB
}

func actor(ctx context.Context) (adminauth.Actor, error) { return adminauth.Verify(ctx, "user") }

type scanner interface{ Scan(...any) error }

const userSelect = `SELECT a.id::text,a.email,coalesce(p.data->>'display_name',''),a.status::text,EXISTS(SELECT 1 FROM users.user_email e WHERE e.user_id=a.id),a.revision,coalesce(a.status_comment,''),extract(epoch FROM a.created_at)::bigint,coalesce(extract(epoch FROM a.last_login_at)::bigint,0),coalesce(a.status_changed_by_admin_id::text,''),coalesce(extract(epoch FROM a.status_changed_at)::bigint,0) FROM users.user_account a LEFT JOIN users.user_profile p ON p.user_id=a.id `

func user(row scanner) (*pb.ManagedUser, error) {
	v := &pb.ManagedUser{}
	e := row.Scan(&v.Id, &v.Email, &v.DisplayName, &v.Status, &v.EmailVerified, &v.Revision, &v.StatusComment, &v.CreatedAtUnix, &v.LastLoginAtUnix, &v.StatusChangedByAdminId, &v.StatusChangedAtUnix)
	return v, m.Error(e)
}
func (s *Service) GetUser(ctx context.Context, q *mpb.ID) (*pb.ManagedUser, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	return user(s.DB.QueryRowContext(ctx, userSelect+`WHERE a.id=$1`, q.Id))
}
func (s *Service) ListUsers(ctx context.Context, q *mpb.Query) (*pb.ManagedUserList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "status", "user_id", "created_from_unix", "created_to_unix"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Status != "" && q.Status != "active" && q.Status != "blocked" && q.Status != "deleted" {
		return nil, m.Invalid()
	}
	args := []any{q.Text, q.Status, q.UserId, q.CreatedFromUnix, q.CreatedToUnix}
	where := `WHERE ($1='' OR position(lower($1) in lower(a.email||' '||coalesce(p.data->>'display_name','')))>0) AND ($2='' OR a.status::text=$2) AND ($3='' OR a.id::text=$3) AND ($4::bigint=0 OR a.created_at>=to_timestamp($4)) AND ($5::bigint=0 OR a.created_at<to_timestamp($5))`
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.ManagedUserList{Items: []*pb.ManagedUser{}}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users.user_account a LEFT JOIN users.user_profile p ON p.user_id=a.id `+where, args...).Scan(&out.Total)
	if e != nil {
		return nil, m.Error(e)
	}
	args = append(args, l, o)
	rows, e := tx.QueryContext(ctx, userSelect+where+` ORDER BY a.created_at DESC,a.id DESC LIMIT $6 OFFSET $7`, args...)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := user(rows)
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
func (s *Service) SetUserStatus(ctx context.Context, q *pb.UserStatusChange) (*pb.ManagedUser, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) || (q.Status != "active" && q.Status != "blocked") || q.ExpectedRevision < 1 || !m.Text(q.Reason, 2000, true) {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	var before string
	var rev int32
	e = tx.QueryRowContext(ctx, `SELECT status::text,revision FROM users.user_account WHERE id=$1 FOR UPDATE`, q.Id).Scan(&before, &rev)
	if e != nil {
		return nil, m.Error(e)
	}
	if before == "deleted" || before == q.Status || rev != q.ExpectedRevision {
		return nil, m.Conflict()
	}
	_, e = tx.ExecContext(ctx, `UPDATE users.user_account SET status=$2,revision=revision+1,status_changed_by_admin_id=$3,status_changed_by_user_id=NULL,status_changed_at=now(),status_comment=$4,updated_at=now() WHERE id=$1`, q.Id, q.Status, a.ID, q.Reason)
	if e != nil {
		return nil, m.Error(e)
	}
	if e = m.Event(ctx, tx, "users.admin_event", a.ID, "user.status", q.Id, q.Reason, before, q.Status, rev+1); e != nil {
		return nil, m.Error(e)
	}
	out, e := user(tx.QueryRowContext(ctx, userSelect+`WHERE a.id=$1`, q.Id))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}

const eventSelect = `SELECT id::text,actor_admin_id::text,action,resource_id::text,comment,previous_status,status,revision,extract(epoch FROM created_at)::bigint created_at_unix,''::text previous_assigned_admin_id,''::text assigned_admin_id FROM users.admin_event`

func (s *Service) UserHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	return m.History(ctx, s.DB, q, eventSelect+` WHERE action='user.status'`)
}
func (s *Service) DomainHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	return m.History(ctx, s.DB, q, eventSelect+` WHERE action='domain.save'`)
}

const domainSelect = `SELECT id::text,domain,organization_name,is_active,revision,extract(epoch FROM created_at)::bigint,extract(epoch FROM updated_at)::bigint FROM users.trusted_email_domain `

func domain(row scanner) (*pb.EmailDomain, error) {
	v := &pb.EmailDomain{}
	e := row.Scan(&v.Id, &v.Domain, &v.OrganizationName, &v.IsActive, &v.Revision, &v.CreatedAtUnix, &v.UpdatedAtUnix)
	return v, m.Error(e)
}
func validDomain(v string) bool {
	if strings.HasPrefix(v, "*.") {
		v = v[2:]
	}
	if len(v) > 253 || !strings.Contains(v, ".") {
		return false
	}
	for _, label := range strings.Split(v, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func (s *Service) ListDomains(ctx context.Context, q *mpb.Query) (*pb.DomainList, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "status"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Status != "" && q.Status != "active" && q.Status != "inactive" {
		return nil, m.Invalid()
	}
	where := `WHERE ($1='' OR position(lower($1) in lower(domain||' '||organization_name))>0) AND ($2='' OR is_active=($2='active'))`
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.DomainList{Items: []*pb.EmailDomain{}}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users.trusted_email_domain `+where, q.Text, q.Status).Scan(&out.Total)
	if e != nil {
		return nil, m.Error(e)
	}
	rows, e := tx.QueryContext(ctx, domainSelect+where+` ORDER BY domain,id LIMIT $3 OFFSET $4`, q.Text, q.Status, l, o)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := domain(rows)
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
func (s *Service) SaveDomain(ctx context.Context, q *pb.DomainInput) (*pb.EmailDomain, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	if e = adminauth.RequireAdministrator(a); e != nil {
		return nil, e
	}
	q.Domain = strings.ToLower(strings.TrimSpace(q.Domain))
	q.OrganizationName = strings.TrimSpace(q.OrganizationName)
	if !validDomain(q.Domain) || !m.Text(q.OrganizationName, 255, true) || !m.Text(q.Reason, 2000, true) || (q.Id != "" && (!m.ID(q.Id) || q.ExpectedRevision < 1)) || (q.Id == "" && q.ExpectedRevision != 0) {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	var id string
	before := ""
	var result *pb.EmailDomain
	if q.Id == "" {
		e = tx.QueryRowContext(ctx, `INSERT INTO users.trusted_email_domain(domain,organization_name,is_active) VALUES($1,$2,$3) RETURNING id::text`, q.Domain, q.OrganizationName, q.IsActive).Scan(&id)
	} else {
		var revision int32
		e = tx.QueryRowContext(ctx, `SELECT domain||':'||is_active::text,revision FROM users.trusted_email_domain WHERE id=$1 FOR UPDATE`, q.Id).Scan(&before, &revision)
		if e == nil && revision != q.ExpectedRevision {
			return nil, m.Conflict()
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE users.trusted_email_domain SET domain=$2,organization_name=$3,is_active=$4,revision=revision+1,updated_at=now() WHERE id=$1`, q.Id, q.Domain, q.OrganizationName, q.IsActive)
		}
		id = q.Id
	}
	if e != nil {
		return nil, m.Error(e)
	}
	result, e = domain(tx.QueryRowContext(ctx, domainSelect+`WHERE id=$1`, id))
	if e != nil {
		return nil, e
	}
	if e = m.Event(ctx, tx, "users.admin_event", a.ID, "domain.save", id, q.Reason, before, fmt.Sprintf("%s:%t", q.Domain, q.IsActive), result.Revision); e != nil {
		return nil, m.Error(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return result, nil
}
func (s *Service) GetStatistics(ctx context.Context, q *mpb.StatisticsQuery) (*mpb.Statistics, error) {
	if _, e := actor(ctx); e != nil {
		return nil, e
	}
	return m.Statistics(ctx, s.DB, q, []m.StatQuery{
		{Category: "users_by_status", SQL: `SELECT status::text,count(*) FROM users.user_account GROUP BY status`},
		{Category: "achievements_by_status", SQL: `SELECT status,count(*) FROM users.achievement WHERE deleted_at IS NULL GROUP BY status`},
		{Category: "achievements_by_kind", SQL: `SELECT kind,count(*) FROM users.achievement WHERE deleted_at IS NULL GROUP BY kind`},
		{Category: "domains_by_status", SQL: `SELECT CASE WHEN is_active THEN 'active' ELSE 'inactive' END,count(*) FROM users.trusted_email_domain GROUP BY is_active`},
		{Category: "users_by_email_domain", SQL: `SELECT split_part(email,'@',2),count(*) FROM users.user_account GROUP BY 1`},
		{Category: "registrations_by_domain", Period: true, SQL: `SELECT split_part(email,'@',2),count(*) FROM users.user_account WHERE created_at >=$1 AND created_at<$2 GROUP BY 1`},
		{Category: "registrations", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM users.user_account WHERE created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
		{Category: "achievement_submissions", Period: true, Daily: true, SQL: `SELECT to_char(submitted_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM users.achievement WHERE submitted_at >=$1 AND submitted_at<$2 GROUP BY 1 ORDER BY 1`},
		{Category: "achievement_approvals", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM users.achievement_decision WHERE approved AND created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
		{Category: "achievement_rejections", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM users.achievement_decision WHERE NOT approved AND created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`},
	})
}
