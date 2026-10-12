package client

import (
	"context"
	"errors"
	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type fakeMedia struct {
	file     *mediav1.File
	err      error
	req      *mediav1.GetFileRequest
	deadline bool
}

func (m *fakeMedia) GetFile(ctx context.Context, req *mediav1.GetFileRequest, _ ...grpc.CallOption) (*mediav1.GetFileResponse, error) {
	m.req = req
	_, m.deadline = ctx.Deadline()
	return &mediav1.GetFileResponse{File: m.file}, m.err
}
func TestValidateAttachment(t *testing.T) {
	user := "00000000-0000-4000-8000-000000000001"
	id := "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name      string
		file      *mediav1.File
		err, want error
	}{
		{name: "owned ready", file: &mediav1.File{Id: id, OwnerUserId: user, Status: mediav1.FileStatus_FILE_STATUS_READY}},
		{name: "public foreign", file: &mediav1.File{Id: id, OwnerUserId: id, IsPublic: true, Status: mediav1.FileStatus_FILE_STATUS_READY}, want: domain.ErrAttachmentNotFound},
		{name: "pending", file: &mediav1.File{Id: id, OwnerUserId: user, Status: mediav1.FileStatus_FILE_STATUS_PENDING}, want: domain.ErrAttachmentNotReady},
		{name: "not found", err: status.Error(codes.NotFound, "missing"), want: domain.ErrAttachmentNotFound},
		{name: "unavailable", err: status.Error(codes.Unavailable, "down"), want: domain.ErrMediaUnavailable},
		{name: "timeout", err: status.Error(codes.DeadlineExceeded, "timeout"), want: domain.ErrMediaUnavailable},
		{name: "empty response", want: domain.ErrMediaUnavailable},
		{name: "wrong file", file: &mediav1.File{Id: user, OwnerUserId: user}, want: domain.ErrMediaUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMedia{file: tc.file, err: tc.err}
			err := NewMedia(m).ValidateAttachment(context.Background(), user, id)
			if !errors.Is(err, tc.want) || !m.deadline || m.req.GetUserId() != user || m.req.GetFileId() != id {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
