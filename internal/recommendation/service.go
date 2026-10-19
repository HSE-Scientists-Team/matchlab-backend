package recommendation

import (
	"context"
	"encoding/json"

	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/rpcmeta"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Search interface {
	People(context.Context, *upb.PeopleQuery, ...grpc.CallOption) (*upb.PeopleList, error)
	Projects(context.Context, *ppb.ProjectQuery, ...grpc.CallOption) (*ppb.ProjectList, error)
}
type User interface {
	GetIdentity(context.Context, *upb.Empty, ...grpc.CallOption) (*upb.Identity, error)
}
type Service struct {
	pb.UnimplementedRecommendationServiceServer
	Search        Search
	User          User
	Profiles      Profiles
	ProjectClient Projects
	Cache         Cache
}

func (s *Service) authorize(ctx context.Context, n int32) (context.Context, int32, string, error) {
	if n == 0 {
		n = 20
	}
	if n < 1 || n > 100 {
		return nil, 0, "", status.Error(codes.InvalidArgument, "limit должен быть 1–100")
	}
	call := rpcmeta.Forward(ctx)
	identity, err := s.User.GetIdentity(call, &upb.Empty{})
	if err != nil {
		return nil, 0, "", err
	}
	return call, n, identity.UserId, nil
}
func (s *Service) People(ctx context.Context, q *pb.RecommendationQuery) (*upb.PeopleList, error) {
	call, n, viewer, err := s.authorize(ctx, q.Limit)
	if err != nil {
		return nil, err
	}
	fallback, err := s.Search.People(call, &upb.PeopleQuery{OpenOnly: true, RandomOrder: true, Page: &upb.Page{Limit: n}})
	if err != nil {
		return nil, err
	}
	ranking := s.load(call, "people", viewer)
	result := &upb.PeopleList{Items: []*upb.Person{}, Total: fallback.Total}
	matches := []Match{}
	seen := map[string]bool{}
	if ranking != nil {
		values, err := validateCandidates(call, ranking.Items, int(n)*2, func(c context.Context, id string) (*upb.Person, error) {
			return s.person(c, viewer, id)
		})
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			if value != nil && len(result.Items) < int(n) {
				result.Items = append(result.Items, value)
				matches = append(matches, ranking.Items[i])
				seen[value.UserId] = true
			}
		}
	}
	for _, value := range fallback.Items {
		if !seen[value.UserId] && len(result.Items) < int(n) {
			result.Items = append(result.Items, value)
			matches = append(matches, fallbackMatch(value.UserId))
			seen[value.UserId] = true
		}
	}
	annotate(ctx, ranking, matches)
	return result, nil
}
func (s *Service) Projects(ctx context.Context, q *pb.RecommendationQuery) (*ppb.ProjectList, error) {
	call, n, viewer, err := s.authorize(ctx, q.Limit)
	if err != nil {
		return nil, err
	}
	fallback, err := s.Search.Projects(call, &ppb.ProjectQuery{RandomOrder: true, Page: &ppb.Page{Limit: n}})
	if err != nil {
		return nil, err
	}
	ranking := s.load(call, "projects", viewer)
	result := &ppb.ProjectList{Items: []*ppb.Project{}, Total: fallback.Total}
	matches := []Match{}
	seen := map[string]bool{}
	if ranking != nil {
		values, err := validateCandidates(call, ranking.Items, int(n)*2, func(c context.Context, id string) (*ppb.Project, error) {
			return s.project(c, viewer, id)
		})
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			if value != nil && len(result.Items) < int(n) {
				entry := ranking.Items[i]
				// Slot changes can invalidate the formerly best role. Drop its stale explanation.
				if entry.RoleID != "" {
					valid := false
					for _, role := range value.Roles {
						valid = valid || (role.Id == entry.RoleID && role.Occupied < role.Slots)
					}
					if !valid {
						entry = fallbackMatch(value.Id)
					}
				}
				result.Items = append(result.Items, value)
				matches = append(matches, entry)
				seen[value.Id] = true
			}
		}
	}
	for _, value := range fallback.Items {
		if !seen[value.Id] && len(result.Items) < int(n) && availableProject(value, viewer) {
			if s.Profiles != nil {
				interaction, err := s.Profiles.CheckInteraction(call, &upb.InteractionQuery{FromUserId: viewer, ToUserId: value.OwnerUserId})
				if err != nil {
					return nil, err
				}
				if interaction.GetBlocked() {
					continue
				}
			}
			result.Items = append(result.Items, value)
			matches = append(matches, fallbackMatch(value.Id))
			seen[value.Id] = true
		}
	}
	annotate(ctx, ranking, matches)
	return result, nil
}

func (s *Service) load(ctx context.Context, kind, viewer string) *Ranking {
	if s.Cache == nil {
		return nil
	}
	result, err := s.Cache.Load(ctx, kind, viewer)
	if err != nil {
		return nil // A cache outage never takes down live catalogue discovery.
	}
	return result
}

func fallbackMatch(id string) Match {
	return Match{ID: id, Factors: []string{"available_catalogue"}, Fallback: true, Model: "fallback-v1"}
}

func annotate(ctx context.Context, ranking *Ranking, matches []Match) {
	result := Ranking{SchemaVersion: 1, Model: "fallback-v1", Items: matches}
	if ranking != nil {
		result.RunID, result.Model, result.GeneratedAt = ranking.RunID, ranking.Model, ranking.GeneratedAt
	}
	raw, err := json.Marshal(result)
	if err == nil {
		// Binary metadata permits Unicode factors without changing existing protobuf messages.
		_ = grpc.SetHeader(ctx, metadata.Pairs("matchlab-recommendations-bin", string(raw)))
	}
}
