package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	managementv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/rpcmeta"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"time"
	"unicode/utf8"
)

type Users interface {
	GetIdentity(context.Context, *upb.Empty, ...grpc.CallOption) (*upb.Identity, error)
	GetProfile(context.Context, *upb.ProfileQuery, ...grpc.CallOption) (*upb.Profile, error)
	CheckInteraction(context.Context, *upb.InteractionQuery, ...grpc.CallOption) (*upb.InteractionResult, error)
	ParticipationVisibility(context.Context, *upb.UserID, ...grpc.CallOption) (*upb.ParticipationFlags, error)
	RecordProjectContext(context.Context, *upb.ProjectContext, ...grpc.CallOption) (*upb.Empty, error)
}
type Service struct {
	pb.UnimplementedProjectServiceServer
	Repo      *Repository
	Users     Users
	SyncToken string
}

func validID(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id != uuid.Nil && id.String() == v
}
func invalid() error {
	return status.Error(codes.InvalidArgument, "некорректные поля запроса")
}
func failure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrMissing):
		return status.Error(codes.NotFound, "объект не найден")
	case errors.Is(err, ErrDenied):
		return status.Error(codes.PermissionDenied, "операция запрещена")
	case errors.Is(err, ErrConflict):
		return status.Error(codes.Aborted, "состояние изменилось или операция недоступна")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "истёк срок запроса")
	}
	return status.Error(codes.Internal, "ошибка сервиса проектов")
}

func (s *Service) actor(ctx context.Context, required, admin bool) (string, error) {
	if admin {
		a, e := adminauth.Verify(ctx, "project")
		return a.ID, e
	}
	if !required && !admin && len(metadata.ValueFromIncomingContext(ctx, "authorization")) == 0 {
		return "", nil
	}
	call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
	defer cancel()
	p, err := s.Users.GetIdentity(call, &upb.Empty{})
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied:
			return "", err
		default:
			return "", status.Error(codes.Unavailable, "User недоступен")
		}
	}
	if !validID(p.GetUserId()) {
		return "", status.Error(codes.Unauthenticated, "недействительный сеанс")
	}
	if admin && p.Role != "moderator" && p.Role != "administrator" {
		return "", status.Error(codes.PermissionDenied, "требуется модератор")
	}
	return p.UserId, nil
}
func page(p *pb.Page) (int32, int32, error) {
	limit, offset := p.GetLimit(), p.GetOffset()
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 10000 {
		return 0, 0, invalid()
	}
	return limit, offset, nil
}
func text(v string, max int, required bool) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= max && (!required || strings.TrimSpace(v) != "")
}
func tags(v []string) bool {
	if len(v) > 30 {
		return false
	}
	seen := map[string]bool{}
	for i, x := range v {
		x = strings.TrimSpace(x)
		k := strings.ToLower(x)
		if !text(x, 80, true) || seen[k] {
			return false
		}
		seen[k] = true
		v[i] = x
	}
	return true
}
func normalizeDate(v string, end bool) (string, error) {
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		t, err = time.Parse("2006-01", v)
		if err == nil && end {
			t = t.AddDate(0, 1, -1)
		}
	}
	if err != nil || t.Year() < 1 {
		return "", invalid()
	}
	return t.Format("2006-01-02"), nil
}
func validateRole(p *pb.RoleInput) error {
	if p.Skills == nil {
		p.Skills = []string{}
	}
	p.Title = strings.TrimSpace(p.Title)
	if !text(p.Title, 255, true) || !text(p.Description, 10000, false) || !tags(p.Skills) || p.Slots < 1 || p.Slots > 100 {
		return invalid()
	}
	return nil
}
func validate(p *pb.ProjectInput, create bool) error {
	p.Title = strings.TrimSpace(p.Title)
	if !text(p.Title, 255, true) || (p.Type != "scientific" && p.Type != "practical") || !text(p.Field, 120, true) || !text(p.Problem, 10000, true) || !text(p.Goal, 10000, true) || !tags(p.Tags) {
		return invalid()
	}
	var err error
	p.PlannedStart, err = normalizeDate(p.PlannedStart, false)
	if err != nil {
		return err
	}
	p.PlannedEnd, err = normalizeDate(p.PlannedEnd, true)
	if err != nil || p.PlannedEnd < p.PlannedStart {
		return invalid()
	}
	if create {
		if p.Id != "" || p.ExpectedRevision != 0 || len(p.Roles) < 1 || len(p.Roles) > 30 {
			return invalid()
		}
		for _, r := range p.Roles {
			if r == nil || r.Id != "" || r.ProjectId != "" || r.ExpectedRevision != 0 {
				return invalid()
			}
			if err = validateRole(r); err != nil {
				return err
			}
		}
	} else if !validID(p.Id) || p.ExpectedRevision < 1 || len(p.Roles) != 0 {
		return invalid()
	}
	return nil
}
func member(p *pb.Project, id string) bool {
	for _, m := range p.Members {
		if m.UserId == id && m.Status == "active" {
			return true
		}
	}
	return false
}
func visible(p *pb.Project, id string) bool {
	return (id != "" && (p.OwnerUserId == id || member(p, id))) || (!p.ModerationHidden && (p.Status == "recruiting" || p.Status == "active" || p.Status == "completed"))
}
func sanitize(p *pb.Project, id string) *pb.Project {
	if p.OwnerUserId != id {
		p.PendingJoinCount = 0
	}
	members := []*pb.Member{}
	if p.OwnerUserId == id || member(p, id) {
		for _, m := range p.Members {
			if m.Status == "active" {
				members = append(members, m)
			}
		}
	}
	p.Members = members
	return p
}
func owner(p *pb.Project, id string, rev int32) error {
	if p.OwnerUserId != id {
		return ErrDenied
	}
	if rev > 0 && p.Revision != rev {
		return ErrConflict
	}
	if p.Status == "completed" || p.Status == "cancelled" {
		return ErrConflict
	}
	return nil
}
func (s *Service) Get(ctx context.Context, q *pb.ProjectID) (*pb.Project, error) {
	id, err := s.actor(ctx, false, false)
	if err != nil {
		return nil, err
	}
	if !validID(q.Id) {
		return nil, invalid()
	}
	p, err := s.Repo.Get(ctx, q.Id)
	if err != nil {
		return nil, failure(err)
	}
	if !visible(p, id) {
		return nil, status.Error(codes.NotFound, "проект не найден")
	}
	return sanitize(p, id), nil
}
func (s *Service) Create(ctx context.Context, q *pb.ProjectInput) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if err = validate(q, true); err != nil {
		return nil, err
	}
	p, err := s.Repo.Create(ctx, id, q)
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) Update(ctx context.Context, q *pb.ProjectInput) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if err = validate(q, false); err != nil {
		return nil, err
	}
	p, err := s.Repo.Change(ctx, q.Id, func(_ *sql.Tx, p *pb.Project) error {
		if err := owner(p, id, q.ExpectedRevision); err != nil {
			return err
		}
		p.Details = q
		return nil
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) List(ctx context.Context, q *pb.ProjectQuery) (*pb.ProjectList, error) {
	id, err := s.actor(ctx, q.Mine != "", false)
	if err != nil {
		return nil, err
	}
	limit, offset, err := page(q.Page)
	if err != nil {
		return nil, err
	}
	if !text(q.Text, 255, false) || !text(q.Field, 120, false) || !tags(q.Tags) || (q.Type != "" && q.Type != "scientific" && q.Type != "practical") || (q.Mine != "" && q.Mine != "owner" && q.Mine != "participant") || (q.Duration != "" && q.Duration != "up_to_3m" && q.Duration != "3_to_6m" && q.Duration != "over_6m") {
		return nil, invalid()
	}
	if q.Status != "" && q.Status != "recruiting" && q.Status != "active" && q.Status != "completed" && q.Status != "cancelled" && q.Status != "unpublished" {
		return nil, invalid()
	}
	show := false
	if q.ProfileUserId != "" {
		if !validID(q.ProfileUserId) {
			return nil, invalid()
		}
		call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
		flags, e := s.Users.ParticipationVisibility(call, &upb.UserID{UserId: q.ProfileUserId})
		cancel()
		if e != nil {
			return nil, e
		}
		show = flags.ShowParticipatingProjects
	}
	res, err := s.Repo.List(ctx, q, id, show, limit, offset, false, false)
	if err != nil {
		return nil, failure(err)
	}
	items := []*pb.Project{}
	for _, p := range res.Items {
		if visible(p, id) {
			items = append(items, sanitize(p, id))
		}
	}
	res.Items = items
	return res, nil
}
func (s *Service) SaveRole(ctx context.Context, q *pb.RoleInput) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(q.ProjectId) || (q.Id != "" && !validID(q.Id)) || q.ExpectedRevision < 1 {
		return nil, invalid()
	}
	if err = validateRole(q); err != nil {
		return nil, err
	}
	p, err := s.Repo.Change(ctx, q.ProjectId, func(tx *sql.Tx, p *pb.Project) error {
		if e := owner(p, id, q.ExpectedRevision); e != nil {
			return e
		}
		if p.Status != "recruiting" && p.Status != "unpublished" {
			return ErrConflict
		}
		if q.Id == "" {
			if len(p.Roles) >= 30 {
				return ErrConflict
			}
			return insertRole(ctx, tx, p.Id, q)
		}
		var found *pb.Role
		for _, r := range p.Roles {
			if r.Id == q.Id {
				found = r
			}
		}
		if found == nil {
			return ErrMissing
		}
		if found.Occupied > q.Slots {
			return ErrConflict
		}
		skills, _ := json.Marshal(q.Skills)
		_, e := tx.ExecContext(ctx, `UPDATE projects.role SET title=$3,description=$4,skills=$5,slots=$6 WHERE project_id=$1 AND id=$2`, p.Id, q.Id, q.Title, q.Description, skills, q.Slots)
		return e
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) DeleteRole(ctx context.Context, q *pb.RoleID) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(q.ProjectId) || !validID(q.Id) || q.ExpectedRevision < 1 {
		return nil, invalid()
	}
	p, err := s.Repo.Change(ctx, q.ProjectId, func(tx *sql.Tx, p *pb.Project) error {
		if e := owner(p, id, q.ExpectedRevision); e != nil {
			return e
		}
		if p.Status != "recruiting" && p.Status != "unpublished" {
			return ErrConflict
		}
		var found *pb.Role
		for _, r := range p.Roles {
			if r.Id == q.Id {
				found = r
			}
		}
		if found == nil {
			return ErrMissing
		}
		if found.Occupied > 0 || len(p.Roles) <= 1 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, `UPDATE projects.join_request SET status='cancelled' WHERE role_id=$1 AND status='pending'`, q.Id); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE projects.role SET deleted_at=now() WHERE project_id=$1 AND id=$2`, p.Id, q.Id)
		return e
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) Transition(ctx context.Context, q *pb.TransitionRequest) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(q.Id) || q.ExpectedRevision < 1 {
		return nil, invalid()
	}
	allowed := map[string]map[string]string{"publish": {"unpublished": "recruiting"}, "unpublish": {"recruiting": "unpublished"}, "start": {"recruiting": "active"}, "complete": {"active": "completed"}, "cancel": {"recruiting": "cancelled", "unpublished": "cancelled", "active": "cancelled"}}
	if allowed[q.Action] == nil {
		return nil, invalid()
	}
	p, err := s.Repo.Change(ctx, q.Id, func(tx *sql.Tx, p *pb.Project) error {
		if e := owner(p, id, q.ExpectedRevision); e != nil {
			return e
		}
		next := allowed[q.Action][p.Status]
		if next == "" || (p.ModerationHidden && (q.Action == "publish" || q.Action == "start")) {
			return ErrConflict
		}
		p.Status = next
		if next == "completed" {
			p.CompletedAtUnix = time.Now().Unix()
		}
		if next == "active" || next == "completed" || next == "cancelled" {
			_, e := tx.ExecContext(ctx, `UPDATE projects.join_request SET status='cancelled' WHERE project_id=$1 AND status='pending'`, p.Id)
			return e
		}
		return nil
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) checkBlock(ctx context.Context, a, b string) error {
	call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
	defer cancel()
	res, err := s.Users.CheckInteraction(call, &upb.InteractionQuery{FromUserId: a, ToUserId: b})
	if err != nil {
		return err
	}
	if res.Blocked {
		return status.Error(codes.PermissionDenied, "пользователь заблокирован")
	}
	return nil
}
func (s *Service) join(ctx context.Context, q *pb.JoinInput, invite bool) (*pb.JoinRequest, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(q.ProjectId) || !validID(q.RoleId) || !text(q.Message, 2000, false) {
		return nil, invalid()
	}
	candidate := id
	if invite {
		candidate = q.CandidateUserId
		if !validID(candidate) {
			return nil, invalid()
		}
		call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
		_, err = s.Users.GetProfile(call, &upb.ProfileQuery{UserId: candidate})
		cancel()
		if err != nil {
			return nil, err
		}
	} else if q.CandidateUserId != "" {
		return nil, invalid()
	}
	var result *pb.JoinRequest
	_, err = s.Repo.Change(ctx, q.ProjectId, func(tx *sql.Tx, p *pb.Project) error {
		if p.ModerationHidden || p.Status != "recruiting" {
			return ErrConflict
		}
		if invite && p.OwnerUserId != id {
			return ErrDenied
		}
		if p.OwnerUserId == candidate || member(p, candidate) {
			return ErrConflict
		}
		if e := s.checkBlock(ctx, p.OwnerUserId, candidate); e != nil {
			return e
		}
		var role *pb.Role
		for _, r := range p.Roles {
			if r.Id == q.RoleId {
				role = r
			}
		}
		if role == nil {
			return ErrMissing
		}
		if role.Occupied >= role.Slots {
			return ErrConflict
		}
		kind := "application"
		if invite {
			kind = "invitation"
		}
		var e error
		result, e = scanJoin(tx.QueryRowContext(ctx, `INSERT INTO projects.join_request(project_id,role_id,candidate_user_id,type,message) VALUES($1,$2,$3,$4,$5) RETURNING id::text,project_id::text,role_id::text,candidate_user_id::text,type,status,message,extract(epoch FROM created_at)::bigint`, p.Id, q.RoleId, candidate, kind, q.Message))
		return e
	})
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, failure(err)
	}
	return result, nil
}
func (s *Service) Apply(ctx context.Context, q *pb.JoinInput) (*pb.JoinRequest, error) {
	return s.join(ctx, q, false)
}
func (s *Service) Invite(ctx context.Context, q *pb.JoinInput) (*pb.JoinRequest, error) {
	return s.join(ctx, q, true)
}
func (s *Service) DecideJoin(ctx context.Context, q *pb.JoinDecision) (*pb.JoinRequest, error) {
	return s.decide(ctx, q.Id, q.Accept, false)
}
func (s *Service) CancelJoin(ctx context.Context, q *pb.JoinID) (*pb.JoinRequest, error) {
	return s.decide(ctx, q.Id, false, true)
}
func (s *Service) decide(ctx context.Context, key string, accept, cancel bool) (*pb.JoinRequest, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(key) {
		return nil, invalid()
	}
	j, err := s.Repo.Join(ctx, key)
	if err != nil {
		return nil, failure(err)
	}
	var result *pb.JoinRequest
	_, err = s.Repo.Change(ctx, j.ProjectId, func(tx *sql.Tx, p *pb.Project) error {
		j, e := scanJoin(tx.QueryRowContext(ctx, joinSelect+`WHERE id=$1 FOR UPDATE`, key))
		if e != nil {
			return e
		}
		if cancel {
			if (j.Type == "application" && j.CandidateUserId != id) || (j.Type == "invitation" && p.OwnerUserId != id) {
				return ErrDenied
			}
		} else if (j.Type == "application" && p.OwnerUserId != id) || (j.Type == "invitation" && j.CandidateUserId != id) {
			return ErrDenied
		}
		if j.Status != "pending" {
			return ErrConflict
		}
		state := "rejected"
		if cancel {
			state = "cancelled"
		}
		if accept {
			if p.Status != "recruiting" || p.ModerationHidden || member(p, j.CandidateUserId) {
				return ErrConflict
			}
			if e = s.checkBlock(ctx, p.OwnerUserId, j.CandidateUserId); e != nil {
				return e
			}
			var role *pb.Role
			for _, r := range p.Roles {
				if r.Id == j.RoleId {
					role = r
				}
			}
			if role == nil || role.Occupied >= role.Slots {
				return ErrConflict
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO projects.member(project_id,user_id,role_id) VALUES($1,$2,$3) ON CONFLICT(project_id,user_id) DO UPDATE SET role_id=EXCLUDED.role_id,status='active'`, p.Id, j.CandidateUserId, j.RoleId)
			if e != nil {
				return e
			}
			state = "accepted"
			if _, e = tx.ExecContext(ctx, `UPDATE projects.join_request SET status='cancelled' WHERE project_id=$1 AND candidate_user_id=$2 AND status='pending' AND id<>$3`, p.Id, j.CandidateUserId, key); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE projects.join_request SET status=$2 WHERE id=$1`, key, state)
		if e != nil {
			return e
		}
		j.Status = state
		result = j
		return nil
	})
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, failure(err)
	}
	return result, nil
}
func (s *Service) ListJoins(ctx context.Context, q *pb.JoinQuery) (*pb.JoinList, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	limit, offset, err := page(q.Page)
	if err != nil {
		return nil, err
	}
	if q.ProjectId != "" {
		if !validID(q.ProjectId) {
			return nil, invalid()
		}
		p, e := s.Repo.Get(ctx, q.ProjectId)
		if e != nil {
			return nil, failure(e)
		}
		if p.OwnerUserId != id {
			return nil, status.Error(codes.PermissionDenied, "требуется инициатор")
		}
	}
	if q.RoleId != "" && !validID(q.RoleId) {
		return nil, invalid()
	}
	if q.Status != "" && q.Status != "pending" && q.Status != "accepted" && q.Status != "rejected" && q.Status != "cancelled" {
		return nil, invalid()
	}
	res, err := s.Repo.Joins(ctx, q, id, limit, offset)
	return res, failure(err)
}
func (s *Service) RemoveMember(ctx context.Context, q *pb.MemberID) (*pb.Project, error) {
	return s.remove(ctx, q.ProjectId, q.UserId, false)
}
func (s *Service) Leave(ctx context.Context, q *pb.ProjectID) (*pb.Project, error) {
	return s.remove(ctx, q.Id, "", true)
}
func (s *Service) remove(ctx context.Context, key, target string, leave bool) (*pb.Project, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if leave {
		target = id
	}
	if !validID(key) || !validID(target) {
		return nil, invalid()
	}
	p, err := s.Repo.Change(ctx, key, func(tx *sql.Tx, p *pb.Project) error {
		if !leave && p.OwnerUserId != id {
			return ErrDenied
		}
		if target == p.OwnerUserId || p.Status == "completed" || p.Status == "cancelled" {
			return ErrConflict
		}
		if !member(p, target) {
			return errUnchanged
		}
		state := "removed"
		if leave {
			state = "left"
		}
		_, e := tx.ExecContext(ctx, `UPDATE projects.member SET status=$3 WHERE project_id=$1 AND user_id=$2`, key, target, state)
		return e
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) Moderate(ctx context.Context, q *pb.ModerationInput) (*pb.Project, error) {
	id, err := s.actor(ctx, true, true)
	if err != nil {
		return nil, err
	}
	if !validID(q.ProjectId) || q.ExpectedRevision < 1 || !text(q.Reason, 2000, true) {
		return nil, invalid()
	}
	p, err := s.Repo.Change(ctx, q.ProjectId, func(tx *sql.Tx, p *pb.Project) error {
		if p.Revision != q.ExpectedRevision {
			return ErrConflict
		}
		p.ModerationHidden = q.Hide
		_, e := tx.ExecContext(ctx, `INSERT INTO projects.moderation(project_id,actor_admin_id,hidden,reason) VALUES($1,$2,$3,$4)`, p.Id, id, q.Hide, q.Reason)
		return e
	})
	if err != nil {
		return nil, failure(err)
	}
	return sanitize(p, id), nil
}
func (s *Service) ListModeration(ctx context.Context, q *pb.ModerationQuery) (*pb.ProjectList, error) {
	if _, err := s.actor(ctx, true, true); err != nil {
		return nil, err
	}
	limit, offset, err := page(q.Page)
	if err != nil {
		return nil, err
	}
	if !text(q.Text, 200, false) || (q.Status != "" && q.Status != "unpublished" && q.Status != "recruiting" && q.Status != "active" && q.Status != "completed" && q.Status != "cancelled") {
		return nil, invalid()
	}
	res, err := s.Repo.List(ctx, &pb.ProjectQuery{Text: q.Text, Status: q.Status}, "", true, limit, offset, true, q.HiddenOnly)
	if err != nil {
		return nil, failure(err)
	}

	return res, nil
}
func (s *Service) ModerationHistory(ctx context.Context, q *pb.ProjectID) (*pb.ModerationList, error) {
	return s.PaginatedModerationHistory(ctx, &managementv1.HistoryQuery{Id: q.Id})
}
