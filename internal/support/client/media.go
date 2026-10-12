package client

import (
	"context"
	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

type MediaClient interface {
	GetFile(context.Context, *mediav1.GetFileRequest, ...grpc.CallOption) (*mediav1.GetFileResponse, error)
}
type Media struct{ client MediaClient }

func NewMedia(client MediaClient) *Media { return &Media{client: client} }

func (m *Media) ValidateAttachment(ctx context.Context, userID, fileID string) error {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := m.client.GetFile(callCtx, &mediav1.GetFileRequest{UserId: userID, FileId: fileID})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch status.Code(err) {
		case codes.NotFound, codes.PermissionDenied:
			return domain.ErrAttachmentNotFound
		default:
			return domain.ErrMediaUnavailable
		}
	}
	file := response.GetFile()
	if file == nil {
		return domain.ErrMediaUnavailable
	}
	owner, ownerErr := uuid.Parse(file.GetOwnerUserId())
	author, authorErr := uuid.Parse(userID)
	returnedID, fileErr := uuid.Parse(file.GetId())
	requestedID, requestErr := uuid.Parse(fileID)
	if ownerErr != nil || authorErr != nil || fileErr != nil || requestErr != nil || returnedID != requestedID {
		return domain.ErrMediaUnavailable
	}
	if owner != author {
		return domain.ErrAttachmentNotFound
	}
	if file.GetStatus() != mediav1.FileStatus_FILE_STATUS_READY {
		return domain.ErrAttachmentNotReady
	}
	return nil
}
