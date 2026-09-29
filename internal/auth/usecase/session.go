package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/repository"
)

var (
	ErrInvalidToken  = errors.New("invalid session token")
	ErrInvalidUserID = errors.New("invalid user ID")
)

const SessionTTL = 24 * time.Hour

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type SessionService interface {
	Create(context.Context, string) (string, time.Time, error)
	Validate(context.Context, string) (string, error)
	Revoke(context.Context, string) error
}

type Service struct{ store repository.SessionStore }

func NewSessionService(store repository.SessionStore) *Service { return &Service{store: store} }

var _ SessionService = (*Service)(nil)

func (s *Service) Create(ctx context.Context, userID string) (string, time.Time, error) {
	if !uuidPattern.MatchString(strings.TrimSpace(userID)) {
		return "", time.Time{}, ErrInvalidUserID
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	expiresAt := time.Now().UTC().Add(SessionTTL)
	if err := s.store.Save(ctx, token, userID, SessionTTL); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Service) Validate(ctx context.Context, token string) (string, error) {
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
