package recommendation

import (
	"context"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/rpcmeta"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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
	Search Search
	User   User
}

func (s *Service) authorize(ctx context.Context, n int32) (context.Context, int32, error) {
	if n == 0 {
		n = 20
	}
	if n < 1 || n > 100 {
		return nil, 0, status.Error(codes.InvalidArgument, "limit должен быть 1–100")
	}
	call := rpcmeta.Forward(ctx)
	if _, err := s.User.GetIdentity(call, &upb.Empty{}); err != nil {
		return nil, 0, err
	}
	return call, n, nil
}
func (s *Service) People(ctx context.Context, q *pb.RecommendationQuery) (*upb.PeopleList, error) {
	call, n, err := s.authorize(ctx, q.Limit)
	if err != nil {
		return nil, err
	}
	return s.Search.People(call, &upb.PeopleQuery{OpenOnly: true, RandomOrder: true, Page: &upb.Page{Limit: n}})
}
func (s *Service) Projects(ctx context.Context, q *pb.RecommendationQuery) (*ppb.ProjectList, error) {
	call, n, err := s.authorize(ctx, q.Limit)
	if err != nil {
		return nil, err
	}
	return s.Search.Projects(call, &ppb.ProjectQuery{RandomOrder: true, Page: &ppb.Page{Limit: n}})
}
