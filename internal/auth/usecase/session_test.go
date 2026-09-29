package usecase

import (
	"context"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	token, userID string
	ttl           time.Duration
}

func (s *memoryStore) Save(_ context.Context, token, userID string, ttl time.Duration) error {
	s.token, s.userID, s.ttl = token, userID, ttl
	return nil
}
func (s *memoryStore) Find(_ context.Context, token string) (string, error) {
	if token != s.token {
		return "", nil
	}
	return s.userID, nil
}
func (s *memoryStore) Delete(_ context.Context, token string) error {
	if token == s.token {
		s.token = ""
	}
	return nil
}

func TestCreateValidateAndRevoke(t *testing.T) {
	store := &memoryStore{}
	service := NewSessionService(store)
	ctx := context.Background()
	userID := "4f9a4c95-6144-4ec8-89e8-3866207d7561"
	token, expires, err := service.Create(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || strings.Contains(token, userID) || store.userID != userID || store.ttl != SessionTTL {
		t.Fatalf("unexpected session token/storage: token length %d, user %q, ttl %s", len(token), store.userID, store.ttl)
	}
	if time.Until(expires) < 23*time.Hour {
		t.Fatalf("unexpected expiry: %s", expires)
	}
	got, err := service.Validate(ctx, token)
	if err != nil || got != userID {
		t.Fatalf("validate: user %q, error %v", got, err)
	}
	if err := service.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
	if store.token != "" {
		t.Fatal("session was not revoked")
	}
}

func TestRejectsInvalidIDsAndTokens(t *testing.T) {
	service := NewSessionService(&memoryStore{})
	if _, _, err := service.Create(context.Background(), "not-a-uuid"); err != ErrInvalidUserID {
		t.Fatalf("Create error = %v", err)
	}
	if _, err := service.Validate(context.Background(), "short"); err != ErrInvalidToken {
		t.Fatalf("Validate error = %v", err)
	}
	if err := service.Revoke(context.Background(), "short"); err != ErrInvalidToken {
		t.Fatalf("Revoke error = %v", err)
	}
}
