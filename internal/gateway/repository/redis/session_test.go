package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository"
	goRedis "github.com/redis/go-redis/v9"
)

type mockKV struct {
	key, value string
	ttl        time.Duration
	getErr     error
	setErr     error
	delErr     error
}

func (m *mockKV) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.key, m.value, m.ttl = key, value, ttl
	return m.setErr
}
func (m *mockKV) Get(_ context.Context, key string) (string, error) {
	m.key = key
	return m.value, m.getErr
}
func (m *mockKV) Del(_ context.Context, key string) error {
	m.key = key
	return m.delErr
}

func TestSessionStore(t *testing.T) {
	kv := &mockKV{}
	store := NewSessionStore(kv)
	ctx := context.Background()
	if err := store.Save(ctx, "secret-token", "user-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(kv.key, "secret-token") || !strings.HasPrefix(kv.key, "gateway:session:") || kv.value != "user-1" || kv.ttl != time.Hour {
		t.Fatalf("unexpected stored data: key %q, value %q, ttl %s", kv.key, kv.value, kv.ttl)
	}
	wantKey := kv.key
	if userID, err := store.Find(ctx, "secret-token"); err != nil || userID != "user-1" || kv.key != wantKey {
		t.Fatalf("Find: user %q, err %v", userID, err)
	}
	if err := store.Delete(ctx, "secret-token"); err != nil || kv.key != wantKey {
		t.Fatalf("Delete: %v", err)
	}
	kv.getErr = goRedis.Nil
	if _, err := store.Find(ctx, "secret-token"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
}
