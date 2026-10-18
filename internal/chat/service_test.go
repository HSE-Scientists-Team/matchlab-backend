package chat

import (
	"context"
	"errors"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

type fakeUsers struct {
	actor     string
	blocked   bool
	err       error
	calls     int
	forwarded bool
}

func (u *fakeUsers) capture(ctx context.Context) {
	u.calls++
	_, deadline := ctx.Deadline()
	md, _ := metadata.FromOutgoingContext(ctx)
	u.forwarded = deadline && len(md.Get("authorization")) == 1 && len(md.Get("x-admin-assertion")) == 0
}
func (u *fakeUsers) GetIdentity(ctx context.Context, _ *upb.Empty, _ ...grpc.CallOption) (*upb.Identity, error) {
	u.capture(ctx)
	return &upb.Identity{UserId: u.actor}, u.err
}
func (u *fakeUsers) GetProfile(ctx context.Context, q *upb.ProfileQuery, _ ...grpc.CallOption) (*upb.Profile, error) {
	u.capture(ctx)
	return &upb.Profile{UserId: q.UserId}, u.err
}
func (u *fakeUsers) CheckInteraction(ctx context.Context, q *upb.InteractionQuery, _ ...grpc.CallOption) (*upb.InteractionResult, error) {
	u.capture(ctx)
	if q.FromUserId != u.actor {
		return nil, status.Error(codes.PermissionDenied, "wrong actor")
	}
	return &upb.InteractionResult{Blocked: u.blocked}, u.err
}

type fakeStore struct {
	calls     int
	c         Conversation
	msgs      []Message
	err       error
	sendCalls int
	retry     bool
}

func (r *fakeStore) Open(context.Context, string, string) (Conversation, bool, error) {
	r.calls++
	return r.c, true, r.err
}
func (r *fakeStore) Conversation(context.Context, string, string) (Conversation, error) {
	r.calls++
	return r.c, r.err
}
func (r *fakeStore) List(context.Context, string, int32, *Cursor) ([]Conversation, error) {
	r.calls++
	return []Conversation{r.c, r.c}, r.err
}
func (r *fakeStore) Messages(context.Context, string, int32, *int64, *int64) ([]Message, error) {
	r.calls++
	return append([]Message{}, r.msgs...), r.err
}
func (r *fakeStore) Send(ctx context.Context, _ string, _ string, _ string, _ string, allowed func(context.Context) error) (Message, bool, error) {
	r.sendCalls++
	if !r.retry {
		if err := allowed(ctx); err != nil {
			return Message{}, false, err
		}
	}
	return Message{ID: uuid.NewString()}, !r.retry, r.err
}
func (r *fakeStore) MarkRead(context.Context, string, string, int64) error { r.calls++; return r.err }
func caller() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer test-session", "x-admin-assertion", "must-not-be-forwarded"))
}
func testService() (*Service, *fakeStore, *fakeUsers) {
	actor, peer := uuid.NewString(), uuid.NewString()
	repo := &fakeStore{c: Conversation{ID: uuid.NewString(), LowID: actor, HighID: peer, CreatedAt: time.Now()}}
	users := &fakeUsers{actor: actor}
	return &Service{Repo: repo, Users: users}, repo, users
}
func TestAuthenticationAndUserFailures(t *testing.T) {
	s, repo, users := testService()
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "invalid")), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer a", "authorization", "Bearer b"))} {
		_, err := s.ListConversations(ctx, &pb.ConversationQuery{})
		if status.Code(err) != codes.Unauthenticated || repo.calls != 0 || users.calls != 0 {
			t.Fatal("unauthenticated read", err)
		}
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.Unavailable} {
		users.err = status.Error(code, "failure")
		_, err := s.ListConversations(caller(), &pb.ConversationQuery{})
		if status.Code(err) != code || repo.calls != 0 || !users.forwarded {
			t.Fatal("User failure", err)
		}
	}
}
func TestValidationBeforeStorage(t *testing.T) {
	s, r, u := testService()
	id := r.c.ID
	peer := r.c.HighID
	cases := []func() error{
		func() error {
			_, e := s.OpenConversation(caller(), &pb.OpenConversationRequest{UserId: u.actor})
			return e
		},
		func() error {
			_, e := s.OpenConversation(caller(), &pb.OpenConversationRequest{UserId: "bad"})
			return e
		},
		func() error {
			_, e := s.SendMessage(caller(), &pb.SendMessageRequest{ConversationId: id, ClientMessageId: uuid.NewString(), Body: " \n"})
			return e
		},
		func() error {
			_, e := s.SendMessage(caller(), &pb.SendMessageRequest{ConversationId: id, ClientMessageId: uuid.NewString(), Body: strings.Repeat("я", 10001)})
			return e
		},
		func() error {
			_, e := s.SendMessage(caller(), &pb.SendMessageRequest{ConversationId: id, ClientMessageId: uuid.NewString(), Body: "a\x00b"})
			return e
		},
		func() error {
			_, e := s.SendMessage(caller(), &pb.SendMessageRequest{ConversationId: id, ClientMessageId: peer, Body: string([]byte{255})})
			return e
		},
		func() error {
			_, e := s.ListConversations(caller(), &pb.ConversationQuery{Cursor: "invalid"})
			return e
		},
		func() error { _, e := s.ListConversations(caller(), &pb.ConversationQuery{Limit: 101}); return e },
		func() error {
			n := int64(1)
			_, e := s.ListMessages(caller(), &pb.MessageQuery{ConversationId: id, BeforeSeq: &n, AfterSeq: &n})
			return e
		},
		func() error {
			_, e := s.MarkRead(caller(), &pb.MarkReadRequest{ConversationId: id, LastReadSeq: -1})
			return e
		},
	}
	for i, call := range cases {
		if err := call(); status.Code(err) != codes.InvalidArgument || r.calls != 0 || r.sendCalls != 0 {
			t.Fatalf("validation %d: %v", i, err)
		}
	}
}
func TestBlockingAccessAndRetries(t *testing.T) {
	s, r, u := testService()
	u.blocked = true
	_, err := s.OpenConversation(caller(), &pb.OpenConversationRequest{UserId: r.c.HighID})
	if status.Code(err) != codes.PermissionDenied || r.calls != 0 {
		t.Fatal("blocked open", err)
	}
	q := &pb.SendMessageRequest{ConversationId: r.c.ID, ClientMessageId: uuid.NewString(), Body: "hello"}
	_, err = s.SendMessage(caller(), q)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal("blocked send", err)
	}
	r.retry = true
	result, err := s.SendMessage(caller(), q)
	if err != nil || result.Created {
		t.Fatal("retry denied after block", err)
	}
	r.err = ErrMissing
	_, err = s.ListMessages(caller(), &pb.MessageQuery{ConversationId: r.c.ID})
	if status.Code(err) != codes.NotFound {
		t.Fatal("foreign conversation", err)
	}
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{{ErrConflict, codes.AlreadyExists}, {ErrInvalid, codes.InvalidArgument}, {errors.New("database secret"), codes.Internal}, {context.Canceled, codes.Canceled}} {
		if status.Code(failure(tc.err)) != tc.code {
			t.Fatal(tc.err)
		}
	}
}
func TestPaginationAndReadPerspective(t *testing.T) {
	s, r, u := testService()
	out, err := s.ListConversations(caller(), &pb.ConversationQuery{Limit: 1})
	if err != nil || len(out.Items) != 1 || out.NextCursor == "" {
		t.Fatal("list", out, err)
	}
	if _, err := decodeCursor(out.NextCursor, u.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCursor(out.NextCursor, uuid.NewString()); status.Code(err) != codes.InvalidArgument {
		t.Fatal("cursor scope", err)
	}
	r.msgs = []Message{{Seq: 3}, {Seq: 2}, {Seq: 1}}
	history, err := s.ListMessages(caller(), &pb.MessageQuery{ConversationId: r.c.ID, Limit: 2})
	if err != nil || !history.HasMore || history.Items[0].Seq != 2 || history.Items[1].Seq != 3 {
		t.Fatal("latest chronological page", history, err)
	}
	zero := int64(0)
	r.msgs = []Message{{Seq: 1}, {Seq: 2}, {Seq: 3}}
	history, err = s.ListMessages(caller(), &pb.MessageQuery{ConversationId: r.c.ID, Limit: 2, AfterSeq: &zero})
	if err != nil || !history.HasMore || history.Items[0].Seq != 1 || history.Items[1].Seq != 2 {
		t.Fatal("poll chronological page", history, err)
	}
	c := Conversation{LowID: "low", HighID: "high", LowRead: 2, HighRead: 4}
	p := conversation(c, "high")
	if p.PeerUserId != "low" || p.LastReadSeq != 4 || p.PeerLastReadSeq != 2 {
		t.Fatal(p)
	}
}
