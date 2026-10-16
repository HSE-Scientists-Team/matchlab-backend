package project

import (
	"context"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"sync"
)

type stubUser struct {
	mu       sync.Mutex
	blocked  bool
	failSync bool
	contexts []*upb.ProjectContext
}

func (*stubUser) GetIdentity(ctx context.Context, _ *upb.Empty, _ ...grpc.CallOption) (*upb.Identity, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	v := md.Get("authorization")
	if len(v) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing")
	}
	role := "client"
	id := v[0]
	if len(id) > 6 && id[:6] == "admin:" {
		role = "administrator"
		id = id[6:]
	}
	return &upb.Identity{UserId: id, Role: role}, nil
}
func (*stubUser) GetProfile(_ context.Context, q *upb.ProfileQuery, _ ...grpc.CallOption) (*upb.Profile, error) {
	return &upb.Profile{UserId: q.UserId, Details: &upb.ProfileInput{DisplayName: "Name"}}, nil
}
func (s *stubUser) CheckInteraction(context.Context, *upb.InteractionQuery, ...grpc.CallOption) (*upb.InteractionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &upb.InteractionResult{Blocked: s.blocked}, nil
}
func (*stubUser) ParticipationVisibility(context.Context, *upb.UserID, ...grpc.CallOption) (*upb.ParticipationFlags, error) {
	return &upb.ParticipationFlags{}, nil
}
func (s *stubUser) RecordProjectContext(ctx context.Context, q *upb.ProjectContext, _ ...grpc.CallOption) (*upb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSync {
		return nil, status.Error(codes.Unavailable, "offline")
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md.Get("x-matchlab-project-token")) != 1 {
		return nil, status.Error(codes.PermissionDenied, "missing service token")
	}
	s.contexts = append(s.contexts, q)
	return &upb.Empty{}, nil
}
