package grpc

import (
	"context"
	"errors"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log/slog"
	"time"
)

type Support interface {
	CreateComplaint(context.Context, domain.NewComplaint) (domain.Complaint, error)
}
type Server struct {
	supportv1.UnimplementedSupportServiceServer
	support Support
	logger  *slog.Logger
}

func NewServer(support Support, logger *slog.Logger) *Server {
	return &Server{support: support, logger: logger}
}

func (s *Server) CreateComplaint(ctx context.Context, req *supportv1.CreateComplaintRequest) (*supportv1.CreateComplaintResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "отсутствует запрос")
	}
	result, err := s.support.CreateComplaint(ctx, domain.NewComplaint{
		UserID: req.GetUserId(), Subject: req.GetSubject(), Description: req.GetDescription(),
		TargetUserID: req.TargetUserId, TargetProjectID: req.TargetProjectId, FileIDs: req.GetFileIds(),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidArgument):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, domain.ErrAttachmentNotFound):
			return nil, status.Error(codes.NotFound, domain.ErrAttachmentNotFound.Error())
		case errors.Is(err, domain.ErrAttachmentNotReady):
			return nil, status.Error(codes.FailedPrecondition, domain.ErrAttachmentNotReady.Error())
		case errors.Is(err, domain.ErrMediaUnavailable):
			return nil, status.Error(codes.Unavailable, domain.ErrMediaUnavailable.Error())
		case errors.Is(err, context.Canceled):
			return nil, status.Error(codes.Canceled, "запрос отменён")
		case errors.Is(err, context.DeadlineExceeded):
			return nil, status.Error(codes.DeadlineExceeded, "истёк срок выполнения запроса")
		default:
			s.logger.ErrorContext(ctx, "создание жалобы: ошибка хранилища")
			return nil, status.Error(codes.Internal, "не удалось создать жалобу")
		}
	}
	return &supportv1.CreateComplaintResponse{Id: result.ID, Status: supportv1.SupportTicketStatus_SUPPORT_TICKET_STATUS_OPEN, CreatedAtUnix: result.CreatedAt.Unix()}, nil
}
