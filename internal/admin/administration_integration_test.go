//go:build integration

package admin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	authpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	mediapb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	project "github.com/HSE-Scientists-Team/matchlab-backend/internal/project"
	supportmanagement "github.com/HSE-Scientists-Team/matchlab-backend/internal/support/management"
	usermanagement "github.com/HSE-Scientists-Team/matchlab-backend/internal/user/management"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/profile"
	userrepository "github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"sync"
	"testing"
	"time"
)

type fixtureMedia struct{}

func (fixtureMedia) CreateDownloadURL(_ context.Context, q *mediapb.CreateDownloadURLRequest, _ ...grpc.CallOption) (*mediapb.CreateDownloadURLResponse, error) {
	return &mediapb.CreateDownloadURLResponse{DownloadUrl: "https://example.test/proof", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
}
func (fixtureMedia) GetFile(context.Context, *mediapb.GetFileRequest, ...grpc.CallOption) (*mediapb.GetFileResponse, error) {
	return nil, status.Error(codes.NotFound, "missing")
}

type fixtureAuth struct{}

func (fixtureAuth) ValidateSession(_ context.Context, q *authpb.ValidateSessionRequest, _ ...grpc.CallOption) (*authpb.ValidateSessionResponse, error) {
	return &authpb.ValidateSessionResponse{UserId: q.Token}, nil
}
func rpcFixture(t *testing.T, server *grpc.Server) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	conn, e := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close() })
	return conn
}
func TestAdministrativeLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c, e := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:17-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test-only", "POSTGRES_DB": "matchlab"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(h string, p nat.Port) string {
		return fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", h, p.Port())
	})}, Started: true})
	if c != nil {
		defer c.Terminate(context.Background())
	}
	if e != nil {
		t.Fatal(e)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	db, e := sql.Open("pgx", fmt.Sprintf("postgres://postgres:test-only@%s:%s/matchlab?sslmode=disable", host, port.Port()))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(15)
	if _, e = migrations.Up(ctx, db); e != nil {
		t.Fatal(e)
	}
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("ADMIN_PUBLIC_KEY", base64.StdEncoding.EncodeToString(public))
	root, e := Bootstrap(ctx, db, "admin@hse.ru", "fixture-strong-password")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Bootstrap(ctx, db, "other@hse.ru", "fixture-strong-password"); status.Code(e) != codes.FailedPrecondition {
		t.Fatal("duplicate bootstrap", e)
	}
	us := grpc.NewServer()
	upb.RegisterUserManagementServiceServer(us, &usermanagement.Service{DB: db})
	profiles := &profile.Service{Repo: &profile.Repository{DB: db}, Auth: fixtureAuth{}, Media: fixtureMedia{}}
	upb.RegisterProfileServiceServer(us, profiles)
	uc := rpcFixture(t, us)
	ss := grpc.NewServer()
	spb.RegisterSupportManagementServiceServer(ss, &supportmanagement.Service{DB: db, Media: fixtureMedia{}})
	sc := rpcFixture(t, ss)
	ps := grpc.NewServer()
	ppb.RegisterProjectServiceServer(ps, &project.Service{Repo: &project.Repository{DB: db}})
	pc := rpcFixture(t, ps)
	api := &Service{DB: db, SigningKey: key, Users: upb.NewUserManagementServiceClient(uc), Profiles: upb.NewProfileServiceClient(uc), Support: spb.NewSupportManagementServiceClient(sc), Projects: ppb.NewProjectServiceClient(pc)}
	as := grpc.NewServer(grpc.UnaryInterceptor(api.UnaryInterceptor))
	pb.RegisterAdminServiceServer(as, api)
	client := pb.NewAdminServiceClient(rpcFixture(t, as))
	require := func(err error, code codes.Code) {
		t.Helper()
		if status.Code(err) != code {
			t.Fatalf("want %s, got %v", code, err)
		}
	}
	login := func(email, password string) context.Context {
		t.Helper()
		v, e := client.Login(ctx, &pb.LoginInput{Email: email, Password: password})
		if e != nil {
			t.Fatal(e)
		}
		if len(v.SessionToken) != 43 || v.ExpiresAtUnix <= time.Now().Unix() {
			t.Fatal("invalid session")
		}
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+v.SessionToken))
	}
	rootCtx := login("ADMIN@hse.ru", "fixture-strong-password")
	_, e = client.GetMe(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))), &mpb.Empty{})
	require(e, codes.Unauthenticated)
	_, e = client.UpdateStaff(rootCtx, &pb.StaffUpdate{Id: root, DisplayName: "Root", Role: "moderator", Active: true, ExpectedRevision: 1, Reason: "test"})
	require(e, codes.FailedPrecondition)
	moderator, e := client.CreateStaff(rootCtx, &pb.StaffInput{Email: "mod@hse.ru", DisplayName: "Moderator", Role: "moderator", Password: "fixture-moderator-password"})
	if e != nil {
		t.Fatal(e)
	}
	modCtx := login("mod@hse.ru", "fixture-moderator-password")
	_, e = client.CreateStaff(modCtx, &pb.StaffInput{Email: "forbidden@hse.ru", Role: "administrator", Password: "fixture-strong-password"})
	require(e, codes.PermissionDenied)
	_, e = client.SaveDomain(modCtx, &upb.DomainInput{Domain: "example.org", OrganizationName: "Example", IsActive: true, Reason: "test"})
	require(e, codes.PermissionDenied)
	domain, e := client.SaveDomain(rootCtx, &upb.DomainInput{Domain: "example.org", OrganizationName: "Example", IsActive: true, Reason: "add domain"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.SaveDomain(rootCtx, &upb.DomainInput{Id: domain.Id, Domain: domain.Domain, OrganizationName: "Example", IsActive: false, ExpectedRevision: domain.Revision + 1, Reason: "stale"})
	require(e, codes.Aborted)
	domain, e = client.SaveDomain(rootCtx, &upb.DomainInput{Id: domain.Id, Domain: domain.Domain, OrganizationName: "Example", IsActive: false, ExpectedRevision: domain.Revision, Reason: "disable registration"})
	if e != nil || domain.IsActive {
		t.Fatalf("domain toggle: %v", e)
	}
	if e = userrepository.NewPostgres(db).SaveRegistration(ctx, "new@example.org", "hash", "0123456789012345678901234567890123456789012345678901234567890123", time.Now().Add(time.Hour)); e == nil {
		t.Fatal("inactive domain accepted")
	}
	user := uuid.NewString()
	if _, e = db.ExecContext(ctx, `INSERT INTO users.user_account(id,email,password_hash) VALUES($1,'owner@hse.ru','fixture')`, user); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO users.user_email(user_id,email) VALUES($1,'owner@hse.ru')`, user); e != nil {
		t.Fatal(e)
	}
	managed, e := client.GetUser(rootCtx, &mpb.ID{Id: user})
	if e != nil {
		t.Fatal(e)
	}
	managed, e = client.SetUserStatus(modCtx, &upb.UserStatusChange{Id: user, Status: "blocked", Reason: "abuse", ExpectedRevision: managed.Revision})
	if e != nil || managed.Status != "blocked" {
		t.Fatal("block", e)
	}
	emailState, e := userrepository.NewPostgres(db).GetEmailStatus(ctx, user)
	if e != nil || emailState.Status != "blocked" {
		t.Fatal("old session gateway bypass", emailState, e)
	}
	_, e = profiles.GetSettings(metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+user)), &upb.Empty{})
	require(e, codes.PermissionDenied)
	managed, e = client.SetUserStatus(rootCtx, &upb.UserStatusChange{Id: user, Status: "active", Reason: "appeal accepted", ExpectedRevision: managed.Revision})
	if e != nil {
		t.Fatal(e)
	}
	hist, e := client.UserHistory(rootCtx, &mpb.HistoryQuery{Id: user, Page: &mpb.Page{Limit: 1, Offset: 1}})
	if e != nil || hist.Total != 2 || len(hist.Items) != 1 {
		t.Fatalf("user history: %v %v", hist, e)
	}
	users, e := client.ListUsers(rootCtx, &mpb.Query{Page: &mpb.Page{Limit: 1, Offset: 100}})
	if e != nil || users.Total != 1 || len(users.Items) != 0 {
		t.Fatal("empty-page total", users, e)
	}
	_, e = upb.NewUserManagementServiceClient(uc).ListUsers(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer legacy-public-admin-token")), &mpb.Query{})
	require(e, codes.PermissionDenied)
	ticket := uuid.NewString()
	file := uuid.NewString()
	if _, e = db.ExecContext(ctx, `INSERT INTO support.support_ticket(id,author_user_id,subject,description,target_user_id) VALUES($1,$2,'Abuse','Details',$2)`, ticket, user); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO support.support_ticket_attachment(support_ticket_id,file_id) VALUES($1,$2)`, ticket, file); e != nil {
		t.Fatal(e)
	}
	v, e := client.UpdateTicket(rootCtx, &spb.TicketChange{Id: ticket, Action: "assign", AssignedAdminId: moderator.Id, ExpectedRevision: 1})
	if e != nil || v.AssignedAdminId != moderator.Id {
		t.Fatal("assignment", e)
	}
	note, e := client.AddTicketNote(modCtx, &spb.TicketNote{Id: ticket, Comment: "Checked proof", ExpectedRevision: v.Revision})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.UpdateTicket(rootCtx, &spb.TicketChange{Id: ticket, Action: "resolve", Comment: "Resolved", ExpectedRevision: note.Revision})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch status.Code(err) {
		case codes.OK:
			success++
		case codes.Aborted:
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent resolution", success, conflict)
	}
	closed, e := client.ListTickets(rootCtx, &mpb.Query{Status: "closed", TargetUserId: user})
	if e != nil || closed.Total != 1 || closed.Items[0].ResolutionComment != "Resolved" {
		t.Fatal("closed queue", closed, e)
	}
	ticketHistory, e := client.TicketHistory(rootCtx, &mpb.HistoryQuery{Id: ticket})
	if e != nil {
		t.Fatal(e)
	}
	assignedRecorded := false
	for _, event := range ticketHistory.Items {
		if event.Action == "assign" && event.AssignedAdminId == moderator.Id && event.PreviousAssignedAdminId == "" {
			assignedRecorded = true
		}
	}
	if !assignedRecorded {
		t.Fatal("assignment audit lost")
	}
	download, e := client.DownloadTicketAttachment(rootCtx, &spb.AttachmentQuery{TicketId: ticket, FileId: file})
	if e != nil || download.DownloadUrl == "" {
		t.Fatal("proof", e)
	}
	_, e = client.DownloadTicketAttachment(rootCtx, &spb.AttachmentQuery{TicketId: ticket, FileId: uuid.NewString()})
	require(e, codes.NotFound)
	reopened, e := client.UpdateTicket(modCtx, &spb.TicketChange{Id: ticket, Action: "reopen", Comment: "New evidence", ExpectedRevision: closed.Items[0].Revision})
	if e != nil || reopened.ResolvedAtUnix != 0 {
		t.Fatal("reopen", e)
	}
	achievement := uuid.NewString()
	if _, e = db.ExecContext(ctx, `INSERT INTO users.achievement(id,user_id,kind,data,status,submitted_at) VALUES($1,$2,'certificate','{"kind":"certificate","title":"Course","institution":"HSE","url":"https://example.test"}','pending_review',now())`, achievement, user); e != nil {
		t.Fatal(e)
	}
	a, e := client.ReviewAchievement(modCtx, &upb.VerificationDecision{Id: achievement, Approve: true, ExpectedRevision: 1})
	if e != nil || a.Status != "verified" || a.Revision != 2 {
		t.Fatal("review", a, e)
	}
	_, e = client.ReviewAchievement(rootCtx, &upb.VerificationDecision{Id: achievement, Approve: true, ExpectedRevision: 1})
	require(e, codes.Aborted)
	decisions, e := client.AchievementHistory(rootCtx, &mpb.HistoryQuery{Id: achievement})
	if e != nil || decisions.Total != 1 || decisions.Items[0].ActorAdminId != moderator.Id {
		t.Fatal("decision history", decisions, e)
	}
	projectID := uuid.NewString()
	if _, e = db.ExecContext(ctx, `INSERT INTO projects.project(id,owner_user_id,data,status) VALUES($1,$2,'{"title":"Unpublished project","type":"scientific","field":"CS","problem":"p","goal":"g","planned_start":"2027-01-01","planned_end":"2027-03-01"}','unpublished')`, projectID, user); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO projects.member(project_id,user_id) VALUES($1,$2)`, projectID, user); e != nil {
		t.Fatal(e)
	}
	projects, e := client.ListProjects(rootCtx, &ppb.ModerationQuery{Status: "unpublished"})
	if e != nil || projects.Total != 1 {
		t.Fatal("admin unpublished catalog", projects, e)
	}
	projectDetails, e := client.GetProject(rootCtx, &ppb.ProjectID{Id: projectID})
	if e != nil {
		t.Fatal(e)
	}
	projectDetails, e = client.ModerateProject(modCtx, &ppb.ModerationInput{ProjectId: projectID, Hide: true, Reason: "abuse", ExpectedRevision: projectDetails.Revision})
	if e != nil || !projectDetails.ModerationHidden {
		t.Fatal("admin project moderation", e)
	}
	projectHistory, e := client.ProjectHistory(rootCtx, &mpb.HistoryQuery{Id: projectID})
	if e != nil || projectHistory.Total != 1 || projectHistory.Items[0].ActorAdminId != moderator.Id {
		t.Fatal("admin project history", projectHistory, e)
	}
	stats, e := client.GetStatistics(rootCtx, &mpb.StatisticsQuery{})
	if e != nil || stats.Users == nil || stats.Support == nil || stats.Projects == nil {
		t.Fatal("statistics", e)
	}
	audit, e := client.ListAudit(rootCtx, &mpb.Query{Result: "succeeded", Page: &mpb.Page{Limit: 100}})
	if e != nil || audit.Total < 8 {
		t.Fatal("audit", audit, e)
	}
	staff, e := client.GetStaff(rootCtx, &mpb.ID{Id: moderator.Id})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.ResetStaffPassword(rootCtx, &pb.PasswordReset{Id: moderator.Id, Password: "fixture-reset-password", Reason: "operator reset", ExpectedRevision: staff.Revision + 1})
	require(e, codes.Aborted)
	_, e = client.ResetStaffPassword(rootCtx, &pb.PasswordReset{Id: moderator.Id, Password: "fixture-reset-password", Reason: "operator reset", ExpectedRevision: staff.Revision})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.GetMe(modCtx, &mpb.Empty{})
	require(e, codes.Unauthenticated)
	moderator, e = client.GetStaff(rootCtx, &mpb.ID{Id: moderator.Id})
	if e != nil {
		t.Fatal(e)
	}
	ss.Stop()
	_, e = client.GetStatistics(rootCtx, &mpb.StatisticsQuery{})
	require(e, codes.Unavailable)
	_, e = client.AddTicketNote(rootCtx, &spb.TicketNote{Id: ticket, Comment: "delivery unavailable", ExpectedRevision: reopened.Revision})
	require(e, codes.Unavailable)
	uncertain, e := client.ListAudit(rootCtx, &mpb.Query{Result: "unknown"})
	if e != nil || uncertain.Total != 1 {
		t.Fatal("missing uncertain audit", uncertain, e)
	}
	_, e = client.UpdateStaff(rootCtx, &pb.StaffUpdate{Id: moderator.Id, DisplayName: "Moderator", Role: "moderator", Active: false, ExpectedRevision: moderator.Revision, Reason: "access removed"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.GetMe(modCtx, &mpb.Empty{})
	require(e, codes.Unauthenticated)
	// Per-account throttle survives new connections and hides account existence.
	for i := 0; i < 5; i++ {
		_, e = client.Login(ctx, &pb.LoginInput{Email: "missing@hse.ru", Password: "incorrect-password"})
		require(e, codes.Unauthenticated)
	}
	_, e = client.Login(ctx, &pb.LoginInput{Email: "missing@hse.ru", Password: "incorrect-password"})
	require(e, codes.ResourceExhausted)
	_, e = client.ChangePassword(rootCtx, &pb.PasswordChange{CurrentPassword: "fixture-strong-password", NewPassword: "fixture-changed-password"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.GetMe(rootCtx, &mpb.Empty{})
	require(e, codes.Unauthenticated)
	newRoot := login("admin@hse.ru", "fixture-changed-password")
	_, e = client.Logout(newRoot, &mpb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.GetMe(newRoot, &mpb.Empty{})
	require(e, codes.Unauthenticated)
	rootAgain := login("admin@hse.ru", "fixture-changed-password")
	rootDetails, e := client.GetStaff(rootAgain, &mpb.ID{Id: root})
	if e != nil {
		t.Fatal(e)
	}
	second, e := client.CreateStaff(rootAgain, &pb.StaffInput{Email: "second@hse.ru", DisplayName: "Second", Role: "administrator", Password: "fixture-second-password"})
	if e != nil {
		t.Fatal(e)
	}
	secondCtx := login("second@hse.ru", "fixture-second-password")
	results = make(chan error, 2)
	for _, target := range []struct {
		ctx     context.Context
		account *pb.StaffAccount
	}{{rootAgain, rootDetails}, {secondCtx, second}} {
		wg.Add(1)
		go func(target struct {
			ctx     context.Context
			account *pb.StaffAccount
		}) {
			defer wg.Done()
			_, err := client.UpdateStaff(target.ctx, &pb.StaffUpdate{Id: target.account.Id, DisplayName: target.account.DisplayName, Role: "administrator", Active: false, ExpectedRevision: target.account.Revision, Reason: "self-disable race"})
			results <- err
		}(target)
	}
	wg.Wait()
	close(results)
	success, conflict = 0, 0
	for err := range results {
		switch status.Code(err) {
		case codes.OK:
			success++
		case codes.FailedPrecondition:
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("last administrator race", success, conflict)
	}
	var activeAdmins int
	if e = db.QueryRowContext(ctx, `SELECT count(*) FROM admin.account WHERE active AND role='administrator'`).Scan(&activeAdmins); e != nil || activeAdmins != 1 {
		t.Fatal("last administrator lost", activeAdmins, e)
	}
	provider, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files, goose.WithTableName("users.goose_db_version"), goose.WithDisableGlobalRegistry(true))
	if e != nil {
		t.Fatal(e)
	}
	// Later additive migrations (including ML views) may be reverted first.
	// The audit-retention assertion specifically targets administration version 7.
	if _, e = provider.DownTo(ctx, 7); e != nil {
		t.Fatal("rollback later migrations", e)
	}
	if _, e = provider.Down(ctx); e == nil {
		t.Fatal("rollback erased administrative audit")
	}
	var retained int
	if e = db.QueryRowContext(ctx, `SELECT count(*) FROM admin.audit_event`).Scan(&retained); e != nil || retained < 1 {
		t.Fatal("audit lost on failed rollback", e)
	}
}
