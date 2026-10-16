//go:build integration

package project

import (
	"context"
	"database/sql"
	"fmt"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"sync"
	"testing"
	"time"
)

func as(id string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", id))
}
func TestProjectLifecycleAndConcurrentAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:17-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test-only", "POSTGRES_DB": "matchlab"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(h string, p nat.Port) string {
		return fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", h, p.Port())
	})}, Started: true})
	if container != nil {
		defer container.Terminate(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432/tcp")
	db, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", host, port.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	users := &stubUser{}
	s := &Service{Repo: &Repository{DB: db}, Users: users, SyncToken: "test-service-token"}
	owner, a, b, admin := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	own, one, two, mod := as(owner), as(a), as(b), as("admin:"+admin)
	check := func(err error, code codes.Code) {
		t.Helper()
		if status.Code(err) != code {
			t.Fatalf("expected %s, got %v", code, err)
		}
	}
	p, err := s.Create(own, &pb.ProjectInput{Title: "Computer vision", Type: "scientific", Field: "CS", Problem: "Campus navigation", Goal: "Build CV", Tags: []string{"Python"}, PlannedStart: "2027-01", PlannedEnd: "2027-03", Roles: []*pb.RoleInput{{Title: "Engineer", Slots: 1, Skills: []string{"Python"}}, {Title: "Designer", Slots: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.Repo.List(ctx, &pb.ProjectQuery{}, "", false, 20, 0, false, false); e != nil {
		t.Fatalf("catalog SQL: %v", e)
	}
	catalog, err := s.List(ctx, &pb.ProjectQuery{Text: "vision", Type: "scientific", Tags: []string{"Python"}, Duration: "up_to_3m"})
	if err != nil || len(catalog.Items) != 1 || catalog.Total != 1 {
		t.Fatalf("catalog %v %v", catalog, err)
	}
	if len(catalog.Items[0].Members) != 0 {
		t.Fatal("public team leaked")
	}
	var role string
	for _, r := range p.Roles {
		if r.Title == "Engineer" {
			role = r.Id
		}
	}
	j1, err := s.Apply(one, &pb.JoinInput{ProjectId: p.Id, RoleId: role, Message: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(one, &pb.JoinInput{ProjectId: p.Id, RoleId: role})
	check(err, codes.Aborted)
	j2, err := s.Apply(two, &pb.JoinInput{ProjectId: p.Id, RoleId: role})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ListJoins(one, &pb.JoinQuery{ProjectId: p.Id})
	check(err, codes.PermissionDenied)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, j := range []*pb.JoinRequest{j1, j2} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, e := s.DecideJoin(own, &pb.JoinDecision{Id: key, Accept: true})
			results <- e
		}(j.Id)
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for e := range results {
		if e == nil {
			won++
		} else if status.Code(e) == codes.Aborted {
			lost++
		} else {
			t.Fatal(e)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal("role overbooked")
	}
	p, err = s.Get(own, &pb.ProjectID{Id: p.Id})
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	candidate := ""
	for _, m := range p.Members {
		if m.UserId != owner {
			active++
			candidate = m.UserId
		}
	}
	if active != 1 {
		t.Fatal("invalid team")
	}
	_, err = s.Transition(one, &pb.TransitionRequest{Id: p.Id, Action: "start", ExpectedRevision: p.Revision})
	check(err, codes.PermissionDenied)
	_, err = s.Moderate(one, &pb.ModerationInput{ProjectId: p.Id, Hide: true, Reason: "test", ExpectedRevision: p.Revision})
	check(err, codes.PermissionDenied)
	p, err = s.Moderate(mod, &pb.ModerationInput{ProjectId: p.Id, Hide: true, Reason: "Hidden", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, &pb.ProjectID{Id: p.Id})
	check(err, codes.NotFound)
	catalog, err = s.List(ctx, &pb.ProjectQuery{})
	if err != nil || len(catalog.Items) != 0 {
		t.Fatal("hidden in search")
	}
	p, err = s.Moderate(mod, &pb.ModerationInput{ProjectId: p.Id, Hide: false, Reason: "Restored", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.RemoveMember(own, &pb.MemberID{ProjectId: p.Id, UserId: candidate})
	if err != nil {
		t.Fatal(err)
	}
	users.blocked = true
	_, err = s.Invite(own, &pb.JoinInput{ProjectId: p.Id, RoleId: role, CandidateUserId: candidate})
	check(err, codes.PermissionDenied)
	users.blocked = false
	invitation, err := s.Invite(own, &pb.JoinInput{ProjectId: p.Id, RoleId: role, CandidateUserId: candidate})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DecideJoin(own, &pb.JoinDecision{Id: invitation.Id, Accept: true})
	check(err, codes.PermissionDenied)
	if _, err = s.DecideJoin(as(candidate), &pb.JoinDecision{Id: invitation.Id, Accept: true}); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Get(own, &pb.ProjectID{Id: p.Id})
	p, err = s.Transition(own, &pb.TransitionRequest{Id: p.Id, Action: "unpublish", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, &pb.ProjectID{Id: p.Id})
	check(err, codes.NotFound)
	p, err = s.Transition(own, &pb.TransitionRequest{Id: p.Id, Action: "publish", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	recommendation, err := s.List(own, &pb.ProjectQuery{RandomOrder: true})
	if err != nil || len(recommendation.Items) != 0 {
		t.Fatal("recommended own project")
	}
	p, err = s.Transition(own, &pb.TransitionRequest{Id: p.Id, Action: "start", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(two, &pb.JoinInput{ProjectId: p.Id, RoleId: role})
	check(err, codes.Aborted)
	p, err = s.Transition(own, &pb.TransitionRequest{Id: p.Id, Action: "complete", ExpectedRevision: p.Revision})
	if err != nil || p.CompletedAtUnix == 0 {
		t.Fatalf("completion %v", err)
	}
	_, err = s.Leave(as(candidate), &pb.ProjectID{Id: p.Id})
	check(err, codes.Aborted)
	users.failSync = true
	if s.DispatchOnce(ctx) == nil {
		t.Fatal("sync failure hidden")
	}
	var pending int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM projects.context_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil || pending == 0 {
		t.Fatal("lost outbox")
	}
	users.failSync = false
	for i := 0; i < 10; i++ {
		if err = s.DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM projects.context_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("outbox not drained")
	}
	last := users.contexts[len(users.contexts)-1]
	if last.State != "completed" || len(last.ParticipantUserIds) != 2 || last.SourceRevision != int64(p.Revision) {
		t.Fatal("invalid final team snapshot")
	}
	history, err := s.ModerationHistory(mod, &pb.ProjectID{Id: p.Id})
	if err != nil || len(history.Items) != 2 {
		t.Fatal("moderation audit lost")
	}
}
