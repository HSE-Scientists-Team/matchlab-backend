package repository

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("session not found")

// SessionStore owns persistence for authentication sessions.
type SessionStore interface {
	Save(ctx context.Context, token, userID string, ttl time.Duration) error
	Find(ctx context.Context, token string) (string, error)
	Delete(ctx context.Context, token string) error
}
