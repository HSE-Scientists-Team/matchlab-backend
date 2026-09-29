package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/repository"
	goRedis "github.com/redis/go-redis/v9"
)

type mockKV struct {
	key, value string
	ttl        time.Duration
	getErr     error
}

func (m *mockKV) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.key, m.value, m.ttl = key, value, ttl
	return nil
}
func (m *mockKV) Get(_ context.Context, key string) (string, error) {
	m.key = key
	return m.value, m.getErr
}
func (m *mockKV) Del(_ context.Context, key string) error { m.key = key; return nil }

func TestSessionStoreHashesTokenAndMapsMissing(t *testing.T) {
	kv := &mockKV{}
	store := NewSessionStore(kv)
	ctx := context.Background()
	if err := store.Save(ctx, "secret-token", "user-id", time.Hour); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(kv.key, "secret-token") || !strings.HasPrefix(kv.key, "auth:session:") || kv.value != "user-id" || kv.ttl != time.Hour {
		t.Fatalf("unexpected stored session: key %q value %q ttl %s", kv.key, kv.value, kv.ttl)
	}
	if got, err := store.Find(ctx, "secret-token"); err != nil || got != "user-id" {
		t.Fatalf("Find: value %q error %v", got, err)
	}
	if err := store.Delete(ctx, "secret-token"); err != nil {
		t.Fatal(err)
	}
	kv.getErr = goRedis.Nil
	if _, err := store.Find(ctx, "secret-token"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
}
