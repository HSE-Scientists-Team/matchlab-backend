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
	"regexp"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidLogin        = errors.New("логин должен содержать от 1 до 32 букв, цифр или символов подчёркивания, точки либо дефиса")
	ErrInvalidEmail        = errors.New("недействительный адрес электронной почты")
	ErrInvalidPassword     = errors.New("пароль должен содержать от 8 до 72 байт")
	ErrInvalidCredentials  = errors.New("неверный логин или пароль")
	ErrAccountInactive     = errors.New("учётная запись неактивна")
	ErrInvalidVerification = errors.New("недействительный токен подтверждения адреса")
	ErrEmailDelivery       = errors.New("не удалось отправить письмо с подтверждением")
)

const EmailVerificationTTL = 30 * time.Minute

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

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

func (s *Service) Register(ctx context.Context, login, password string) (string, error) {
	login, err := normalizeLogin(login)
	if err != nil {
		return "", err
	}
	if len(password) < 8 || len(password) > 72 {
		return "", ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("хеширование пароля: %w", err)
	}
	return s.users.Create(ctx, login, string(hash))
}

func (s *Service) Login(ctx context.Context, login, password string) (string, string, error) {
	login, err := normalizeLogin(login)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}
	account, err := s.users.FindByLogin(ctx, login)
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
	if err := s.users.MarkLogin(ctx, account.ID); err != nil {
		return "", "", err
	}
	token, err := s.sessions.Create(ctx, account.ID)
	if err != nil {
		return "", "", fmt.Errorf("создание сеанса входа: %w", err)
	}
	return account.ID, token, nil
}

func (s *Service) RequestEmailVerification(ctx context.Context, userID, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if s.email == nil {
		return ErrEmailDelivery
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("создание токена подтверждения адреса: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(token))
	expiresAt := s.now().UTC().Add(EmailVerificationTTL)
	if err := s.users.SaveEmailVerification(ctx, userID, email, hex.EncodeToString(hash[:]), expiresAt); err != nil {
		return err
	}
	if err := s.email.SendVerification(ctx, email, token, expiresAt); err != nil {
		return fmt.Errorf("%w: %v", ErrEmailDelivery, err)
	}
	return nil
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

func normalizeLogin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !loginPattern.MatchString(value) {
		return "", ErrInvalidLogin
	}
	return strings.ToLower(value), nil
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
