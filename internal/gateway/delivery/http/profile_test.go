package http

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeProfiles struct {
	pb.ProfileServiceClient
	calls int
	auth  []string
}

func (f *fakeProfiles) UpdateProfile(ctx context.Context, p *pb.ProfileInput, _ ...grpc.CallOption) (*pb.Profile, error) {
	f.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	f.auth = md.Get("authorization")
	return &pb.Profile{Details: p}, nil
}
func TestPublicProfileAllowlistAndIdentity(t *testing.T) {
	r := mux.NewRouter()
	client := &fakeProfiles{}
	RegisterProfiles(r, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/api/v1/users/me/verification-queue", "/api/v1/users/me/achievements/x/approve", "/api/v1/users/me/project-context"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if w.Code != 404 {
			t.Fatalf("internal route exposed: %s %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("PUT", "/api/v1/users/me/profile", strings.NewReader(`{"display_name":"Анна"}`))
	req.Header.Set("Authorization", "Bearer session")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || len(client.auth) != 1 || client.auth[0] != "Bearer session" {
		t.Fatalf("identity lost: %d %v", w.Code, client.auth)
	}
	w = httptest.NewRecorder()
	bad := httptest.NewRequest("PUT", "/api/v1/users/me/profile", strings.NewReader(`{"display_name":"Анна","verified":true}`))
	bad.Header.Set("Authorization", "Bearer session")
	r.ServeHTTP(w, bad)
	if w.Code != 400 || client.calls != 1 {
		t.Fatal("client controlled verification")
	}
}

func (f *fakeProfiles) SaveAchievement(_ context.Context, p *pb.AchievementInput, _ ...grpc.CallOption) (*pb.Achievement, error) {
	return &pb.Achievement{Details: p, Status: "unverified"}, nil
}
func TestAchievementBodySupportsUnicodeLimit(t *testing.T) {
	r := mux.NewRouter()
	RegisterProfiles(r, &fakeProfiles{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"kind":"scientific","title":"Paper","description":"` + strings.Repeat("😀", 10000) + `"}`
	req := httptest.NewRequest("POST", "/api/v1/users/me/achievements", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer session")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("valid Unicode text rejected: %d", w.Code)
	}
}
