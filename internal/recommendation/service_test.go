package recommendation

import (
	"context"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"testing"
)

type userStub struct{ deny bool }

func (s userStub) GetIdentity(context.Context, *upb.Empty, ...grpc.CallOption) (*upb.Identity, error) {
	if s.deny {
		return nil, status.Error(codes.Unauthenticated, "no session")
	}
	return &upb.Identity{UserId: "user"}, nil
}

type searchStub struct {
	people  *upb.PeopleQuery
	project *ppb.ProjectQuery
	auth    []string
}

func (s *searchStub) People(ctx context.Context, q *upb.PeopleQuery, _ ...grpc.CallOption) (*upb.PeopleList, error) {
	s.people = q
	md, _ := metadata.FromOutgoingContext(ctx)
	s.auth = md.Get("authorization")
	return &upb.PeopleList{}, nil
}
func (s *searchStub) Projects(_ context.Context, q *ppb.ProjectQuery, _ ...grpc.CallOption) (*ppb.ProjectList, error) {
	s.project = q
	return &ppb.ProjectList{}, nil
}
func TestRecommendationsUseRandomOwnerFilteredSampling(t *testing.T) {
	search := &searchStub{}
	s := &Service{Search: search, User: userStub{}}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if _, err := s.People(ctx, &pb.RecommendationQuery{Limit: 7}); err != nil {
		t.Fatal(err)
	}
	if !search.people.RandomOrder || !search.people.OpenOnly || search.people.Page.Limit != 7 || len(search.auth) != 1 || search.auth[0] != "Bearer token" {
		t.Fatal("not randomly sampling eligible people")
	}
	if _, err := s.Projects(ctx, &pb.RecommendationQuery{}); err != nil {
		t.Fatal(err)
	}
	if !search.project.RandomOrder || search.project.Page.Limit != 20 {
		t.Fatal("not sampling projects")
	}
	s.User = userStub{deny: true}
	if _, err := s.People(ctx, &pb.RecommendationQuery{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous personalization")
	}
	if _, err := s.People(ctx, &pb.RecommendationQuery{Limit: 101}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("unbounded results")
	}
}
