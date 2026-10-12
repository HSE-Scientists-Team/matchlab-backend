package http

import (
	"context"
	supportv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log/slog"
	"net/http"
	"time"
)

type SupportClient interface {
	CreateComplaint(context.Context, *supportv1.CreateComplaintRequest, ...grpc.CallOption) (*supportv1.CreateComplaintResponse, error)
}

func RegisterSupport(router *mux.Router, support SupportClient, auth AuthClient, user UserClient, logger *slog.Logger) {
	routes := AuthenticatedSubrouter(router, "/api/v1/complaints", auth, user, logger)
	routes.HandleFunc("", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Subject         string   `json:"subject"`
			Description     string   `json:"description"`
			TargetUserID    *string  `json:"target_user_id"`
			TargetProjectID *string  `json:"target_project_id"`
			FileIDs         []string `json:"file_ids"`
		}
		if !decodeJSONLimit(w, r, &input, 128*1024) {
			return
		}
		if (input.TargetUserID == nil) == (input.TargetProjectID == nil) {
			writeError(w, http.StatusBadRequest, "нужно указать ровно один target_user_id или target_project_id")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		response, err := support.CreateComplaint(ctx, &supportv1.CreateComplaintRequest{
			UserId: AuthenticatedUserID(r.Context()), Subject: input.Subject, Description: input.Description,
			TargetUserId: input.TargetUserID, TargetProjectId: input.TargetProjectID, FileIds: input.FileIDs,
		})
		if err != nil {
			logger.ErrorContext(r.Context(), "создание жалобы", "request_id", RequestID(r.Context()), "grpc_code", status.Code(err))
			switch status.Code(err) {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, status.Convert(err).Message())
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "вложение не найдено или не принадлежит автору")
			case codes.FailedPrecondition:
				writeError(w, http.StatusConflict, "вложение ещё не готово")
			case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
				writeError(w, http.StatusServiceUnavailable, "сервис Support недоступен")
			default:
				writeError(w, http.StatusInternalServerError, "не удалось создать жалобу")
			}
			return
		}
		if response == nil || response.GetId() == "" || response.GetStatus() != supportv1.SupportTicketStatus_SUPPORT_TICKET_STATUS_OPEN {
			writeError(w, http.StatusBadGateway, "некорректный ответ сервиса Support")
			return
		}
		writeJSON(w, http.StatusCreated, struct {
			ID        string    `json:"id"`
			Status    string    `json:"status"`
			CreatedAt time.Time `json:"created_at"`
		}{ID: response.GetId(), Status: "open", CreatedAt: time.Unix(response.GetCreatedAtUnix(), 0).UTC()})
	}).Methods(http.MethodPost)
}
