package repository

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("сеанс не найден")

type SessionStore interface {
	Save(context.Context, string, string, time.Duration) error
	Find(context.Context, string) (string, error)
	Delete(context.Context, string) error
}
