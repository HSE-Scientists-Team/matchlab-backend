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
	id, err := s.users.Register(ctx, req.GetLogin(), req.GetPassword())
	switch {
	case errors.Is(err, usecase.ErrInvalidLogin):
		return nil, status.Error(codes.InvalidArgument, "login must be 1-32 characters: letters, digits, _, ., or -")
	case errors.Is(err, usecase.ErrInvalidPassword):
		return nil, status.Error(codes.InvalidArgument, "password must contain between 8 and 72 bytes")
	case errors.Is(err, repository.ErrLoginTaken):
		return nil, status.Error(codes.AlreadyExists, "login is already registered")
	case err != nil:
		return nil, status.Error(codes.Internal, "could not register user")
	default:
		return &userv1.RegisterResponse{UserId: id}, nil
	}
}

func (s *Server) Login(ctx context.Context, req *userv1.LoginRequest) (*userv1.LoginResponse, error) {
	id, token, err := s.users.Login(ctx, req.GetLogin(), req.GetPassword())
	switch {
	case errors.Is(err, usecase.ErrInvalidCredentials):
		return nil, status.Error(codes.Unauthenticated, "invalid login or password")
	case errors.Is(err, usecase.ErrAccountInactive):
		return nil, status.Error(codes.FailedPrecondition, "account is not active")
	case err != nil:
		return nil, status.Error(codes.Unavailable, "login service unavailable")
	default:
		return &userv1.LoginResponse{UserId: id, SessionToken: token}, nil
	}
}

func (s *Server) RequestEmailVerification(ctx context.Context, req *userv1.RequestEmailVerificationRequest) (*userv1.RequestEmailVerificationResponse, error) {
	err := s.users.RequestEmailVerification(ctx, req.GetUserId(), req.GetEmail())
	switch {
	case errors.Is(err, usecase.ErrInvalidEmail):
		return nil, status.Error(codes.InvalidArgument, "valid email is required")
	case errors.Is(err, repository.ErrNotFound):
		return nil, status.Error(codes.NotFound, "user not found")
	case errors.Is(err, repository.ErrEmailAlreadyVerified):
		return nil, status.Error(codes.AlreadyExists, "email is already verified for this user")
	case errors.Is(err, usecase.ErrEmailDelivery):
		return nil, status.Error(codes.Unavailable, "email verification could not be sent")
	case err != nil:
		return nil, status.Error(codes.Internal, "could not request email verification")
	default:
		return &userv1.RequestEmailVerificationResponse{}, nil
	}
}

func (s *Server) ConfirmEmail(ctx context.Context, req *userv1.ConfirmEmailRequest) (*userv1.ConfirmEmailResponse, error) {
	err := s.users.ConfirmEmail(ctx, req.GetToken())
	switch {
	case errors.Is(err, usecase.ErrInvalidVerification):
		return nil, status.Error(codes.InvalidArgument, "invalid email verification token")
	case errors.Is(err, repository.ErrVerificationNotFound):
		return nil, status.Error(codes.NotFound, "verification token not found or already used")
	case errors.Is(err, repository.ErrVerificationExpired):
		return nil, status.Error(codes.DeadlineExceeded, "email verification token expired")
	case errors.Is(err, repository.ErrEmailClaimed):
		return nil, status.Error(codes.AlreadyExists, "email is already verified by another user")
	case err != nil:
		return nil, status.Error(codes.Internal, "could not confirm email")
	default:
		return &userv1.ConfirmEmailResponse{}, nil
	}
}

func (s *Server) GetEmailStatus(ctx context.Context, req *userv1.GetEmailStatusRequest) (*userv1.GetEmailStatusResponse, error) {
	result, err := s.users.GetEmailStatus(ctx, req.GetUserId())
	if errors.Is(err, repository.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "could not load email status")
	}
	return &userv1.GetEmailStatusResponse{Email: result.Email, Status: result.Status, PendingEmail: result.PendingEmail}, nil
}
