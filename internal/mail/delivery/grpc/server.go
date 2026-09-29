package grpc

import (
	"context"
	"errors"
	"time"

	mailv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/mail/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	mailv1.UnimplementedEmailServiceServer
	mail *service.Service
}

func NewServer(mail *service.Service) *Server { return &Server{mail: mail} }

func (s *Server) SendVerificationEmail(ctx context.Context, req *mailv1.SendVerificationEmailRequest) (*mailv1.SendVerificationEmailResponse, error) {
	if err := s.mail.SendVerification(ctx, req.GetRecipient(), req.GetToken(), time.Unix(req.GetExpiresAtUnix(), 0)); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidEmail), errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrInvalidExpiry):
			return nil, status.Error(codes.InvalidArgument, "некорректное письмо для подтверждения адреса")
		default:
			return nil, status.Error(codes.Unavailable, "SMTP-сервер не принял письмо")
		}
	}
	return &mailv1.SendVerificationEmailResponse{Sent: true}, nil
}
