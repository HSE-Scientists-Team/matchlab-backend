package http

import (
	"context"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Explicit route allowlist: privileged ProfileService RPCs have no HTTP mapping.
func RegisterProfiles(router *mux.Router, client pb.ProfileServiceClient, logger *slog.Logger) {
	h := &profileHandler{client: client, logger: logger}
	r := router.PathPrefix("/api/v1/users").Subrouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if strings.HasPrefix(r.URL.Path, "/api/v1/users/me/") && len(r.Header.Values("Authorization")) == 0 {
				writeError(w, 401, "требуется сеанс")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.HandleFunc("/me/profile", h.profile).Methods("GET", "PUT")
	r.HandleFunc("/me/settings", h.settings).Methods("GET", "PUT")
	r.HandleFunc("/me/blocks", h.blocks).Methods("GET")
	r.HandleFunc("/me/blocks/{id}", h.block).Methods("PUT", "DELETE")
	r.HandleFunc("/me/achievements", h.achievements).Methods("GET", "POST")
	r.HandleFunc("/me/achievements/{id}", h.achievement).Methods("GET", "PUT", "DELETE")
	r.HandleFunc("/me/achievements/{id}/submit", h.submit).Methods("POST")
	r.HandleFunc("/me/reviews", h.reviews).Methods("GET")
	r.HandleFunc("/me/reviews", h.createReview).Methods("POST")
	r.HandleFunc("/{user_id}/profile", h.profile).Methods("GET")
	r.HandleFunc("/{user_id}/achievements", h.achievements).Methods("GET")
	r.HandleFunc("/{user_id}/reviews", h.reviews).Methods("GET")
}

type profileHandler struct {
	client pb.ProfileServiceClient
	logger *slog.Logger
}

func profileContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	if values := r.Header.Values("Authorization"); len(values) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, metadata.MD{"authorization": values})
	}
	return ctx, cancel
}
func profilePage(w http.ResponseWriter, r *http.Request) *pb.Page {
	p := &pb.Page{}
	for name, target := range map[string]*int32{"limit": &p.Limit, "offset": &p.Offset} {
		if v := r.URL.Query().Get(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				writeError(w, 400, "некорректная пагинация")
				return nil
			}
			*target = int32(n)
		}
	}
	return p
}
func (h *profileHandler) reply(w http.ResponseWriter, r *http.Request, value proto.Message, err error, code int) {
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		httpCode := 500
		switch status.Code(err) {
		case codes.InvalidArgument:
			httpCode = 400
		case codes.Unauthenticated:
			httpCode = 401
		case codes.PermissionDenied:
			httpCode = 403
		case codes.NotFound:
			httpCode = 404
		case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
			httpCode = 409
		case codes.Unavailable:
			httpCode = 503
		case codes.DeadlineExceeded:
			httpCode = 504
		case codes.Canceled:
			httpCode = 408
		}
		message := status.Convert(err).Message()
		if httpCode == 500 {
			message = "ошибка сервиса User"
			h.logger.ErrorContext(r.Context(), "RPC профиля", "grpc_code", status.Code(err))
		}
		writeError(w, httpCode, message)
		return
	}
	if code == 204 {
		w.WriteHeader(204)
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
func (h *profileHandler) profile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	if r.Method == "PUT" {
		p := &pb.ProfileInput{}
		if !decodeJSONLimit(w, r, p, 65536) {
			return
		}
		res, err := h.client.UpdateProfile(ctx, p)
		h.reply(w, r, res, err, 200)
		return
	}
	res, err := h.client.GetProfile(ctx, &pb.ProfileQuery{UserId: mux.Vars(r)["user_id"]})
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) settings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	if r.Method == "PUT" {
		p := &pb.PrivacySettings{}
		if !decodeJSON(w, r, p) {
			return
		}
		res, err := h.client.UpdateSettings(ctx, p)
		h.reply(w, r, res, err, 200)
		return
	}
	res, err := h.client.GetSettings(ctx, &pb.Empty{})
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) achievements(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	if r.Method == "POST" {
		p := &pb.AchievementInput{}
		if !decodeJSONLimit(w, r, p, 65536) {
			return
		}
		if p.Id != "" || p.ExpectedRevision != 0 {
			writeError(w, 400, "id и expected_revision недопустимы при создании")
			return
		}
		res, err := h.client.SaveAchievement(ctx, p)
		h.reply(w, r, res, err, 201)
		return
	}
	page := profilePage(w, r)
	if page == nil {
		return
	}
	res, err := h.client.ListAchievements(ctx, &pb.AchievementQuery{UserId: mux.Vars(r)["user_id"], Kind: r.URL.Query().Get("kind"), Page: page})
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) achievement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	id := mux.Vars(r)["id"]
	switch r.Method {
	case "PUT":
		p := &pb.AchievementInput{}
		if !decodeJSONLimit(w, r, p, 65536) {
			return
		}
		if p.Id != "" && p.Id != id {
			writeError(w, 400, "id не соответствует пути")
			return
		}
		p.Id = id
		res, err := h.client.SaveAchievement(ctx, p)
		h.reply(w, r, res, err, 200)
	case "DELETE":
		res, err := h.client.DeleteAchievement(ctx, &pb.AchievementID{Id: id})
		h.reply(w, r, res, err, 204)
	default:
		res, err := h.client.GetAchievement(ctx, &pb.AchievementID{Id: id})
		h.reply(w, r, res, err, 200)
	}
}
func (h *profileHandler) submit(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	res, err := h.client.SubmitAchievement(ctx, &pb.AchievementID{Id: mux.Vars(r)["id"]})
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) blocks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	page := profilePage(w, r)
	if page == nil {
		return
	}
	res, err := h.client.ListBlocks(ctx, page)
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) block(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	p := &pb.UserID{UserId: mux.Vars(r)["id"]}
	if r.Method == "DELETE" {
		res, err := h.client.UnblockUser(ctx, p)
		h.reply(w, r, res, err, 204)
	} else {
		res, err := h.client.BlockUser(ctx, p)
		h.reply(w, r, res, err, 204)
	}
}
func (h *profileHandler) reviews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	page := profilePage(w, r)
	if page == nil {
		return
	}
	res, err := h.client.ListReviews(ctx, &pb.ReviewQuery{UserId: mux.Vars(r)["user_id"], Page: page})
	h.reply(w, r, res, err, 200)
}
func (h *profileHandler) createReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := profileContext(r)
	defer cancel()
	p := &pb.ReviewInput{}
	if !decodeJSONLimit(w, r, p, 65536) {
		return
	}
	res, err := h.client.CreateReview(ctx, p)
	h.reply(w, r, res, err, 201)
}
