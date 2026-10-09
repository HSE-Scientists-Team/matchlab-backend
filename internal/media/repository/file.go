package repository

import (
	"context"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

type Files interface {
	CreatePending(context.Context, domain.NewFile) (domain.File, error)
	FindOwned(context.Context, string, string) (domain.File, error)
	FindReadable(context.Context, string, string) (domain.File, error)
	MarkReady(context.Context, string, string, domain.ObjectInfo) (domain.File, error)
}
