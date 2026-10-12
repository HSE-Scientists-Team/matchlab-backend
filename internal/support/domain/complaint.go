package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidArgument    = errors.New("некорректная жалоба")
	ErrAttachmentNotFound = errors.New("вложение не найдено или не принадлежит автору")
	ErrAttachmentNotReady = errors.New("вложение ещё не готово")
	ErrMediaUnavailable   = errors.New("сервис Media недоступен")
)

type NewComplaint struct {
	UserID          string
	Subject         string
	Description     string
	TargetUserID    *string
	TargetProjectID *string
	FileIDs         []string
}

type Complaint struct {
	ID        string
	Status    string
	CreatedAt time.Time
}
