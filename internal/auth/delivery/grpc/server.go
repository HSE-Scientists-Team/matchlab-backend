package grpc

import (
	"context"
	"errors"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/usecase"
	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	authv1.UnimplementedAuthServiceServer
	sessions usecase.SessionService
}

func NewServer(sessions usecase.SessionService) *Server { return &Server{sessions: sessions} }

func (s *Server) CreateSession(ctx context.Context, req *authv1.CreateSessionRequest) (*authv1.CreateSessionResponse, error) {
	token, expiresAt, err := s.sessions.Create(ctx, req.GetUserId())
	if errors.Is(err, usecase.ErrInvalidUserID) {
		return nil, status.Error(codes.InvalidArgument, "требуется корректный user_id")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "хранилище сеансов недоступно")
	}
	return &authv1.CreateSessionResponse{Token: token, ExpiresAtUnix: expiresAt.Unix()}, nil
}

func (s *Server) ValidateSession(ctx context.Context, req *authv1.ValidateSessionRequest) (*authv1.ValidateSessionResponse, error) {
	userID, err := s.sessions.Validate(ctx, req.GetToken())
	if errors.Is(err, usecase.ErrInvalidToken) || errors.Is(err, repository.ErrNotFound) {
		return nil, status.Error(codes.Unauthenticated, "недействительный сеанс")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "хранилище сеансов недоступно")
	}
	return &authv1.ValidateSessionResponse{UserId: userID}, nil
}

func (s *Server) RevokeSession(ctx context.Context, req *authv1.RevokeSessionRequest) (*authv1.RevokeSessionResponse, error) {
	if err := s.sessions.Revoke(ctx, req.GetToken()); err != nil {
		if errors.Is(err, usecase.ErrInvalidToken) {
			return nil, status.Error(codes.InvalidArgument, "недействительный токен сеанса")
		}
		return nil, status.Error(codes.Unavailable, "хранилище сеансов недоступно")
	}
	return &authv1.RevokeSessionResponse{}, nil
}
