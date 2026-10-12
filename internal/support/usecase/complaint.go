package usecase

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"github.com/google/uuid"
)

const MaxDescriptionLength = 10000
const MaxAttachments = 20

type Repository interface {
	Create(context.Context, domain.NewComplaint) (domain.Complaint, error)
}
type Media interface {
	ValidateAttachment(context.Context, string, string) error
}
type Service struct {
	repo  Repository
	media Media
}

func NewService(repo Repository, media Media) *Service { return &Service{repo: repo, media: media} }

func (s *Service) CreateComplaint(ctx context.Context, input domain.NewComplaint) (domain.Complaint, error) {
	invalid := func(message string) (domain.Complaint, error) {
		return domain.Complaint{}, fmt.Errorf("%w: %s", domain.ErrInvalidArgument, message)
	}
	userID, err := parseID(input.UserID)
	if err != nil {
		return invalid("user_id должен быть непустым UUID")
	}
	input.UserID = userID
	input.Subject = strings.TrimSpace(input.Subject)
	input.Description = strings.TrimSpace(input.Description)
	if !utf8.ValidString(input.Subject) || strings.ContainsRune(input.Subject, 0) || utf8.RuneCountInString(input.Subject) < 1 || utf8.RuneCountInString(input.Subject) > 255 {
		return invalid("subject должен содержать от 1 до 255 символов")
	}
	if !utf8.ValidString(input.Description) || strings.ContainsRune(input.Description, 0) || utf8.RuneCountInString(input.Description) < 1 || utf8.RuneCountInString(input.Description) > MaxDescriptionLength {
		return invalid("description должен содержать от 1 до 10000 символов")
	}
	if (input.TargetUserID == nil) == (input.TargetProjectID == nil) {
		return invalid("нужно указать ровно один target_user_id или target_project_id")
	}
	if input.TargetUserID != nil {
		id, err := parseID(*input.TargetUserID)
		if err != nil {
			return invalid("target_user_id должен быть непустым UUID")
		}
		input.TargetUserID = &id
	}
	if input.TargetProjectID != nil {
		id, err := parseID(*input.TargetProjectID)
		if err != nil {
			return invalid("target_project_id должен быть непустым UUID")
		}
		input.TargetProjectID = &id
	}
	if len(input.FileIDs) > MaxAttachments {
		return invalid("допускается не более 20 вложений")
	}
	seen := make(map[string]bool, len(input.FileIDs))
	ids := make([]string, len(input.FileIDs))
	for i, value := range input.FileIDs {
		id, err := parseID(value)
		if err != nil {
			return invalid("file_ids должны содержать непустые UUID")
		}
		if seen[id] {
			return invalid("file_ids не должны повторяться")
		}
		seen[id] = true
		ids[i] = id
	}
	input.FileIDs = ids
	for _, id := range input.FileIDs {
		if err := s.media.ValidateAttachment(ctx, input.UserID, id); err != nil {
			return domain.Complaint{}, err
		}
	}
	return s.repo.Create(ctx, input)
}

func parseID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("invalid UUID")
	}
	return id.String(), nil
}
