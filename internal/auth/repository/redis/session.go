package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/auth/repository"
	goRedis "github.com/redis/go-redis/v9"
)

type KV interface {
	Set(context.Context, string, string, time.Duration) error
	Get(context.Context, string) (string, error)
	Del(context.Context, string) error
}

type clientAdapter struct{ client *goRedis.Client }

func NewClientAdapter(client *goRedis.Client) KV { return clientAdapter{client: client} }
func (a clientAdapter) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return a.client.Set(ctx, key, value, ttl).Err()
}
func (a clientAdapter) Get(ctx context.Context, key string) (string, error) {
	return a.client.Get(ctx, key).Result()
}
func (a clientAdapter) Del(ctx context.Context, key string) error {
	return a.client.Del(ctx, key).Err()
}

type SessionStore struct{ kv KV }

func NewSessionStore(kv KV) *SessionStore { return &SessionStore{kv: kv} }

var _ repository.SessionStore = (*SessionStore)(nil)

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "auth:session:" + hex.EncodeToString(sum[:])
}

func (s *SessionStore) Save(ctx context.Context, token, userID string, ttl time.Duration) error {
	if err := s.kv.Set(ctx, sessionKey(token), userID, ttl); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func (s *SessionStore) Find(ctx context.Context, token string) (string, error) {
	userID, err := s.kv.Get(ctx, sessionKey(token))
	if errors.Is(err, goRedis.Nil) {
		return "", repository.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find session: %w", err)
	}
	return userID, nil
}

func (s *SessionStore) Delete(ctx context.Context, token string) error {
	if err := s.kv.Del(ctx, sessionKey(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
