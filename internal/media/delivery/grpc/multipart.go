package grpc

import (
	"context"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

func multipartProto(state domain.MultipartState) *mediav1.MultipartState {
	result := &mediav1.MultipartState{File: toProtoFile(state.File), Status: state.Upload.Status, ExpectedSizeBytes: state.Upload.ExpectedSize, PartSizeBytes: state.Upload.PartSize, PartCount: state.Upload.PartCount(), ExpiresAtUnix: state.Upload.ExpiresAt.Unix(), UploadedBytes: state.UploadedBytes}
	for _, part := range state.Parts {
		result.Parts = append(result.Parts, &mediav1.MultipartPart{PartNumber: part.Number, Etag: part.ETag, SizeBytes: part.SizeBytes})
	}
	return result
}
func (s *Server) CreateMultipart(ctx context.Context, req *mediav1.CreateUploadRequest) (*mediav1.MultipartState, error) {
	state, err := s.media.CreateMultipart(ctx, req.GetUserId(), req.GetOriginalName(), req.GetContentType(), req.GetSizeBytes(), req.GetIsPublic())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return multipartProto(state), nil
}
func (s *Server) GetMultipart(ctx context.Context, req *mediav1.GetFileRequest) (*mediav1.MultipartState, error) {
	state, err := s.media.GetMultipart(ctx, req.GetFileId(), req.GetUserId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return multipartProto(state), nil
}
func (s *Server) CreatePartURLs(ctx context.Context, req *mediav1.CreatePartURLsRequest) (*mediav1.CreatePartURLsResponse, error) {
	parts, err := s.media.PartURLs(ctx, req.GetFileId(), req.GetUserId(), req.GetPartNumbers())
	if err != nil {
		return nil, toGRPCError(err)
	}
	result := &mediav1.CreatePartURLsResponse{}
	for _, part := range parts {
		result.Parts = append(result.Parts, &mediav1.PartURL{PartNumber: part.Number, SizeBytes: part.SizeBytes, UploadUrl: part.Request.URL, Method: part.Request.Method, Headers: part.Request.Headers, ExpiresAtUnix: part.Request.ExpiresAt.Unix()})
	}
	return result, nil
}
func (s *Server) CompleteMultipart(ctx context.Context, req *mediav1.CompleteMultipartRequest) (*mediav1.CompleteUploadResponse, error) {
	parts := make([]domain.Part, 0, len(req.GetParts()))
	for _, part := range req.GetParts() {
		parts = append(parts, domain.Part{Number: part.GetPartNumber(), ETag: part.GetEtag()})
	}
	file, err := s.media.CompleteMultipart(ctx, req.GetFileId(), req.GetUserId(), parts)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.CompleteUploadResponse{File: toProtoFile(file)}, nil
}
func (s *Server) AbortMultipart(ctx context.Context, req *mediav1.CompleteUploadRequest) (*mediav1.AbortMultipartResponse, error) {
	if err := s.media.AbortMultipart(ctx, req.GetFileId(), req.GetUserId()); err != nil {
		return nil, toGRPCError(err)
	}
	return &mediav1.AbortMultipartResponse{}, nil
}
