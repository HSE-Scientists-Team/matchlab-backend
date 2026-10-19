package http

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeChat struct {
	pb.ChatServiceClient
	calls     int
	err       error
	created   bool
	forwarded bool
	send      *pb.SendMessageRequest
	query     *pb.MessageQuery
	read      *pb.MarkReadRequest
}

func (f *fakeChat) capture(ctx context.Context) {
	f.calls++
	_, deadline := ctx.Deadline()
	md, _ := metadata.FromOutgoingContext(ctx)
	f.forwarded = deadline && len(md.Get("authorization")) == 1 && len(md.Get("x-admin-assertion")) == 0
}
func (f *fakeChat) OpenConversation(ctx context.Context, q *pb.OpenConversationRequest, _ ...grpc.CallOption) (*pb.OpenConversationResponse, error) {
	f.capture(ctx)
	return &pb.OpenConversationResponse{Conversation: &pb.Conversation{Id: "conversation", PeerUserId: q.UserId}, Created: f.created}, f.err
}
func (f *fakeChat) ListConversations(ctx context.Context, _ *pb.ConversationQuery, _ ...grpc.CallOption) (*pb.ConversationList, error) {
	f.capture(ctx)
	return &pb.ConversationList{}, f.err
}
func (f *fakeChat) ListMessages(ctx context.Context, q *pb.MessageQuery, _ ...grpc.CallOption) (*pb.MessageList, error) {
	f.capture(ctx)
	f.query = q
	return &pb.MessageList{}, f.err
}
func (f *fakeChat) SendMessage(ctx context.Context, q *pb.SendMessageRequest, _ ...grpc.CallOption) (*pb.SendMessageResponse, error) {
	f.capture(ctx)
	f.send = q
	return &pb.SendMessageResponse{Message: &pb.Message{Id: "message", Seq: 1, Body: q.Body}, Created: f.created}, f.err
}
func (f *fakeChat) MarkRead(ctx context.Context, q *pb.MarkReadRequest, _ ...grpc.CallOption) (*pb.Empty, error) {
	f.capture(ctx)
	f.read = q
	return &pb.Empty{}, f.err
}
func chatRequest(f *fakeChat, method, path, body, token string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	RegisterChat(router, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", token)
	req.Header.Set("X-Admin-Assertion", "privileged")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}
func TestChatRoutes(t *testing.T) {
	for _, created := range []bool{false, true} {
		f := &fakeChat{created: created}
		want := 200
		if created {
			want = 201
		}
		response := chatRequest(f, "POST", "/api/v1/chat/conversations", `{"user_id":"peer"}`, "Bearer session")
		if response.Code != want || !f.forwarded || !strings.Contains(response.Body.String(), `"peer_user_id":"peer"`) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(response.Code, response.Body)
		}
		response = chatRequest(f, "POST", "/api/v1/chat/conversations/conv/messages", `{"client_message_id":"client","body":"<b>plain text</b>"}`, "Bearer session")
		if response.Code != want || f.send.GetConversationId() != "conv" || f.send.GetBody() != "<b>plain text</b>" || !strings.Contains(response.Body.String(), `"seq":"1"`) {
			t.Fatal(response.Code, response.Body)
		}
	}
	f := &fakeChat{}
	response := chatRequest(f, "GET", "/api/v1/chat/conversations/conv/messages?after_seq=0&limit=12", "", "Bearer session")
	if response.Code != 200 || f.query.AfterSeq == nil || *f.query.AfterSeq != 0 || f.query.Limit != 12 {
		t.Fatal(response.Code, f.query)
	}
	response = chatRequest(f, "PUT", "/api/v1/chat/conversations/conv/read", `{"last_read_seq":5}`, "Bearer session")
	if response.Code != 204 || response.Body.Len() != 0 || f.read.LastReadSeq != 5 {
		t.Fatal(response.Code, response.Body)
	}
	response = chatRequest(f, "GET", "/api/v1/chat/conversations", "", "Bearer session")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatal(response.Code, response.Body)
	}
}
func TestChatRejectsMalformedRequests(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, token string
		want                      int
	}{
		{"GET", "/api/v1/chat/conversations", "", "", 401},
		{"POST", "/api/v1/chat/conversations", `{"user_id":"peer","sender_user_id":"spoof"}`, "Bearer a", 400},
		{"POST", "/api/v1/chat/conversations/id/messages", `{"client_message_id":"client","body":"a","sender_user_id":"spoof"}`, "Bearer a", 400},
		{"GET", "/api/v1/chat/conversations/id/messages?before_seq=1&after_seq=0", "", "Bearer a", 400},
		{"GET", "/api/v1/chat/conversations/id/messages?before_seq=0", "", "Bearer a", 400},
		{"GET", "/api/v1/chat/conversations/id/messages?after_seq=-1", "", "Bearer a", 400},
		{"GET", "/api/v1/chat/conversations?limit=101", "", "Bearer a", 400},
		{"GET", "/api/v1/chat/conversations?limit=abc", "", "Bearer a", 400},
		{"PUT", "/api/v1/chat/conversations/id/read", `{}`, "Bearer a", 400},
		{"PUT", "/api/v1/chat/conversations/id/read", `{"last_read_seq":null}`, "Bearer a", 400},
		{"PUT", "/api/v1/chat/conversations/id/read", `{"last_read_seq":-1}`, "Bearer a", 400},
		{"DELETE", "/api/v1/chat/conversations/id/messages", "", "Bearer a", 405},
		{"PUT", "/api/v1/chat/conversations/id/messages", "", "Bearer a", 405},
	} {
		f := &fakeChat{}
		r := chatRequest(f, tc.method, tc.path, tc.body, tc.token)
		if r.Code != tc.want || f.calls != 0 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, r.Code, r.Body)
		}
	}
}
func TestChatErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.AlreadyExists, 409}, {codes.Internal, 500}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}} {
		f := &fakeChat{err: status.Error(tc.code, "secret storage detail")}
		r := chatRequest(f, "GET", "/api/v1/chat/conversations", "", "Bearer a")
		if r.Code != tc.want || tc.want == 500 && strings.Contains(r.Body.String(), "secret") {
			t.Fatalf("%v: %d %s", tc.code, r.Code, r.Body)
		}
	}
}
