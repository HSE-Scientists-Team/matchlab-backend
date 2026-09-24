package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository"
)

var ErrInvalidToken = errors.New("invalid session token")
var ErrInvalidUserID = errors.New("user ID is required")

const sessionTTL = 24 * time.Hour

type SessionService interface {
	Create(ctx context.Context, userID string) (string, error)
	Get(ctx context.Context, token string) (string, error)
	Revoke(ctx context.Context, token string) error
}

type Service struct{ store repository.SessionStore }

func NewSessionService(store repository.SessionStore) *Service { return &Service{store: store} }

var _ SessionService = (*Service)(nil)

func (s *Service) Create(ctx context.Context, userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", ErrInvalidUserID
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	if err := s.store.Save(ctx, token, userID, sessionTTL); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) Get(ctx context.Context, token string) (string, error) {
	if !validToken(token) {
		return "", ErrInvalidToken
	}
	return s.store.Find(ctx, token)
}

func (s *Service) Revoke(ctx context.Context, token string) error {
	if !validToken(token) {
		return ErrInvalidToken
	}
	return s.store.Delete(ctx, token)
}

func validToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}
