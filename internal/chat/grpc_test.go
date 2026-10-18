package chat

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"testing"
	"time"
)

func TestGRPCContractAndCredentials(t *testing.T) {
	service, repo, users := testService()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterChatServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "passthrough:///chat", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewChatServiceClient(conn)
	if _, err := client.ListConversations(ctx, &pb.ConversationQuery{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous RPC", err)
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer session", "x-admin-assertion", "must-not-leave-chat"))
	out, err := client.OpenConversation(ctx, &pb.OpenConversationRequest{UserId: repo.c.HighID})
	if err != nil || !out.Created || out.Conversation.PeerUserId != repo.c.HighID || !users.forwarded {
		t.Fatal("open RPC", out, err)
	}
	zero := int64(0)
	repo.msgs = []Message{{Seq: 1}, {Seq: 2}}
	history, err := client.ListMessages(ctx, &pb.MessageQuery{ConversationId: repo.c.ID, AfterSeq: &zero, Limit: 1})
	if err != nil || !history.HasMore || len(history.Items) != 1 || history.Items[0].Seq != 1 {
		t.Fatal("optional zero lost in protobuf", history, err)
	}
	if _, err := client.MarkRead(ctx, &pb.MarkReadRequest{ConversationId: repo.c.ID, LastReadSeq: 1}); err != nil {
		t.Fatal(err)
	}
}
