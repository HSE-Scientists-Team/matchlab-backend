package http

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type projectClientStub struct {
	pb.ProjectServiceClient
	identity []string
	seen     *pb.ProjectInput
}

func (s *projectClientStub) Create(ctx context.Context, p *pb.ProjectInput, _ ...grpc.CallOption) (*pb.Project, error) {
	s.seen = p
	md, _ := metadata.FromOutgoingContext(ctx)
	s.identity = md.Get("authorization")
	return &pb.Project{Id: "project"}, nil
}
func TestProjectGatewayDoesNotExposeModeration(t *testing.T) {
	r := mux.NewRouter()
	client := &projectClientStub{}
	RegisterProjects(r, client, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/api/v1/projects/x/moderate", "/api/v1/projects/moderation", "/api/v1/projects/x/moderation-history"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if w.Code != 404 && w.Code != 405 {
			t.Fatalf("privileged route exposed %s %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"title":"Test"}`))
	req.Header.Set("Authorization", "Bearer session")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 201 || len(client.identity) != 1 || client.identity[0] != "Bearer session" {
		t.Fatal("lost identity")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"owner_user_id":"spoof"}`)))
	if w.Code != 400 {
		t.Fatal("client controls owner")
	}
}
