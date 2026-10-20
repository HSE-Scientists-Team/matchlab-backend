package profile

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"testing"
)

func TestProfileValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input *pb.ProfileInput
		valid bool
	}{
		{"normal", &pb.ProfileInput{DisplayName: "Анна", Skills: []string{"Go"}, ProjectTypes: []string{"scientific"}}, true},
		{"missing name", &pb.ProfileInput{}, false},
		{"duplicate skill", &pb.ProfileInput{DisplayName: "Анна", Skills: []string{"Go", " go "}}, false},
		{"arbitrary type", &pb.ProfileInput{DisplayName: "Анна", ProjectTypes: []string{"anything"}}, false},
		{"malformed email", &pb.ProfileInput{DisplayName: "Анна", ContactEmail: "Name <a@hse.ru>"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (validateProfile(tc.input) == nil) != tc.valid {
				t.Fatal("unexpected validation result")
			}
		})
	}
}
func TestAchievementValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input *pb.AchievementInput
		valid bool
	}{
		{"scientific", &pb.AchievementInput{Kind: "scientific", Title: "Work", Description: "Contribution", Url: "https://doi.org/10.1/test"}, true},
		{"no description", &pb.AchievementInput{Kind: "scientific", Title: "Work"}, false},
		{"certificate", &pb.AchievementInput{Kind: "certificate", Title: "Certificate", Institution: "HSE"}, true},
		{"unsafe link", &pb.AchievementInput{Kind: "project", Title: "Work", Description: "Text", Url: "javascript:alert(1)"}, false},
		{"stale update", &pb.AchievementInput{Id: "00000000-0000-4000-8000-000000000001", Kind: "project", Title: "Work", Description: "Text"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (validateAchievement(tc.input) == nil) != tc.valid {
				t.Fatal("unexpected validation result")
			}
		})
	}
}
func TestRedactionKeepsVerificationPublic(t *testing.T) {
	a := &pb.Achievement{UserId: "owner", Status: "needs_evidence", ModerationComment: "Private", Details: &pb.AchievementInput{EvidenceFileId: "secret"}}
	redact(a, "owner")
	if a.Details.EvidenceFileId != "secret" {
		t.Fatal("owner lost evidence")
	}
	redact(a, "guest")
	if a.Details.EvidenceFileId != "" || a.ModerationComment != "" || a.Status != "needs_evidence" {
		t.Fatal("privacy violation")
	}
}
func TestModeratorRPCRequiresCredential(t *testing.T) {
	s := &Service{}
	if _, err := s.ListVerificationQueue(context.Background(), &pb.Page{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unprotected queue: %v", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer x", "authorization", "Bearer y"))
	if _, err := s.RecordProjectContext(ctx, &pb.ProjectContext{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ambiguous identity: %v", err)
	}
}

func TestAchievementSources(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		valid     bool
	}{
		{"publication", "https://doi.org/10.1000/example", true},
		{"signed certificate", "https://example.org/certificate?token=sample", true},
		{"article about verification", "https://example.org/articles/verify-email", true},
		{"email confirmation", "https://matchlab.space/verify-email?token=sample", false},
		{"confirmation without token", "https://example.org/verify-email", false},
		{"local confirmation", "http://localhost:5173/verify-email?token=sample", false},
		{"escaped confirmation", "https://example.org/%76erify-email/?token=sample", false},
		{"normalized confirmation", "https://example.org/a/../verify-email?token=sample", false},
		{"HTTP confirmation action", "https://api.matchlab.space/api/v1/auth/email/confirm", false},
		{"credentials in URL", "https://name:password@example.org/certificate", false},
		{"relative", "/certificate", false},
		{"unsafe scheme", "javascript:alert(1)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &pb.AchievementInput{Kind: "certificate", Title: "Certificate", Institution: "Example", Url: tc.url}
			if got := validateAchievement(input) == nil; got != tc.valid {
				t.Fatalf("save validation = %v; want %v", got, tc.valid)
			}
			// Existing rows must not bypass the rule when submitted for moderation.
			err := validateAchievementSubmission(input)
			if (err == nil) != tc.valid {
				t.Fatalf("submission validation = %v; want valid=%v", err, tc.valid)
			}
			if !tc.valid && status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("unexpected code %v", status.Code(err))
			}
		})
	}
	if err := validateAchievementSubmission(&pb.AchievementInput{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("empty evidence accepted: %v", err)
	}
	if err := validateAchievementSubmission(&pb.AchievementInput{EvidenceFileId: "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatalf("file-only proof rejected: %v", err)
	}
}
