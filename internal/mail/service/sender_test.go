package service

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

type controlledSender struct {
	started   chan string
	finish    chan error
	recipient string
}

func (s *controlledSender) SendVerification(ctx context.Context, recipient, token string, _ time.Time) error {
	s.recipient = recipient
	s.started <- token
	select {
	case err := <-s.finish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSendVerificationWaitsForSMTPResult(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	for _, smtpErr := range []error{nil, errors.New("SMTP недоступен")} {
		sender := &controlledSender{started: make(chan string), finish: make(chan error)}
		mail := NewService(sender)
		result := make(chan error, 1)
		go func() {
			result <- mail.SendVerification(context.Background(), " Person@Example.org ", token, time.Now().Add(30*time.Minute))
		}()
		if got := <-sender.started; got != token || sender.recipient != "person@example.org" {
			t.Fatalf("неожиданные данные отправки: %q, %q", got, sender.recipient)
		}
		select {
		case err := <-result:
			t.Fatalf("отправка завершилась до ответа SMTP: %v", err)
		default:
		}
		sender.finish <- smtpErr
		if err := <-result; (err == nil) != (smtpErr == nil) {
			t.Fatalf("результат SMTP %v, результат сервиса %v", smtpErr, err)
		}
	}
}

func TestSendVerificationRejectsInvalidInputs(t *testing.T) {
	sender := &controlledSender{started: make(chan string), finish: make(chan error)}
	mail := NewService(sender)
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	for _, tc := range []struct {
		recipient string
		token     string
		expires   time.Time
		want      error
	}{
		{"bad-address", token, time.Now().Add(time.Minute), ErrInvalidEmail},
		{"a@example.org", "bad-token", time.Now().Add(time.Minute), ErrInvalidToken},
		{"a@example.org", token, time.Now().Add(-time.Minute), ErrInvalidExpiry},
	} {
		if err := mail.SendVerification(context.Background(), tc.recipient, tc.token, tc.expires); !errors.Is(err, tc.want) {
			t.Fatalf("ожидалась ошибка %v, получена %v", tc.want, err)
		}
	}
}
