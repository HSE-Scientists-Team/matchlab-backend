package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	authv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	userv1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthClient interface {
	ValidateSession(context.Context, *authv1.ValidateSessionRequest, ...grpc.CallOption) (*authv1.ValidateSessionResponse, error)
	RevokeSession(context.Context, *authv1.RevokeSessionRequest, ...grpc.CallOption) (*authv1.RevokeSessionResponse, error)
}

type UserClient interface {
	Register(context.Context, *userv1.RegisterRequest, ...grpc.CallOption) (*userv1.RegisterResponse, error)
	Login(context.Context, *userv1.LoginRequest, ...grpc.CallOption) (*userv1.LoginResponse, error)
	ConfirmEmail(context.Context, *userv1.ConfirmEmailRequest, ...grpc.CallOption) (*userv1.ConfirmEmailResponse, error)
	GetEmailStatus(context.Context, *userv1.GetEmailStatusRequest, ...grpc.CallOption) (*userv1.GetEmailStatusResponse, error)
}

type Handler struct {
	auth   AuthClient
	user   UserClient
	logger *slog.Logger
}

const grpcCallTimeout = 3 * time.Second

type authenticatedUserKey struct{}

func AuthenticatedUserID(ctx context.Context) string {
	userID, _ := ctx.Value(authenticatedUserKey{}).(string)
	return userID
}

// AuthenticatedSubrouter создаёт группу маршрутов, которая проверяет сеанс
// по токену и подтверждение email перед вызовом обработчиков. Другие API могут использовать отдельные
// группы и не зависеть от хранилища Auth.
func AuthenticatedSubrouter(parent *mux.Router, prefix string, auth AuthClient, user UserClient, logger *slog.Logger) *mux.Router {
	routes := parent.PathPrefix(prefix).Subrouter()
	routes.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				writeError(w, http.StatusUnauthorized, "отсутствует токен доступа")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
			response, err := auth.ValidateSession(ctx, &authv1.ValidateSessionRequest{Token: token})
			cancel()
			if err != nil {
				if status.Code(err) == codes.Unauthenticated {
					writeError(w, http.StatusUnauthorized, "недействительный сеанс")
					return
				}
				logger.ErrorContext(r.Context(), "проверка сеанса защищённого маршрута", "request_id", RequestID(r.Context()), "grpc_code", status.Code(err))
				writeError(w, http.StatusServiceUnavailable, "сервис Auth недоступен")
				return
			}
			if !requireVerifiedEmail(w, r, user, response.GetUserId()) {
				return
			}
			userCtx := context.WithValue(r.Context(), authenticatedUserKey{}, response.GetUserId())
			next.ServeHTTP(w, r.WithContext(userCtx))
		})
	})
	return routes
}

func Register(router *mux.Router, auth AuthClient, user UserClient, logger *slog.Logger) {
	h := &Handler{auth: auth, user: user, logger: logger}
	// Отдельная группа маршрутов для каждой области API позволяет добавлять
	// обработчики новых сервисов без привязки к маршрутам аутентификации.
	api := router.PathPrefix("/api/v1").Subrouter()
	api.HandleFunc("/auth/register", h.register).Methods(http.MethodPost)
	api.HandleFunc("/auth/login", h.login).Methods(http.MethodPost)
	api.HandleFunc("/auth/email/confirm", h.confirmEmail).Methods(http.MethodPost)
	api.HandleFunc("/sessions/current", h.current).Methods(http.MethodGet)
	api.HandleFunc("/sessions/current", h.revoke).Methods(http.MethodDelete)
	emailRoutes := AuthenticatedSubrouter(api, "/users/me/email", auth, user, logger)
	emailRoutes.HandleFunc("", h.emailStatus).Methods(http.MethodGet)
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var req credentials
	if !decodeJSON(w, r, &req) {
		return credentials{}, false
	}
	return req, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "некорректное тело запроса")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "запрос должен содержать один объект JSON")
		return false
	}
	return true
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	response, err := h.user.Register(ctx, &userv1.RegisterRequest{Email: req.Email, Password: req.Password})
	if err != nil {
		h.logRPCError(r, "регистрация пользователя", err)
		switch status.Code(err) {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, status.Convert(err).Message())
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, status.Convert(err).Message())
		case codes.AlreadyExists:
			writeError(w, http.StatusConflict, "email уже зарегистрирован")
		default:
			writeError(w, http.StatusServiceUnavailable, "сервис User недоступен")
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Status string `json:"status"`
	}{Status: response.GetStatus()})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	response, err := h.user.Login(ctx, &userv1.LoginRequest{Email: req.Email, Password: req.Password})
	if err != nil {
		h.logRPCError(r, "вход пользователя", err)
		switch status.Code(err) {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, status.Convert(err).Message())
		case codes.Unauthenticated:
			writeError(w, http.StatusUnauthorized, "неверный email или пароль")
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, status.Convert(err).Message())
		case codes.FailedPrecondition:
			writeError(w, http.StatusForbidden, "учётная запись неактивна")
		default:
			writeError(w, http.StatusServiceUnavailable, "сервис User недоступен")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		UserID string `json:"user_id"`
		Token  string `json:"access_token"`
	}{UserID: response.GetUserId(), Token: response.GetSessionToken()})
}

func (h *Handler) confirmEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	_, err := h.user.ConfirmEmail(ctx, &userv1.ConfirmEmailRequest{Token: req.Token})
	if err != nil {
		h.logRPCError(r, "подтверждение адреса", err)
		switch status.Code(err) {
		case codes.AlreadyExists:
			writeError(w, http.StatusConflict, "email уже зарегистрирован")
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, "подтверждение почты для этой организации недоступно")
		case codes.DeadlineExceeded:
			writeError(w, http.StatusGone, "срок подтверждения адреса электронной почты истёк")
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "токен подтверждения не найден или уже использован")
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, "недействительный токен подтверждения адреса")
		default:
			writeError(w, http.StatusServiceUnavailable, "подтверждение адреса электронной почты недоступно")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) emailStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	result, err := h.user.GetEmailStatus(ctx, &userv1.GetEmailStatusRequest{UserId: AuthenticatedUserID(r.Context())})
	if err != nil {
		h.logRPCError(r, "получение состояния адреса", err)
		writeError(w, http.StatusServiceUnavailable, "состояние адреса электронной почты недоступно")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Email        string `json:"email,omitempty"`
		Status       string `json:"status"`
		PendingEmail string `json:"pending_email,omitempty"`
	}{Email: result.GetEmail(), Status: result.GetStatus(), PendingEmail: result.GetPendingEmail()})
}

func (h *Handler) current(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "отсутствует токен доступа")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	response, err := h.auth.ValidateSession(ctx, &authv1.ValidateSessionRequest{Token: token})
	if err != nil {
		h.logRPCError(r, "проверка сеанса", err)
		if status.Code(err) == codes.Unauthenticated {
			writeError(w, http.StatusUnauthorized, "недействительный сеанс")
		} else {
			writeError(w, http.StatusServiceUnavailable, "сервис Auth недоступен")
		}
		return
	}
	if !requireVerifiedEmail(w, r, h.user, response.GetUserId()) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		UserID string `json:"user_id"`
	}{UserID: response.GetUserId()})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "отсутствует токен доступа")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	_, err := h.auth.RevokeSession(ctx, &authv1.RevokeSessionRequest{Token: token})
	if err != nil {
		h.logRPCError(r, "отзыв сеанса", err)
		if status.Code(err) == codes.InvalidArgument {
			writeError(w, http.StatusUnauthorized, "недействительный сеанс")
		} else {
			writeError(w, http.StatusServiceUnavailable, "сервис Auth недоступен")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) logRPCError(r *http.Request, operation string, err error) {
	h.logger.ErrorContext(r.Context(), operation, "request_id", RequestID(r.Context()), "grpc_code", status.Code(err))
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, struct {
		Error string `json:"error"`
	}{Error: message})
}

// Проверка в Gateway также закрывает доступ по сеансам, созданным до миграции.
func requireVerifiedEmail(w http.ResponseWriter, r *http.Request, user UserClient, userID string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), grpcCallTimeout)
	defer cancel()
	result, err := user.GetEmailStatus(ctx, &userv1.GetEmailStatusRequest{UserId: userID})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "состояние адреса электронной почты недоступно")
		return false
	}
	if result.GetStatus() != "verified" {
		writeError(w, http.StatusForbidden, "подтвердите адрес электронной почты")
		return false
	}
	return true
}
