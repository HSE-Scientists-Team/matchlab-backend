package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrInvalidEmail  = errors.New("недействительный адрес получателя")
	ErrInvalidToken  = errors.New("недействительный токен подтверждения")
	ErrInvalidExpiry = errors.New("недействительный срок действия токена подтверждения")
)

type Sender interface {
	SendVerification(context.Context, string, string, time.Time) error
}

type Service struct {
	sender Sender
	now    func() time.Time
}

func NewService(sender Sender) *Service {
	return &Service{sender: sender, now: time.Now}
}

// SendVerification возвращается только после принятия письма SMTP-сервером.
func (s *Service) SendVerification(ctx context.Context, recipient, token string, expiresAt time.Time) error {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	parsed, err := mail.ParseAddress(recipient)
	if err != nil || parsed.Address != recipient || len(recipient) > 320 {
		return ErrInvalidEmail
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return ErrInvalidToken
	}
	now := s.now().UTC()
	if !expiresAt.After(now) || expiresAt.After(now.Add(2*time.Hour)) {
		return ErrInvalidExpiry
	}
	if err := s.sender.SendVerification(ctx, recipient, token, expiresAt); err != nil {
		return fmt.Errorf("отправка письма с подтверждением: %w", err)
	}
	return nil
}
