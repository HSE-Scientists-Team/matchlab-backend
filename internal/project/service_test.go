package project

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type roleUser struct{}

func (roleUser) GetIdentity(context.Context, *upb.Empty, ...grpc.CallOption) (*upb.Identity, error) {
	return &upb.Identity{UserId: "00000000-0000-4000-8000-000000000001", Role: "client"}, nil
}
func TestTransitionsAndPermissions(t *testing.T) {
	s := &Service{Users: &stubUser{}}
	if _, e := s.Moderate(context.Background(), &pb.ModerationInput{}); status.Code(e) != codes.Unauthenticated {
		t.Fatalf("unprotected admin: %v", e)
	}
	p := &pb.Project{OwnerUserId: "owner", Status: "recruiting", Revision: 1}
	if owner(p, "other", 1) != ErrDenied {
		t.Fatal("ownership bypass")
	}
	if owner(p, "owner", 2) != ErrConflict {
		t.Fatal("revision bypass")
	}
	if !visible(p, "") {
		t.Fatal("public catalog blocked")
	}
	p.ModerationHidden = true
	if visible(p, "") {
		t.Fatal("hidden project leaked")
	}
	if !visible(p, "owner") {
		t.Fatal("owner lost access")
	}
}
func TestValidationAndPublicTeamPrivacy(t *testing.T) {
	p := &pb.ProjectInput{Title: "Project", Type: "scientific", Field: "CS", Problem: "Problem", Goal: "Goal", PlannedStart: "2027-01", PlannedEnd: "2027-03", Roles: []*pb.RoleInput{{Title: "Engineer", Slots: 1}}}
	if err := validate(p, true); err != nil {
		t.Fatal(err)
	}
	if p.PlannedStart != "2027-01-01" || p.PlannedEnd != "2027-03-31" {
		t.Fatal("month interval incorrect")
	}
	p.PlannedEnd = "2026-01-01"
	if validate(p, true) == nil {
		t.Fatal("negative interval")
	}
	value := &pb.Project{OwnerUserId: "owner", Members: []*pb.Member{{UserId: "secret", Status: "active"}}, PendingJoinCount: 12}
	sanitize(value, "")
	if len(value.Members) != 0 || value.PendingJoinCount != 0 {
		t.Fatal("private team leaked")
	}
}
