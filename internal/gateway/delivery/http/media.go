package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type MediaClient interface {
	DeleteFile(context.Context, *mediav1.DeleteFileRequest, ...grpc.CallOption) (*mediav1.DeleteFileResponse, error)
	CreateMultipart(context.Context, *mediav1.CreateUploadRequest, ...grpc.CallOption) (*mediav1.MultipartState, error)
	GetMultipart(context.Context, *mediav1.GetFileRequest, ...grpc.CallOption) (*mediav1.MultipartState, error)
	CreatePartURLs(context.Context, *mediav1.CreatePartURLsRequest, ...grpc.CallOption) (*mediav1.CreatePartURLsResponse, error)
	CompleteMultipart(context.Context, *mediav1.CompleteMultipartRequest, ...grpc.CallOption) (*mediav1.CompleteUploadResponse, error)
	AbortMultipart(context.Context, *mediav1.CompleteUploadRequest, ...grpc.CallOption) (*mediav1.AbortMultipartResponse, error)
	CreateUpload(context.Context, *mediav1.CreateUploadRequest, ...grpc.CallOption) (*mediav1.CreateUploadResponse, error)
	CompleteUpload(context.Context, *mediav1.CompleteUploadRequest, ...grpc.CallOption) (*mediav1.CompleteUploadResponse, error)
	GetFile(context.Context, *mediav1.GetFileRequest, ...grpc.CallOption) (*mediav1.GetFileResponse, error)
	CreateDownloadURL(context.Context, *mediav1.CreateDownloadURLRequest, ...grpc.CallOption) (*mediav1.CreateDownloadURLResponse, error)
}

type mediaHandler struct {
	media  MediaClient
	auth   AuthClient
	user   UserClient
	logger *slog.Logger
}

func RegisterMedia(router *mux.Router, media MediaClient, auth AuthClient, user UserClient, logger *slog.Logger) {
	h := &mediaHandler{media: media, auth: auth, user: user, logger: logger}
	routes := router.PathPrefix("/api/v1/media/files").Subrouter()
	routes.Use(h.authorize)
	routes.HandleFunc("/multipart", h.createMultipart).Methods(http.MethodPost)
	routes.HandleFunc("/{id}/multipart", h.getMultipart).Methods(http.MethodGet)
	routes.HandleFunc("/{id}/multipart", h.abortMultipart).Methods(http.MethodDelete)
	routes.HandleFunc("/{id}/multipart/part-urls", h.partURLs).Methods(http.MethodPost)
	routes.HandleFunc("/{id}/multipart/complete", h.completeMultipart).Methods(http.MethodPost)
	routes.HandleFunc("", h.create).Methods(http.MethodPost)
	routes.HandleFunc("/{id}/complete", h.complete).Methods(http.MethodPost)
	routes.HandleFunc("/{id}", h.get).Methods(http.MethodGet)
	routes.HandleFunc("/{id}", h.delete).Methods(http.MethodDelete)
	routes.HandleFunc("/{id}/download-url", h.download).Methods(http.MethodGet)
}

// Read requests may be anonymous. A supplied credential must still be valid.
func (h *mediaHandler) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/multipart") && len(r.Header.Values("Authorization")) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "отсутствует или некорректен токен доступа")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
		result, err := h.auth.ValidateSession(ctx, &authv1.ValidateSessionRequest{Token: token})
		cancel()
		if err != nil {
			if status.Code(err) == codes.Unauthenticated {
				writeError(w, http.StatusUnauthorized, "недействительный сеанс")
			} else {
				writeError(w, http.StatusServiceUnavailable, "сервис Auth недоступен")
			}
			return
		}
		if result.GetUserId() == "" {
			writeError(w, http.StatusUnauthorized, "недействительный сеанс")
			return
		}
		if !requireVerifiedEmail(w, r, h.user, result.GetUserId()) {
			return
		}
		ctx = context.WithValue(r.Context(), authenticatedUserKey{}, result.GetUserId())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type mediaFile struct {
	ID             string `json:"id"`
	OwnerUserID    string `json:"owner_user_id"`
	OriginalName   string `json:"original_name"`
	ContentType    string `json:"content_type"`
	SizeBytes      *int64 `json:"size_bytes,omitempty"`
	Status         string `json:"status"`
	CreatedAtUnix  int64  `json:"created_at_unix"`
	UploadedAtUnix *int64 `json:"uploaded_at_unix,omitempty"`
	IsPublic       bool   `json:"is_public"`
}

func mediaFileJSON(file *mediav1.File) mediaFile {
	return mediaFile{ID: file.GetId(), OwnerUserID: file.GetOwnerUserId(), OriginalName: file.GetOriginalName(),
		ContentType: file.GetContentType(), SizeBytes: file.SizeBytes,
		Status:        strings.ToLower(strings.TrimPrefix(file.GetStatus().String(), "FILE_STATUS_")),
		CreatedAtUnix: file.GetCreatedAtUnix(), UploadedAtUnix: file.UploadedAtUnix, IsPublic: file.GetIsPublic()}
}

const mediaCallTimeout = 10 * time.Second

func (h *mediaHandler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	_, err := h.media.DeleteFile(ctx, &mediav1.DeleteFileRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *mediaHandler) create(w http.ResponseWriter, r *http.Request) {
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
	result, err := h.media.CreateUpload(ctx, &mediav1.CreateUploadRequest{UserId: AuthenticatedUserID(r.Context()),
		OriginalName: req.OriginalName, ContentType: req.ContentType, SizeBytes: req.SizeBytes, IsPublic: req.IsPublic})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/media/files/"+result.GetFile().GetId())
	writeJSON(w, http.StatusCreated, struct {
		File          mediaFile         `json:"file"`
		UploadURL     string            `json:"upload_url"`
		Method        string            `json:"method"`
		Headers       map[string]string `json:"headers"`
		ExpiresAtUnix int64             `json:"expires_at_unix"`
	}{mediaFileJSON(result.GetFile()), result.GetUploadUrl(), result.GetMethod(), result.GetHeaders(), result.GetExpiresAtUnix()})
}

func (h *mediaHandler) complete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		File mediaFile `json:"file"`
	}{mediaFileJSON(result.GetFile())})
}

func (h *mediaHandler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.GetFile(ctx, &mediav1.GetFileRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		File mediaFile `json:"file"`
	}{mediaFileJSON(result.GetFile())})
}

func (h *mediaHandler) download(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), mediaCallTimeout)
	defer cancel()
	result, err := h.media.CreateDownloadURL(ctx, &mediav1.CreateDownloadURLRequest{UserId: AuthenticatedUserID(r.Context()), FileId: mux.Vars(r)["id"]})
	if err != nil {
		h.rpcError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DownloadURL   string `json:"download_url"`
		ExpiresAtUnix int64  `json:"expires_at_unix"`
	}{result.GetDownloadUrl(), result.GetExpiresAtUnix()})
}

func (h *mediaHandler) rpcError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "вызов Media", "request_id", RequestID(r.Context()), "grpc_code", status.Code(err))
	code, message := http.StatusInternalServerError, "внутренняя ошибка сервера"
	switch status.Code(err) {
	case codes.InvalidArgument:
		code, message = http.StatusBadRequest, "некорректные параметры файла"
	case codes.NotFound:
		code, message = http.StatusNotFound, "файл не найден"
	case codes.AlreadyExists:
		code, message = http.StatusConflict, "файл уже существует"
	case codes.FailedPrecondition:
		code, message = http.StatusConflict, "файл не готов или загруженный объект не соответствует требованиям"
	case codes.PermissionDenied:
		code, message = http.StatusForbidden, "доступ запрещён"
	case codes.Unauthenticated:
		code, message = http.StatusUnauthorized, "требуется сеанс"
	case codes.Unavailable:
		code, message = http.StatusServiceUnavailable, "сервис Media недоступен"
	case codes.DeadlineExceeded:
		code, message = http.StatusGatewayTimeout, "превышено время ожидания Media"
	case codes.Canceled:
		code, message = http.StatusRequestTimeout, "запрос отменён"
	}
	writeError(w, code, message)
}
