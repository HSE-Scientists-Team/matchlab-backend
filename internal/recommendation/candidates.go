package recommendation

import (
	"context"
	"sync"

	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Profiles interface {
	GetProfile(context.Context, *upb.ProfileQuery, ...grpc.CallOption) (*upb.Profile, error)
	CheckInteraction(context.Context, *upb.InteractionQuery, ...grpc.CallOption) (*upb.InteractionResult, error)
}

type Projects interface {
	Get(context.Context, *ppb.ProjectID, ...grpc.CallOption) (*ppb.Project, error)
}

func (s *Service) person(ctx context.Context, viewer, id string) (*upb.Person, error) {
	if id == viewer || s.Profiles == nil {
		return nil, nil
	}
	profile, err := s.Profiles.GetProfile(ctx, &upb.ProfileQuery{UserId: id})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := profile.GetDetails()
	if d == nil || !d.OpenToCollaboration || d.DisplayName == "" {
		return nil, nil
	}
	interaction, err := s.Profiles.CheckInteraction(ctx, &upb.InteractionQuery{FromUserId: viewer, ToUserId: id})
	if err != nil || interaction.GetBlocked() {
		return nil, err
	}
	// Deliberately project to Person. Never serialize contact fields from Profile.
	return &upb.Person{UserId: id, DisplayName: d.DisplayName, AvatarFileId: d.AvatarFileId,
		University: d.University, StudyLevel: d.StudyLevel, StudyYear: d.StudyYear,
		Specialization: d.Specialization, Skills: d.Skills, Interests: d.Interests,
		OpenToCollaboration: d.OpenToCollaboration, Verified: profile.Verified,
		Rating: profile.Rating, CompletedProjectCount: profile.CompletedProjectCount}, nil
}

func availableProject(project *ppb.Project, viewer string) bool {
	if project == nil || project.Status != "recruiting" || project.ModerationHidden || project.OwnerUserId == viewer {
		return false
	}
	for _, member := range project.Members {
		if member.UserId == viewer && member.Status == "active" {
			return false
		}
	}
	for _, role := range project.Roles {
		if role.Occupied < role.Slots {
			return true
		}
	}
	return false
}

func (s *Service) project(ctx context.Context, viewer, id string) (*ppb.Project, error) {
	if s.ProjectClient == nil {
		return nil, nil
	}
	project, err := s.ProjectClient.Get(ctx, &ppb.ProjectID{Id: id})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !availableProject(project, viewer) {
		return nil, nil
	}
	if s.Profiles != nil {
		interaction, err := s.Profiles.CheckInteraction(ctx, &upb.InteractionQuery{FromUserId: viewer, ToUserId: project.OwnerUserId})
		if err != nil || interaction.GetBlocked() {
			return nil, err
		}
	}
	return project, nil
}

// Check a bounded candidate pool concurrently, retaining the original ranking.
// An owner outage is an error, not permission to serve stale cards.
func validateCandidates[T any](ctx context.Context, entries []Match, limit int, validate func(context.Context, string) (*T, error)) ([]*T, error) {
	if len(entries) > limit {
		entries = entries[:limit]
	}
	results := make([]*T, len(entries))
	var wg sync.WaitGroup
	var mutex sync.Mutex
	var firstError error
	semaphore := make(chan struct{}, 8)
	for i, entry := range entries {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				mutex.Lock()
				if firstError == nil {
					firstError = ctx.Err()
				}
				mutex.Unlock()
				return
			}
			value, err := validate(ctx, id)
			if err != nil {
				mutex.Lock()
				if firstError == nil {
					firstError = err
				}
				mutex.Unlock()
				return
			}
			results[index] = value
		}(i, entry.ID)
	}
	wg.Wait()
	return results, firstError
}
