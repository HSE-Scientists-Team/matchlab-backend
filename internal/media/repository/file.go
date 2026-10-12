package repository

import (
	"context"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

type Files interface {
	Multiparts
	CreatePending(context.Context, domain.NewFile) (domain.File, error)
	FindOwned(context.Context, string, string) (domain.File, error)
	FindReadable(context.Context, string, string) (domain.File, error)
	MarkReady(context.Context, string, string, domain.ObjectInfo) (domain.File, error)
	BeginDeletion(context.Context, string, string) (domain.File, error)
	FinishDeletion(context.Context, string, string) error
	DeletingFiles(context.Context) ([]UploadOwner, error)
	StalePendingFiles(context.Context, time.Time, string) ([]UploadOwner, error)
	WithStalePending(context.Context, string, string, time.Time, func(*domain.File, *domain.Multipart) error) error
}
