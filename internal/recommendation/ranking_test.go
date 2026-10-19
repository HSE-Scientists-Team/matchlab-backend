package recommendation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type cacheStub struct {
	ranking *Ranking
	err     error
}

func (s cacheStub) Load(context.Context, string, string) (*Ranking, error) {
	return s.ranking, s.err
}

type catalogueStub struct {
	people   []*upb.Person
	projects []*ppb.Project
}

func (s catalogueStub) People(context.Context, *upb.PeopleQuery, ...grpc.CallOption) (*upb.PeopleList, error) {
	return &upb.PeopleList{Items: s.people, Total: int64(len(s.people))}, nil
}

func (s catalogueStub) Projects(context.Context, *ppb.ProjectQuery, ...grpc.CallOption) (*ppb.ProjectList, error) {
	return &ppb.ProjectList{Items: s.projects, Total: int64(len(s.projects))}, nil
}

type profilesStub struct {
	blocked map[string]bool
	err     error
}

func (s profilesStub) GetProfile(_ context.Context, q *upb.ProfileQuery, _ ...grpc.CallOption) (*upb.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if q.UserId == "removed" {
		return nil, status.Error(codes.NotFound, "deleted")
	}
	return &upb.Profile{UserId: q.UserId, Details: &upb.ProfileInput{DisplayName: "Fresh name", OpenToCollaboration: true,
		ContactEmail: "private@example.org", Telegram: "private-telegram"}}, nil
}

func (s profilesStub) CheckInteraction(_ context.Context, q *upb.InteractionQuery, _ ...grpc.CallOption) (*upb.InteractionResult, error) {
	return &upb.InteractionResult{Blocked: s.blocked[q.ToUserId]}, s.err
}

type projectsStub struct{ values map[string]*ppb.Project }

func (s projectsStub) Get(_ context.Context, q *ppb.ProjectID, _ ...grpc.CallOption) (*ppb.Project, error) {
	p := s.values[q.Id]
	if p == nil {
		return nil, status.Error(codes.NotFound, "gone")
	}
	return p, nil
}

func TestCachedPeopleAreRevalidatedAndNeverExposeContacts(t *testing.T) {
	s := &Service{User: userStub{}, Profiles: profilesStub{blocked: map[string]bool{"blocked": true}},
		Search: catalogueStub{people: []*upb.Person{{UserId: "fill"}}},
		Cache:  cacheStub{ranking: &Ranking{Items: []Match{{ID: "blocked"}, {ID: "removed"}, {ID: "fresh"}}}}}
	result, err := s.People(context.Background(), &pb.RecommendationQuery{Limit: 2})
	if err != nil || len(result.GetItems()) != 2 || result.Items[0].UserId != "fresh" || result.Items[1].UserId != "fill" {
		t.Fatalf("invalid current cards: %v %v", result, err)
	}
	raw, err := protojson.Marshal(result)
	if err != nil || strings.Contains(string(raw), "private") || result.Items[0].DisplayName != "Fresh name" {
		t.Fatalf("cached/private profile leaked: %s %v", raw, err)
	}
}

func TestRedisFailureFallsBackButProfileOutageFailsClosed(t *testing.T) {
	s := &Service{User: userStub{}, Search: catalogueStub{people: []*upb.Person{{UserId: "live"}}},
		Cache: cacheStub{err: errors.New("redis offline")}}
	result, err := s.People(context.Background(), &pb.RecommendationQuery{})
	if err != nil || len(result.GetItems()) != 1 || result.Items[0].UserId != "live" {
		t.Fatalf("no live fallback: %v %v", result, err)
	}
	s.Cache = cacheStub{ranking: &Ranking{Items: []Match{{ID: "candidate"}}}}
	s.Profiles = profilesStub{err: status.Error(codes.Unavailable, "owner unavailable")}
	if _, err := s.People(context.Background(), &pb.RecommendationQuery{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("stale data served through outage: %v", err)
	}
}

func TestProjectEligibilityRecheckedAgainstCurrentOwners(t *testing.T) {
	open := func(id string) *ppb.Project {
		return &ppb.Project{Id: id, OwnerUserId: "owner", Status: "recruiting", Roles: []*ppb.Role{{Id: "role", Slots: 2, Occupied: 1}}}
	}
	values := map[string]*ppb.Project{}
	entries := []Match{}
	for _, id := range []string{"hidden", "full", "own", "member", "blocked", "good"} {
		values[id] = open(id)
		entries = append(entries, Match{ID: id})
	}
	values["hidden"].ModerationHidden = true
	values["full"].Roles[0].Occupied = 2
	values["own"].OwnerUserId = "user"
	values["member"].Members = []*ppb.Member{{UserId: "user", Status: "active"}}
	values["blocked"].OwnerUserId = "blocked-owner"
	s := &Service{User: userStub{}, Search: catalogueStub{}, ProjectClient: projectsStub{values},
		Profiles: profilesStub{blocked: map[string]bool{"blocked-owner": true}}, Cache: cacheStub{ranking: &Ranking{Items: entries}}}
	result, err := s.Projects(context.Background(), &pb.RecommendationQuery{Limit: 3})
	if err != nil || len(result.GetItems()) != 1 || result.Items[0].Id != "good" {
		t.Fatalf("ineligible project survived: %v %v", result, err)
	}
}

func TestCacheDecodingRejectsStaleWrongGenerationAndDuplicateIDs(t *testing.T) {
	now := time.Now()
	valid := Ranking{SchemaVersion: 1, RunID: "run", Model: "baseline-v1", GeneratedAt: now.Format(time.RFC3339Nano),
		Items: []Match{{ID: "00000000-0000-4000-8000-000000000001", Score: 0.7}}}
	check := func(value Ranking, wantError bool) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeRanking(raw, "run", now)
		if (err != nil) != wantError {
			t.Fatalf("decode error=%v wantError=%t", err, wantError)
		}
	}
	check(valid, false)
	stale := valid
	stale.GeneratedAt = now.Add(-40 * time.Hour).Format(time.RFC3339Nano)
	check(stale, true)
	wrong := valid
	wrong.RunID = "other"
	check(wrong, true)
	duplicate := valid
	duplicate.Items = append(append([]Match{}, valid.Items...), valid.Items[0])
	check(duplicate, true)
	invalid := valid
	invalid.Items = []Match{{ID: valid.Items[0].ID, Score: 2}}
	check(invalid, true)
}
