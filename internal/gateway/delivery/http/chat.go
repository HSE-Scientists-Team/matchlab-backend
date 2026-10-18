package http

import (
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"net/http"
	"strconv"
)

type chatHandler struct {
	client pb.ChatServiceClient
	logger *slog.Logger
}

func RegisterChat(router *mux.Router, client pb.ChatServiceClient, logger *slog.Logger) {
	h := &chatHandler{client: client, logger: logger}
	routes := router.PathPrefix("/api/v1/chat").Subrouter()
	routes.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if len(r.Header.Values("Authorization")) != 1 || bearerToken(r) == "" {
				writeError(w, 401, "требуется сеанс")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	routes.HandleFunc("/conversations", h.conversations).Methods("GET", "POST")
	routes.HandleFunc("/conversations/{id}/messages", h.messages).Methods("GET", "POST")
	routes.HandleFunc("/conversations/{id}/read", h.read).Methods("PUT")
}
func (h *chatHandler) reply(w http.ResponseWriter, r *http.Request, value proto.Message, err error, code int) {
	if err != nil {
		code = 500
		switch status.Code(err) {
		case codes.InvalidArgument:
			code = 400
		case codes.Unauthenticated:
			code = 401
		case codes.PermissionDenied:
			code = 403
		case codes.NotFound:
			code = 404
		case codes.AlreadyExists:
			code = 409
		case codes.Unavailable:
			code = 503
		case codes.DeadlineExceeded:
			code = 504
		case codes.Canceled:
			code = 408
		}
		message := status.Convert(err).Message()
		if code == 500 {
			message = "ошибка сервиса Chat"
			h.logger.ErrorContext(r.Context(), "RPC чата", "grpc_code", status.Code(err))
		}
		writeError(w, code, message)
		return
	}
	if code == 204 {
		w.WriteHeader(code)
		return
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(value)
	if err != nil {
		writeError(w, 500, "ошибка ответа")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}
func chatLimit(w http.ResponseWriter, r *http.Request) (int32, bool) {
	if !r.URL.Query().Has("limit") {
		return 0, true
	}
	n, err := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 32)
	if err != nil || n < 1 || n > 100 || len(r.URL.Query()["limit"]) != 1 {
		writeError(w, 400, "limit должен быть от 1 до 100")
		return 0, false
	}
	return int32(n), true
}
func (h *chatHandler) conversations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	if r.Method == "POST" {
		var input struct {
			UserID string `json:"user_id"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		result, err := h.client.OpenConversation(ctx, &pb.OpenConversationRequest{UserId: input.UserID})
		code := 200
		if result.GetCreated() {
			code = 201
		}
		h.reply(w, r, result.GetConversation(), err, code)
		return
	}
	n, ok := chatLimit(w, r)
	if !ok {
		return
	}
	result, err := h.client.ListConversations(ctx, &pb.ConversationQuery{Limit: n, Cursor: r.URL.Query().Get("cursor")})
	h.reply(w, r, result, err, 200)
}
func (h *chatHandler) messages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	id := mux.Vars(r)["id"]
	if r.Method == "POST" {
		var input struct {
			ClientID string `json:"client_message_id"`
			Body     string `json:"body"`
		}
		if !decodeJSONLimit(w, r, &input, 128*1024) {
			return
		}
		result, err := h.client.SendMessage(ctx, &pb.SendMessageRequest{ConversationId: id, ClientMessageId: input.ClientID, Body: input.Body})
		code := 200
		if result.GetCreated() {
			code = 201
		}
		h.reply(w, r, result.GetMessage(), err, code)
		return
	}
	n, ok := chatLimit(w, r)
	if !ok {
		return
	}
	query := &pb.MessageQuery{ConversationId: id, Limit: n}
	for name, target := range map[string]**int64{"before_seq": &query.BeforeSeq, "after_seq": &query.AfterSeq} {
		if values, exists := r.URL.Query()[name]; exists {
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || value < 0 || name == "before_seq" && value == 0 || len(values) != 1 {
				writeError(w, 400, "некорректный номер сообщения")
				return
			}
			*target = &value
		}
	}
	if query.BeforeSeq != nil && query.AfterSeq != nil {
		writeError(w, 400, "before_seq и after_seq несовместимы")
		return
	}
	result, err := h.client.ListMessages(ctx, query)
	h.reply(w, r, result, err, 200)
}
func (h *chatHandler) read(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Seq *int64 `json:"last_read_seq"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Seq == nil || *input.Seq < 0 {
		writeError(w, 400, "требуется last_read_seq >= 0")
		return
	}
	ctx, cancel := profileContext(r)
	defer cancel()
	result, err := h.client.MarkRead(ctx, &pb.MarkReadRequest{ConversationId: mux.Vars(r)["id"], LastReadSeq: *input.Seq})
	h.reply(w, r, result, err, 204)
}
