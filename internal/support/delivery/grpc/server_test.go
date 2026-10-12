package grpc

import (
	"context"
	"errors"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

type fakeSupport struct {
	input    domain.NewComplaint
	err      error
	deadline bool
}

func (s *fakeSupport) CreateComplaint(ctx context.Context, input domain.NewComplaint) (domain.Complaint, error) {
	s.input = input
	_, s.deadline = ctx.Deadline()
	return domain.Complaint{ID: "created", Status: "open", CreatedAt: time.Unix(100, 0)}, s.err
}
func TestCreateComplaintGRPC(t *testing.T) {
	fake := &fakeSupport{}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	supportv1.RegisterSupportServiceServer(server, NewServer(fake, slog.New(slog.NewTextHandler(io.Discard, nil))))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "passthrough:///support", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := supportv1.NewSupportServiceClient(conn)
	target := "target"
	request := &supportv1.CreateComplaintRequest{UserId: "author", Subject: "subject", Description: "description", TargetProjectId: &target, FileIds: []string{"file"}}
	response, err := client.CreateComplaint(ctx, request)
	if err != nil || response.GetId() != "created" || response.GetCreatedAtUnix() != 100 || response.GetStatus() != supportv1.SupportTicketStatus_SUPPORT_TICKET_STATUS_OPEN || fake.input.TargetUserID != nil || fake.input.TargetProjectID == nil || *fake.input.TargetProjectID != "target" || len(fake.input.FileIDs) != 1 || !fake.deadline {
		t.Fatalf("RPC: %+v %v %+v", response, err, fake)
	}
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{domain.ErrInvalidArgument, codes.InvalidArgument}, {domain.ErrAttachmentNotFound, codes.NotFound},
		{domain.ErrAttachmentNotReady, codes.FailedPrecondition}, {domain.ErrMediaUnavailable, codes.Unavailable},
		{context.Canceled, codes.Canceled}, {context.DeadlineExceeded, codes.DeadlineExceeded}, {errors.New("secret database error"), codes.Internal},
	} {
		fake.err = tc.err
		_, err := client.CreateComplaint(ctx, request)
		if status.Code(err) != tc.code {
			t.Fatalf("error: got %v want %v", err, tc.code)
		}
		if tc.code == codes.Internal && status.Convert(err).Message() == tc.err.Error() {
			t.Fatal("database details leaked")
		}
	}
}
