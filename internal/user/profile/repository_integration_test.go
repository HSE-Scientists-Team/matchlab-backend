//go:build integration

package profile

import (
	"context"
	"database/sql"
	"fmt"
	auth "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	media "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"sync"
	"testing"
	"time"
)

type authStub struct{}

func (authStub) ValidateSession(_ context.Context, p *auth.ValidateSessionRequest, _ ...grpc.CallOption) (*auth.ValidateSessionResponse, error) {
	if !validID(p.Token) {
		return nil, status.Error(codes.Unauthenticated, "bad token")
	}
	return &auth.ValidateSessionResponse{UserId: p.Token}, nil
}

type mediaStub struct{ owner, file string }

func (m mediaStub) GetFile(_ context.Context, p *media.GetFileRequest, _ ...grpc.CallOption) (*media.GetFileResponse, error) {
	if p.FileId != m.file {
		return nil, status.Error(codes.NotFound, "missing")
	}
	return &media.GetFileResponse{File: &media.File{Id: m.file, OwnerUserId: m.owner, Status: media.FileStatus_FILE_STATUS_READY, ContentType: "application/pdf"}}, nil
}
func (mediaStub) CreateDownloadURL(context.Context, *media.CreateDownloadURLRequest, ...grpc.CallOption) (*media.CreateDownloadURLResponse, error) {
	return &media.CreateDownloadURLResponse{DownloadUrl: "https://example.test/signed", ExpiresAtUnix: 123}, nil
}
func caller(id string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+id))
}
func TestProfileLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:17-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test-only", "POSTGRES_DB": "matchlab"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port nat.Port) string {
		return fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", host, port.Port())
	})}, Started: true})
	if c != nil {
		defer c.Terminate(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	db, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", host, port.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	owner, other, mod := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for i, id := range []string{owner, other, mod} {
		email := fmt.Sprintf("user%d@hse.ru", i)
		if _, err = db.ExecContext(ctx, `INSERT INTO users.user_account(id,email,password_hash) VALUES($1,$2,'hash')`, id, email); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO users.user_email(user_id,email) VALUES($1,$2)`, id, email); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, `UPDATE users.user_account SET role='moderator' WHERE id=$1`, mod); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{DB: db}
	s := &Service{Repo: repo, Auth: authStub{}, Media: mediaStub{owner: owner, file: uuid.NewString()}}
	own, another, moderator := caller(owner), caller(other), caller(mod)
	mustCode := func(err error, code codes.Code) {
		t.Helper()
		if status.Code(err) != code {
			t.Fatalf("want %s got %v", code, err)
		}
	}
	p, err := s.UpdateProfile(own, &pb.ProfileInput{DisplayName: "Анна", Skills: []string{"Go"}, ContactEmail: "anna@hse.ru", Telegram: "anna"})
	if err != nil || !p.Verified {
		t.Fatalf("profile: %v %v", p, err)
	}
	guest, err := s.GetProfile(ctx, &pb.ProfileQuery{UserId: owner})
	if err != nil || guest.Details.ContactEmail != "" {
		t.Fatalf("contact leak: %v %v", guest, err)
	}
	if _, err = s.ListVerificationQueue(own, &pb.Page{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary user queue access: %v", err)
	}
	a, err := s.SaveAchievement(own, &pb.AchievementInput{Kind: "scientific", Title: "Paper", Description: "My work", Url: "https://doi.org/10.1/example"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteAchievement(another, &pb.AchievementID{Id: a.Id})
	mustCode(err, codes.NotFound)
	a, err = s.SubmitAchievement(own, &pb.AchievementID{Id: a.Id})
	if err != nil || a.Status != "pending_review" {
		t.Fatalf("submit: %v", err)
	}
	queue, err := s.ListVerificationQueue(moderator, &pb.Page{})
	if err != nil || len(queue.Items) != 1 {
		t.Fatalf("queue: %v", err)
	}
	// Both moderators observe the same revision; exactly one decision may commit.
	decision := &pb.VerificationDecision{Id: a.Id, ExpectedRevision: a.Revision, Approve: true}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ReviewAchievement(moderator, decision); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if status.Code(e) == codes.Aborted {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("duplicate decisions %d/%d", success, conflict)
	}
	a, err = s.GetAchievement(own, &pb.AchievementID{Id: a.Id})
	if err != nil {
		t.Fatal(err)
	}
	input := a.Details
	input.Title = "Edited paper"
	a, err = s.SaveAchievement(own, input)
	if err != nil || a.Status != "unverified" {
		t.Fatalf("edit did not reset approval: %v", err)
	}
	_, err = s.ReviewAchievement(moderator, decision)
	mustCode(err, codes.Aborted)
	a, err = s.SubmitAchievement(own, &pb.AchievementID{Id: a.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReviewAchievement(moderator, &pb.VerificationDecision{Id: a.Id, ExpectedRevision: a.Revision, Comment: "Add evidence"})
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.SubmitAchievement(own, &pb.AchievementID{Id: a.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReviewAchievement(moderator, &pb.VerificationDecision{Id: a.Id, ExpectedRevision: a.Revision, Approve: true})
	if err != nil {
		t.Fatalf("resubmission failed: %v", err)
	}
	// A moderator cannot approve their own item.
	self, err := s.SaveAchievement(moderator, &pb.AchievementInput{Kind: "certificate", Title: "Course", Institution: "HSE", Url: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	self, err = s.SubmitAchievement(moderator, &pb.AchievementID{Id: self.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReviewAchievement(moderator, &pb.VerificationDecision{Id: self.Id, ExpectedRevision: self.Revision, Approve: true})
	mustCode(err, codes.PermissionDenied)
	evidence := s.Media.(mediaStub).file
	private, err := s.SaveAchievement(own, &pb.AchievementInput{Kind: "certificate", Title: "Private certificate", Institution: "HSE", EvidenceFileId: evidence})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.GetAchievement(ctx, &pb.AchievementID{Id: private.Id})
	if err != nil || read.Details.EvidenceFileId != "" {
		t.Fatalf("evidence leak: %v", err)
	}
	if _, err = s.GetAchievementEvidence(another, &pb.AchievementID{Id: private.Id}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("unprotected evidence")
	}
	if _, err = s.GetAchievementEvidence(moderator, &pb.AchievementID{Id: private.Id}); err != nil {
		t.Fatal(err)
	}
	// Current teams may see contacts, completed teams may only create reviews.
	project := &pb.ProjectContext{ProjectId: uuid.NewString(), Title: "Project", InitiatorUserId: owner, ParticipantUserIds: []string{owner, other}}
	if _, err = s.RecordProjectContext(moderator, project); err != nil {
		t.Fatal(err)
	}
	team, err := s.GetProfile(another, &pb.ProfileQuery{UserId: owner})
	if err != nil || team.Details.ContactEmail == "" {
		t.Fatalf("team contacts: %v", err)
	}
	review := &pb.ReviewInput{ProjectId: project.ProjectId, TargetUserId: owner, Title: "Great", Text: "Great collaboration", Rating: 5}
	_, err = s.CreateReview(another, review)
	mustCode(err, codes.NotFound)
	project.CompletedAtUnix = time.Now().Add(-time.Minute).Unix()
	if _, err = s.RecordProjectContext(moderator, project); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordProjectContext(moderator, project); err != nil {
		t.Fatalf("snapshot not idempotent: %v", err)
	}
	team, err = s.GetProfile(another, &pb.ProfileQuery{UserId: owner})
	if err != nil || team.Details.ContactEmail != "" {
		t.Fatal("historical contacts leaked")
	}
	if _, err = s.CreateReview(another, review); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateReview(another, review)
	mustCode(err, codes.Aborted)
	_, err = s.CreateReview(moderator, review)
	mustCode(err, codes.NotFound)
	reviews, err := s.ListReviews(ctx, &pb.ReviewQuery{UserId: owner})
	if err != nil || len(reviews.Items) != 1 || reviews.Items[0].AuthorRole != "participant" {
		t.Fatalf("reviews: %v %v", reviews, err)
	}
	p, err = s.GetProfile(ctx, &pb.ProfileQuery{UserId: owner})
	if err != nil || p.Rating != 5 || p.ReviewCount != 1 {
		t.Fatalf("rating: %v %v", p, err)
	}
	if _, err = s.BlockUser(own, &pb.UserID{UserId: other}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BlockUser(own, &pb.UserID{UserId: other}); err != nil {
		t.Fatal(err)
	}
	result, err := s.CheckInteraction(another, &pb.InteractionQuery{FromUserId: other, ToUserId: owner})
	if err != nil || !result.Blocked {
		t.Fatal("block ineffective")
	}
	blocks, err := s.ListBlocks(own, &pb.Page{})
	if err != nil || len(blocks.Items) != 1 {
		t.Fatal("blocks duplicated")
	}
	if _, err = s.UnblockUser(own, &pb.UserID{UserId: other}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteAchievement(own, &pb.AchievementID{Id: a.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetAchievement(ctx, &pb.AchievementID{Id: a.Id})
	mustCode(err, codes.NotFound)
	var decisions int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM users.achievement_decision WHERE achievement_id=$1`, a.Id).Scan(&decisions); err != nil || decisions != 3 {
		t.Fatal("audit lost")
	}
}
