package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository"
)

type mockStore struct {
	token, userID string
	ttl           time.Duration
	saveErr       error
	findErr       error
	deleteErr     error
}

func (m *mockStore) Save(_ context.Context, token, userID string, ttl time.Duration) error {
	m.token, m.userID, m.ttl = token, userID, ttl
	return m.saveErr
}
func (m *mockStore) Find(_ context.Context, token string) (string, error) {
	m.token = token
	return m.userID, m.findErr
}
func (m *mockStore) Delete(_ context.Context, token string) error {
	m.token = token
	return m.deleteErr
}

func TestSessionLifecycle(t *testing.T) {
	store := &mockStore{}
	service := NewSessionService(store)
	token, err := service.Create(context.Background(), "user-1")
	if err != nil || !validToken(token) || store.userID != "user-1" || store.ttl != sessionTTL {
		t.Fatalf("Create: token %q, ttl %s, err %v", token, store.ttl, err)
	}
	userID, err := service.Get(context.Background(), token)
	if err != nil || userID != "user-1" || store.token != token {
		t.Fatalf("Get: user %q, err %v", userID, err)
	}
	if err := service.Revoke(context.Background(), token); err != nil || store.token != token {
		t.Fatalf("Revoke: %v", err)
	}
}

func TestSessionErrors(t *testing.T) {
	store := &mockStore{saveErr: errors.New("redis unavailable")}
	service := NewSessionService(store)
	if _, err := service.Create(context.Background(), " "); !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("invalid user: %v", err)
	}
	if _, err := service.Create(context.Background(), "user-1"); !errors.Is(err, store.saveErr) {
		t.Fatalf("save failure: %v", err)
	}
	if _, err := service.Get(context.Background(), "invalid"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token: %v", err)
	}
	if err := service.Revoke(context.Background(), "invalid"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token revoke: %v", err)
	}
	store.saveErr = nil
	token, err := service.Create(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	store.findErr = repository.ErrNotFound
	if _, err := service.Get(context.Background(), token); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
}
