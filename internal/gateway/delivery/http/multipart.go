package http

import (
	"context"
	"net/http"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/gorilla/mux"
)

type multipartPart struct {
	Number    int32  `json:"part_number"`
	ETag      string `json:"etag"`
	SizeBytes int64  `json:"size_bytes"`
}
type multipartState struct {
	File          mediaFile       `json:"file"`
	Status        string          `json:"status"`
	ExpectedSize  int64           `json:"expected_size_bytes"`
	PartSize      int64           `json:"part_size_bytes"`
	PartCount     int32           `json:"part_count"`
	ExpiresAt     int64           `json:"expires_at_unix"`
	UploadedBytes int64           `json:"uploaded_bytes"`
	Parts         []multipartPart `json:"parts"`
}

func multipartJSON(state *mediav1.MultipartState) multipartState {
	result := multipartState{File: mediaFileJSON(state.GetFile()), Status: state.GetStatus(), ExpectedSize: state.GetExpectedSizeBytes(), PartSize: state.GetPartSizeBytes(), PartCount: state.GetPartCount(), ExpiresAt: state.GetExpiresAtUnix(), UploadedBytes: state.GetUploadedBytes(), Parts: []multipartPart{}}
	for _, part := range state.GetParts() {
		result.Parts = append(result.Parts, multipartPart{Number: part.GetPartNumber(), ETag: part.GetEtag(), SizeBytes: part.GetSizeBytes()})
	}
	return result
}
func (h *mediaHandler) createMultipart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OriginalName string `json:"original_name"`
		ContentType  string `json:"content_type"`
		SizeBytes    int64  `json:"size_bytes"`
		IsPublic     bool   `json:"is_public"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.CreateMultipart(ctx, &mediav1.CreateUploadRequest{UserId: AuthenticatedUserID(r.Context()), OriginalName: req.OriginalName, ContentType: req.ContentType, SizeBytes: req.SizeBytes, IsPublic: req.IsPublic})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/media/files/"+result.GetFile().GetId()+"/multipart")
	writeJSON(w, http.StatusCreated, multipartJSON(result))
}
func (h *mediaHandler) getMultipart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.GetMultipart(ctx, &mediav1.GetFileRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, multipartJSON(result))
}
func (h *mediaHandler) partURLs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Numbers []int32 `json:"part_numbers"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.CreatePartURLs(ctx, &mediav1.CreatePartURLsRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"], PartNumbers: req.Numbers})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	type partURL struct {
		Number    int32             `json:"part_number"`
		SizeBytes int64             `json:"size_bytes"`
		URL       string            `json:"upload_url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		ExpiresAt int64             `json:"expires_at_unix"`
	}
	parts := make([]partURL, 0, len(result.GetParts()))
	for _, part := range result.GetParts() {
		parts = append(parts, partURL{part.GetPartNumber(), part.GetSizeBytes(), part.GetUploadUrl(), part.GetMethod(), part.GetHeaders(), part.GetExpiresAtUnix()})
	}
	writeJSON(w, http.StatusOK, struct {
		Parts []partURL `json:"parts"`
	}{parts})
}
func (h *mediaHandler) completeMultipart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parts []struct {
			Number int32  `json:"part_number"`
			ETag   string `json:"etag"`
		} `json:"parts"`
	}
	if !decodeJSONLimit(w, r, &req, 1024*1024) {
		return
	}
	parts := make([]*mediav1.MultipartPart, 0, len(req.Parts))
	for _, part := range req.Parts {
		parts = append(parts, &mediav1.MultipartPart{PartNumber: part.Number, Etag: part.ETag})
	}
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.CompleteMultipart(ctx, &mediav1.CompleteMultipartRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"], Parts: parts})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		File mediaFile `json:"file"`
	}{mediaFileJSON(result.GetFile())})
}
func (h *mediaHandler) abortMultipart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	_, err := h.media.AbortMultipart(ctx, &mediav1.CompleteUploadRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
