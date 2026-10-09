package grpc

import (
	"context"
	"errors"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Media interface {
	CreateUpload(context.Context, string, string, string, int64, bool) (domain.Upload, error)
	CompleteUpload(context.Context, string, string) (domain.File, error)
	GetFile(context.Context, string, string) (domain.File, error)
	CreateDownloadURL(context.Context, string, string) (domain.SignedRequest, error)
}

type Server struct {
	mediav1.UnimplementedMediaServiceServer
	media Media
}

func NewServer(media Media) *Server { return &Server{media: media} }

var _ mediav1.MediaServiceServer = (*Server)(nil)

func (s *Server) CreateUpload(ctx context.Context, req *mediav1.CreateUploadRequest) (*mediav1.CreateUploadResponse, error) {
	result, err := s.media.CreateUpload(ctx, req.GetUserId(), req.GetOriginalName(), req.GetContentType(), req.GetSizeBytes(), req.GetIsPublic())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.CreateUploadResponse{File: toProtoFile(result.File), UploadUrl: result.Request.URL,
		Method: result.Request.Method, Headers: result.Request.Headers, ExpiresAtUnix: result.Request.ExpiresAt.Unix()}, nil
}

func (s *Server) CompleteUpload(ctx context.Context, req *mediav1.CompleteUploadRequest) (*mediav1.CompleteUploadResponse, error) {
	file, err := s.media.CompleteUpload(ctx, req.GetFileId(), req.GetUserId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.CompleteUploadResponse{File: toProtoFile(file)}, nil
}

func (s *Server) GetFile(ctx context.Context, req *mediav1.GetFileRequest) (*mediav1.GetFileResponse, error) {
	file, err := s.media.GetFile(ctx, req.GetFileId(), req.GetUserId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.GetFileResponse{File: toProtoFile(file)}, nil
}

func (s *Server) CreateDownloadURL(ctx context.Context, req *mediav1.CreateDownloadURLRequest) (*mediav1.CreateDownloadURLResponse, error) {
	request, err := s.media.CreateDownloadURL(ctx, req.GetFileId(), req.GetUserId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.CreateDownloadURLResponse{DownloadUrl: request.URL, ExpiresAtUnix: request.ExpiresAt.Unix()}, nil
}

func toProtoFile(file domain.File) *mediav1.File {
	result := &mediav1.File{Id: file.ID, OwnerUserId: file.OwnerUserID, IsPublic: file.IsPublic, OriginalName: file.OriginalName,
		ContentType: file.ContentType, SizeBytes: file.SizeBytes, CreatedAtUnix: file.CreatedAt.Unix()}
	if file.UploadedAt != nil {
		value := file.UploadedAt.Unix()
		result.UploadedAtUnix = &value
	}
	switch file.Status {
	case domain.FileStatusPending:
		result.Status = mediav1.FileStatus_FILE_STATUS_PENDING
	case domain.FileStatusReady:
		result.Status = mediav1.FileStatus_FILE_STATUS_READY
	case domain.FileStatusDeleting:
		result.Status = mediav1.FileStatus_FILE_STATUS_DELETING
	case domain.FileStatusDeleted:
		result.Status = mediav1.FileStatus_FILE_STATUS_DELETED
	case domain.FileStatusFailed:
		result.Status = mediav1.FileStatus_FILE_STATUS_FAILED
	default:
		result.Status = mediav1.FileStatus_FILE_STATUS_UNSPECIFIED
	}
	return result
}

func toGRPCError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "время выполнения запроса истекло")
	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, domain.ErrInvalidArgument.Error())
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, domain.ErrNotFound.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, domain.ErrAlreadyExists.Error())
	case errors.Is(err, domain.ErrInvalidStatus):
		return status.Error(codes.FailedPrecondition, domain.ErrInvalidStatus.Error())
	case errors.Is(err, domain.ErrObjectNotFound):
		return status.Error(codes.FailedPrecondition, domain.ErrObjectNotFound.Error())
	case errors.Is(err, domain.ErrObjectMismatch):
		return status.Error(codes.FailedPrecondition, domain.ErrObjectMismatch.Error())
	case errors.Is(err, domain.ErrStorageUnavailable):
		return status.Error(codes.Unavailable, domain.ErrStorageUnavailable.Error())
	default:
		return status.Error(codes.Internal, "не удалось выполнить операцию с файлом")
	}
}
