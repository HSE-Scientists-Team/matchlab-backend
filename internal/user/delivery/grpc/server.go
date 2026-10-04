package grpc

import (
	"context"
	"errors"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/usecase"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	userv1.UnimplementedUserServiceServer
	users *usecase.Service
}

func NewServer(users *usecase.Service) *Server { return &Server{users: users} }

func (s *Server) Register(ctx context.Context, req *userv1.RegisterRequest) (*userv1.RegisterResponse, error) {
	err := s.users.Register(ctx, req.GetEmail(), req.GetPassword())
	switch {
	case errors.Is(err, usecase.ErrInvalidEmail):
		return nil, status.Error(codes.InvalidArgument, "требуется корректный адрес электронной почты")
	case errors.Is(err, usecase.ErrInvalidPassword):
		return nil, status.Error(codes.InvalidArgument, "пароль должен содержать от 8 до 72 байт")
	case errors.Is(err, repository.ErrEmailTaken):
		return nil, status.Error(codes.AlreadyExists, "email уже зарегистрирован")
	case errors.Is(err, repository.ErrOrganizationUnavailable):
		return nil, status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, usecase.ErrEmailDelivery):
		return nil, status.Error(codes.Unavailable, usecase.ErrEmailDelivery.Error())
	case err != nil:
		return nil, status.Error(codes.Internal, "не удалось зарегистрировать пользователя")
	default:
		return &userv1.RegisterResponse{Status: "pending"}, nil
	}
}

func (s *Server) Login(ctx context.Context, req *userv1.LoginRequest) (*userv1.LoginResponse, error) {
	id, token, err := s.users.Login(ctx, req.GetEmail(), req.GetPassword())
	switch {
	case errors.Is(err, usecase.ErrInvalidCredentials):
		return nil, status.Error(codes.Unauthenticated, "неверный email или пароль")
	case errors.Is(err, usecase.ErrEmailUnverified):
		return nil, status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, usecase.ErrAccountInactive):
		return nil, status.Error(codes.FailedPrecondition, "учётная запись неактивна")
	case err != nil:
		return nil, status.Error(codes.Unavailable, "сервис входа недоступен")
	default:
		return &userv1.LoginResponse{UserId: id, SessionToken: token}, nil
	}
}

func (s *Server) ConfirmEmail(ctx context.Context, req *userv1.ConfirmEmailRequest) (*userv1.ConfirmEmailResponse, error) {
	err := s.users.ConfirmEmail(ctx, req.GetToken())
	switch {
	case errors.Is(err, usecase.ErrInvalidVerification):
		return nil, status.Error(codes.InvalidArgument, "недействительный токен подтверждения адреса")
	case errors.Is(err, repository.ErrVerificationNotFound):
		return nil, status.Error(codes.NotFound, "токен подтверждения не найден или уже использован")
	case errors.Is(err, repository.ErrVerificationExpired):
		return nil, status.Error(codes.DeadlineExceeded, "срок действия токена подтверждения адреса истёк")
	case errors.Is(err, repository.ErrEmailTaken):
		return nil, status.Error(codes.AlreadyExists, "email уже зарегистрирован")
	case errors.Is(err, repository.ErrOrganizationUnavailable):
		return nil, status.Error(codes.PermissionDenied, "подтверждение почты для этой организации недоступно")
	case err != nil:
		return nil, status.Error(codes.Internal, "не удалось подтвердить адрес")
	default:
		return &userv1.ConfirmEmailResponse{}, nil
	}
}

func (s *Server) GetEmailStatus(ctx context.Context, req *userv1.GetEmailStatusRequest) (*userv1.GetEmailStatusResponse, error) {
	result, err := s.users.GetEmailStatus(ctx, req.GetUserId())
	if errors.Is(err, repository.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "пользователь не найден")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "не удалось получить состояние адреса")
	}
	return &userv1.GetEmailStatusResponse{Email: result.Email, Status: result.Status, PendingEmail: result.PendingEmail}, nil
}
