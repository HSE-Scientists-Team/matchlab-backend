package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/repository"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/gateway/usecase"
	"github.com/gorilla/mux"
)

type Handler struct {
	sessions usecase.SessionService
	logger   *slog.Logger
}

func Register(router *mux.Router, sessions usecase.SessionService, logger *slog.Logger) {
	h := &Handler{sessions: sessions, logger: logger}
	router.HandleFunc("/sessions/current", h.current).Methods(http.MethodGet)
	router.HandleFunc("/sessions/current", h.revoke).Methods(http.MethodDelete)
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func (h *Handler) current(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	userID, err := h.sessions.Get(r.Context(), token)
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, usecase.ErrInvalidToken) {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "read session", "request_id", RequestID(r.Context()), "error", err)
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		UserID string `json:"user_id"`
	}{UserID: userID})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	if err := h.sessions.Revoke(r.Context(), token); err != nil {
		if errors.Is(err, usecase.ErrInvalidToken) {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		h.logger.ErrorContext(r.Context(), "revoke session", "request_id", RequestID(r.Context()), "error", err)
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
