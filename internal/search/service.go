package search

import (
	"context"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/rpcmeta"
	"google.golang.org/grpc"
)

type People interface {
	SearchPeople(context.Context, *upb.PeopleQuery, ...grpc.CallOption) (*upb.PeopleList, error)
}
type Projects interface {
	List(context.Context, *ppb.ProjectQuery, ...grpc.CallOption) (*ppb.ProjectList, error)
}
type Service struct {
	pb.UnimplementedSearchServiceServer
	User    People
	Project Projects
}

// Owners enforce visibility, blocks, pagination and validation. No private data
// or replica of another service's database is stored here.
func (s *Service) People(ctx context.Context, q *upb.PeopleQuery) (*upb.PeopleList, error) {
	return s.User.SearchPeople(rpcmeta.Forward(ctx), q)
}
func (s *Service) Projects(ctx context.Context, q *ppb.ProjectQuery) (*ppb.ProjectList, error) {
	return s.Project.List(rpcmeta.Forward(ctx), q)
}
