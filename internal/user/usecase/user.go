package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidEmail        = errors.New("недействительный адрес электронной почты")
	ErrInvalidPassword     = errors.New("пароль должен содержать от 8 до 72 байт")
	ErrInvalidCredentials  = errors.New("неверный email или пароль")
	ErrEmailUnverified     = errors.New("подтвердите адрес электронной почты")
	ErrAccountInactive     = errors.New("учётная запись неактивна")
	ErrInvalidVerification = errors.New("недействительный токен подтверждения адреса")
	ErrEmailDelivery       = errors.New("не удалось отправить письмо с подтверждением")
)

const EmailVerificationTTL = 30 * time.Minute

type SessionCreator interface {
	Create(context.Context, string) (string, error)
}

type EmailSender interface {
	SendVerification(context.Context, string, string, time.Time) error
}

type Service struct {
	users    repository.Users
	sessions SessionCreator
	email    EmailSender
	now      func() time.Time
}

func NewService(users repository.Users, sessions SessionCreator, email EmailSender) *Service {
	return &Service{users: users, sessions: sessions, email: email, now: time.Now}
}

func (s *Service) Register(ctx context.Context, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if len(password) < 8 || len(password) > 72 {
		return ErrInvalidPassword
	}
	if s.email == nil {
		return ErrEmailDelivery
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("хеширование пароля: %w", err)
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("создание токена подтверждения: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := s.now().UTC().Add(EmailVerificationTTL)
	if err := s.users.SaveRegistration(ctx, email, string(passwordHash), hex.EncodeToString(tokenHash[:]), expiresAt); err != nil {
		return err
	}
	if err := s.email.SendVerification(ctx, email, token, expiresAt); err != nil {
		return fmt.Errorf("%w: %v", ErrEmailDelivery, err)
	}
	return nil
}

func (s *Service) Login(ctx context.Context, email, password string) (string, string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}
	account, err := s.users.FindByEmail(ctx, email)
	if errors.Is(err, repository.ErrNotFound) {
		return "", "", ErrInvalidCredentials
	}
	if err != nil {
		return "", "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) != nil {
		return "", "", ErrInvalidCredentials
	}
	if account.Status != "active" {
		return "", "", ErrAccountInactive
	}
	if !account.Verified {
		return "", "", ErrEmailUnverified
	}
	if err := s.users.MarkLogin(ctx, account.ID); err != nil {
		return "", "", err
	}
	token, err := s.sessions.Create(ctx, account.ID)
	if err != nil {
		return "", "", fmt.Errorf("создание сеанса входа: %w", err)
	}
	return account.ID, token, nil
}

func (s *Service) ConfirmEmail(ctx context.Context, token string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return ErrInvalidVerification
	}
	hash := sha256.Sum256([]byte(token))
	return s.users.ConfirmEmail(ctx, hex.EncodeToString(hash[:]), s.now().UTC())
}

func (s *Service) GetEmailStatus(ctx context.Context, userID string) (repository.EmailStatus, error) {
	return s.users.GetEmailStatus(ctx, userID)
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 320 {
		return "", ErrInvalidEmail
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", ErrInvalidEmail
	}
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return "", ErrInvalidEmail
	}
	return value, nil
}
