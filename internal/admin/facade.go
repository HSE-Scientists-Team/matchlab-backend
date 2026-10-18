package admin

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"log/slog"
	"time"
)

func field(q proto.Message, name string) string {
	if q == nil {
		return ""
	}
	v := q.ProtoReflect()
	f := v.Descriptor().Fields().ByName(protoreflect.Name(name))
	if f == nil || f.Kind() != protoreflect.StringKind {
		return ""
	}
	return v.Get(f).String()
}
func proxy[Q proto.Message, R proto.Message](ctx context.Context, s *Service, audience string, q Q, call func(context.Context, Q, ...grpc.CallOption) (R, error), action string, adminOnly bool) (R, error) {
	var zero R
	i, e := self(ctx)
	if e != nil {
		return zero, e
	}
	if adminOnly && i.account.Role != "administrator" {
		return zero, status.Error(codes.PermissionDenied, "требуется администратор")
	}
	out, e := adminauth.Sign(ctx, s.SigningKey, i.account.Id, i.account.Role, audience, i.account.Email)
	if e != nil {
		return zero, e
	}
	out, cancel := context.WithTimeout(out, 5*time.Second)
	defer cancel()
	eventID := ""
	target := field(q, "id")
	if target == "" {
		target = field(q, "project_id")
	}
	if target == "" {
		target = field(q, "ticket_id")
	}
	if action != "" {
		if target != "" && !m.ID(target) {
			return zero, m.Invalid()
		}
		if !m.Text(field(q, "reason"), 4000, false) || !m.Text(field(q, "comment"), 4000, false) {
			return zero, m.Invalid()
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return zero, m.Error(err)
		}
		reason := field(q, "reason")
		if reason == "" {
			reason = field(q, "comment")
		}
		eventID, err = audit(ctx, tx, i.account.Id, action, target, reason, "started")
		if err != nil {
			tx.Rollback()
			return zero, err
		}
		if err = tx.Commit(); err != nil {
			return zero, m.Error(err)
		}
	}
	result, e := call(out, q)
	if eventID != "" {
		state := "succeeded"
		if e != nil {
			state = "failed"
			switch status.Code(e) {
			case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal, codes.Unknown:
				state = "unknown"
			}
		}
		// The owner commits the authoritative mutation and history atomically. A lost
		// response is not a rollback; expected_revision prevents blind replay.
		if e == nil && target == "" {
			target = field(result, "id")
		}
		finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, err := s.DB.ExecContext(finish, `UPDATE admin.audit_event SET result=$2,resource_id=$3,completed_at=now() WHERE id=$1`, eventID, state, target)
		cancelFinish()
		if err != nil {
			slog.ErrorContext(ctx, "admin audit outcome update failed", "audit_event_id", eventID)
		}
	}
	if e != nil {
		switch status.Code(e) {
		case codes.Unknown:
			return zero, status.Error(codes.Unavailable, "зависимый сервис недоступен; проверьте состояние перед повтором")
		}
	}
	return result, e
}
func (s *Service) GetStatistics(ctx context.Context, q *mpb.StatisticsQuery) (*pb.AdminStatistics, error) {
	i, e := self(ctx)
	if e != nil {
		return nil, e
	}
	from, to, e := m.Range(q)
	if e != nil {
		return nil, e
	}
	q = &mpb.StatisticsQuery{FromUnix: from.Unix(), ToUnix: to.Unix()}
	out := &pb.AdminStatistics{}
	group, call := errgroup.WithContext(ctx)
	group.Go(func() error {
		v, err := proxy(call, s, "user", q, s.Users.GetStatistics, "", false)
		out.Users = v
		return err
	})
	group.Go(func() error {
		v, err := proxy(call, s, "support", q, s.Support.GetStatistics, "", false)
		out.Support = v
		return err
	})
	group.Go(func() error {
		v, err := proxy(call, s, "project", q, s.Projects.GetStatistics, "", false)
		out.Projects = v
		return err
	})
	group.Go(func() error {
		specs := []m.StatQuery{{Category: "staff_by_status", SQL: `SELECT CASE WHEN active THEN 'active' ELSE 'inactive' END,count(*) FROM admin.account GROUP BY active`}, {Category: "staff_by_role", SQL: `SELECT role,count(*) FROM admin.account GROUP BY role`}, {Category: "logins", Period: true, Daily: true, SQL: `SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*) FROM admin.audit_event WHERE action='session.login' AND created_at >=$1 AND created_at<$2 GROUP BY 1 ORDER BY 1`}}
		if i.account.Role != "administrator" {
			specs = specs[2:]
		}
		v, err := m.Statistics(call, s.DB, q, specs)
		out.Staff = v
		return err
	})
	if e = group.Wait(); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *Service) ListTickets(ctx context.Context, q *mpb.Query) (*spb.TicketList, error) {
	return proxy(ctx, s, "support", q, s.Support.ListTickets, "", false)
}

func (s *Service) GetTicket(ctx context.Context, q *mpb.ID) (*spb.Ticket, error) {
	return proxy(ctx, s, "support", q, s.Support.GetTicket, "", false)
}

func (s *Service) UpdateTicket(ctx context.Context, q *spb.TicketChange) (*spb.Ticket, error) {
	if _, e := self(ctx); e != nil {
		return nil, e
	}
	if q.Action == "assign" {
		if !m.ID(q.AssignedAdminId) {
			return nil, m.Invalid()
		}
		var active bool
		if e := s.DB.QueryRowContext(ctx, `SELECT active FROM admin.account WHERE id=$1`, q.AssignedAdminId).Scan(&active); e != nil {
			return nil, m.Error(e)
		}
		if !active {
			return nil, status.Error(codes.FailedPrecondition, "сотрудник неактивен")
		}
	}
	return proxy(ctx, s, "support", q, s.Support.UpdateTicket, "ticket.update", false)
}

func (s *Service) AddTicketNote(ctx context.Context, q *spb.TicketNote) (*mpb.Event, error) {
	return proxy(ctx, s, "support", q, s.Support.AddNote, "ticket.note", false)
}

func (s *Service) TicketHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	return proxy(ctx, s, "support", q, s.Support.TicketHistory, "", false)
}

func (s *Service) DownloadTicketAttachment(ctx context.Context, q *spb.AttachmentQuery) (*mpb.Download, error) {
	return proxy(ctx, s, "support", q, s.Support.DownloadAttachment, "ticket.evidence", false)
}

func (s *Service) ListUsers(ctx context.Context, q *mpb.Query) (*upb.ManagedUserList, error) {
	return proxy(ctx, s, "user", q, s.Users.ListUsers, "", false)
}

func (s *Service) GetUser(ctx context.Context, q *mpb.ID) (*upb.ManagedUser, error) {
	return proxy(ctx, s, "user", q, s.Users.GetUser, "", false)
}

func (s *Service) SetUserStatus(ctx context.Context, q *upb.UserStatusChange) (*upb.ManagedUser, error) {
	return proxy(ctx, s, "user", q, s.Users.SetUserStatus, "user.status", false)
}

func (s *Service) UserHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	return proxy(ctx, s, "user", q, s.Users.UserHistory, "", false)
}

func (s *Service) ListDomains(ctx context.Context, q *mpb.Query) (*upb.DomainList, error) {
	return proxy(ctx, s, "user", q, s.Users.ListDomains, "", false)
}

func (s *Service) SaveDomain(ctx context.Context, q *upb.DomainInput) (*upb.EmailDomain, error) {
	return proxy(ctx, s, "user", q, s.Users.SaveDomain, "domain.save", true)
}

func (s *Service) DomainHistory(ctx context.Context, q *mpb.HistoryQuery) (*mpb.EventList, error) {
	return proxy(ctx, s, "user", q, s.Users.DomainHistory, "", false)
}

func (s *Service) ListAchievements(ctx context.Context, q *mpb.Query) (*upb.ManagedAchievementList, error) {
	return proxy(ctx, s, "user", q, s.Users.ListAchievementsForReview, "", false)
}

func (s *Service) GetAchievement(ctx context.Context, q *mpb.ID) (*upb.Achievement, error) {
	return proxy(ctx, s, "user", q, s.Users.GetAchievementForReview, "", false)
}

func (s *Service) ReviewAchievement(ctx context.Context, q *upb.VerificationDecision) (*upb.Achievement, error) {
	return proxy(ctx, s, "user", q, s.Profiles.ReviewAchievement, "achievement.review", false)
}

func (s *Service) AchievementHistory(ctx context.Context, q *mpb.HistoryQuery) (*upb.AchievementDecisionList, error) {
	return proxy(ctx, s, "user", q, s.Users.AchievementHistory, "", false)
}

func (s *Service) AchievementEvidence(ctx context.Context, q *upb.AchievementID) (*upb.Evidence, error) {
	return proxy(ctx, s, "user", q, s.Profiles.GetAchievementEvidence, "achievement.evidence", false)
}

func (s *Service) ListProjects(ctx context.Context, q *ppb.ModerationQuery) (*ppb.ProjectList, error) {
	return proxy(ctx, s, "project", q, s.Projects.ListModeration, "", false)
}

func (s *Service) GetProject(ctx context.Context, q *ppb.ProjectID) (*ppb.Project, error) {
	return proxy(ctx, s, "project", q, s.Projects.GetForModeration, "", false)
}

func (s *Service) ModerateProject(ctx context.Context, q *ppb.ModerationInput) (*ppb.Project, error) {
	return proxy(ctx, s, "project", q, s.Projects.Moderate, "project.moderate", false)
}

func (s *Service) ProjectHistory(ctx context.Context, q *mpb.HistoryQuery) (*ppb.ModerationList, error) {
	return proxy(ctx, s, "project", q, s.Projects.PaginatedModerationHistory, "", false)
}
