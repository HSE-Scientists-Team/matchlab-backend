package http

import (
	"context"
	"encoding/json"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type adminMock struct {
	pb.AdminServiceClient
	query          *mpb.Query
	decision       *upb.VerificationDecision
	calls          int
	internalHeader bool
}

func (m *adminMock) ListTickets(ctx context.Context, q *mpb.Query, _ ...grpc.CallOption) (*spb.TicketList, error) {
	m.query = q
	m.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	m.internalHeader = len(md.Get("x-matchlab-admin-assertion")) > 0
	if len(md.Get("authorization")) != 1 {
		return nil, status.Error(codes.Unauthenticated, "session required")
	}
	return &spb.TicketList{Items: []*spb.Ticket{}, Total: 12}, nil
}
func (m *adminMock) ReviewAchievement(_ context.Context, q *upb.VerificationDecision, _ ...grpc.CallOption) (*upb.Achievement, error) {
	m.calls++
	m.decision = q
	return &upb.Achievement{Id: q.Id, Status: "needs_evidence"}, nil
}
func TestAdminHTTPBoundary(t *testing.T) {
	mock := &adminMock{}
	router := mux.NewRouter()
	RegisterAdmin(router, mock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(method, url, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer ordinary-or-staff-token")
		r.Header.Set("x-matchlab-admin-assertion", "forged")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/api/v1/admin/tickets?limit=10&offset=20&status=closed&unassigned_only=true&target_user_id=00000000-0000-4000-8000-000000000001", "")
	if w.Code != 200 || mock.query.Page.Limit != 10 || mock.query.Page.Offset != 20 || mock.query.Status != "closed" || mock.query.TargetUserId == "" || !mock.query.UnassignedOnly || mock.internalHeader {
		t.Fatal(w.Code, w.Body.String(), mock.query)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive response cached")
	}
	for _, url := range []string{"/api/v1/admin/tickets?limit=invalid", "/api/v1/admin/tickets?limit=1&limit=2", "/api/v1/admin/tickets?unknown=1", "/api/v1/admin/tickets?unassigned_only=invalid"} {
		if w = request("GET", url, ""); w.Code != 400 {
			t.Fatal(url, w.Code)
		}
	}
	before := mock.calls
	if w = request("POST", "/api/v1/admin/achievements/00000000-0000-4000-8000-000000000001/decision", `{"expected_revision":1,"comment":"recheck"}`); w.Code != 400 || mock.calls != before {
		t.Fatal("missing decision boolean accepted", w.Code)
	}
	if w = request("POST", "/api/v1/admin/achievements/00000000-0000-4000-8000-000000000001/decision", `{"approve":false,"expected_revision":1,"comment":"recheck"}`); w.Code != 200 || mock.decision.Id != "00000000-0000-4000-8000-000000000001" || mock.decision.Approve {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("POST", "/api/v1/admin/achievements/00000000-0000-4000-8000-000000000001/decision", `{"id":"00000000-0000-4000-8000-000000000002","approve":false,"expected_revision":1,"comment":"recheck"}`); w.Code != 400 {
		t.Fatal("conflicting path ID accepted")
	}
	if w = request("POST", "/api/v1/admin/ReviewAchievement", `{}`); w.Code != 404 {
		t.Fatal("RPC passthrough exposed")
	}
}
func TestAdminRoutesMatchOpenAPI(t *testing.T) {
	root := "../../../../"
	data, e := os.ReadFile(root + "scripts/api/admin-routes.json")
	if e != nil {
		t.Fatal(e)
	}
	var entries [][]json.RawMessage
	if e = json.Unmarshal(data, &entries); e != nil {
		t.Fatal(e)
	}
	var document struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	raw, e := os.ReadFile(root + "api/http/openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if e = yaml.Unmarshal(raw, &document); e != nil {
		t.Fatal(e)
	}
	expected := map[string]bool{}
	for _, entry := range entries {
		var verb, path string
		json.Unmarshal(entry[0], &verb)
		json.Unmarshal(entry[1], &path)
		path = "/api/v1/admin" + path
		expected[verb+" "+path] = true
		if document.Paths[path][strings.ToLower(verb)] == nil {
			t.Fatal("OpenAPI path missing", verb, path)
		}
	}
	router := mux.NewRouter()
	RegisterAdmin(router, &adminMock{}, slog.Default())
	actual := map[string]bool{}
	if e = router.Walk(func(r *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		methods, e := r.GetMethods()
		if e != nil {
			return nil
		}
		path, _ := r.GetPathTemplate()
		for _, verb := range methods {
			actual[verb+" "+path] = true
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if len(actual) != len(expected) {
		t.Fatal("route count differs", len(actual), len(expected))
	}
	for route := range expected {
		if !actual[route] {
			t.Fatal("missing implementation", route)
		}
	}
}
