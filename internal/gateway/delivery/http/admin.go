package http

import (
	"context"
	"encoding/json"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"io"
	"log/slog"
	"net/http"
	"strconv"
)

// RegisterAdmin explicitly maps only Admin facade RPCs; privileged User/Support/Project
// services never receive a caller-supplied internal assertion from HTTP.
func RegisterAdmin(router *mux.Router, client pb.AdminServiceClient, logger *slog.Logger) {
	r := router.PathPrefix("/api/v1/admin").Subrouter()
	adminRoute(r, "GET", "/staff/{id}", func() *mpb.ID { return &mpb.ID{} }, client.GetStaff, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "POST", "/auth/login", func() *pb.LoginInput { return &pb.LoginInput{} }, client.Login, true, 200, map[string]string{}, logger, []string{})
	adminRoute(r, "DELETE", "/sessions/current", func() *mpb.Empty { return &mpb.Empty{} }, client.Logout, false, 204, map[string]string{}, logger, []string{})
	adminRoute(r, "GET", "/me", func() *mpb.Empty { return &mpb.Empty{} }, client.GetMe, false, 200, map[string]string{}, logger, []string{})
	adminRoute(r, "PUT", "/me/password", func() *pb.PasswordChange { return &pb.PasswordChange{} }, client.ChangePassword, true, 204, map[string]string{}, logger, []string{})
	adminRoute(r, "GET", "/staff", func() *mpb.Query { return &mpb.Query{} }, client.ListStaff, false, 200, map[string]string{}, logger, []string{"text", "status", "limit", "offset"})
	adminRoute(r, "POST", "/staff", func() *pb.StaffInput { return &pb.StaffInput{} }, client.CreateStaff, true, 201, map[string]string{}, logger, []string{})
	adminRoute(r, "PUT", "/staff/{id}", func() *pb.StaffUpdate { return &pb.StaffUpdate{} }, client.UpdateStaff, true, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "PUT", "/staff/{id}/password", func() *pb.PasswordReset { return &pb.PasswordReset{} }, client.ResetStaffPassword, true, 204, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "DELETE", "/staff/{id}/sessions", func() *mpb.ID { return &mpb.ID{} }, client.RevokeStaffSessions, false, 204, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/audit", func() *mpb.Query { return &mpb.Query{} }, client.ListAudit, false, 200, map[string]string{}, logger, []string{"text", "actor_admin_id", "action", "result", "created_from_unix", "created_to_unix", "limit", "offset"})
	adminRoute(r, "GET", "/tickets", func() *mpb.Query { return &mpb.Query{} }, client.ListTickets, false, 200, map[string]string{}, logger, []string{"text", "status", "user_id", "assigned_admin_id", "unassigned_only", "target_user_id", "target_project_id", "created_from_unix", "created_to_unix", "limit", "offset"})
	adminRoute(r, "GET", "/tickets/{id}", func() *mpb.ID { return &mpb.ID{} }, client.GetTicket, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "POST", "/tickets/{id}/actions", func() *spb.TicketChange { return &spb.TicketChange{} }, client.UpdateTicket, true, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "POST", "/tickets/{id}/notes", func() *spb.TicketNote { return &spb.TicketNote{} }, client.AddTicketNote, true, 201, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/tickets/{id}/history", func() *mpb.HistoryQuery { return &mpb.HistoryQuery{} }, client.TicketHistory, false, 200, map[string]string{"id": "id"}, logger, []string{"limit", "offset"})
	adminRoute(r, "POST", "/tickets/{ticket_id}/attachments/{file_id}/download-url", func() *spb.AttachmentQuery { return &spb.AttachmentQuery{} }, client.DownloadTicketAttachment, false, 200, map[string]string{"ticket_id": "ticket_id", "file_id": "file_id"}, logger, []string{})
	adminRoute(r, "GET", "/users", func() *mpb.Query { return &mpb.Query{} }, client.ListUsers, false, 200, map[string]string{}, logger, []string{"text", "status", "user_id", "created_from_unix", "created_to_unix", "limit", "offset"})
	adminRoute(r, "GET", "/users/{id}", func() *mpb.ID { return &mpb.ID{} }, client.GetUser, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "PUT", "/users/{id}/status", func() *upb.UserStatusChange { return &upb.UserStatusChange{} }, client.SetUserStatus, true, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/users/{id}/history", func() *mpb.HistoryQuery { return &mpb.HistoryQuery{} }, client.UserHistory, false, 200, map[string]string{"id": "id"}, logger, []string{"limit", "offset"})
	adminRoute(r, "GET", "/email-domains", func() *mpb.Query { return &mpb.Query{} }, client.ListDomains, false, 200, map[string]string{}, logger, []string{"text", "status", "limit", "offset"})
	adminRoute(r, "POST", "/email-domains", func() *upb.DomainInput { return &upb.DomainInput{} }, client.SaveDomain, true, 201, map[string]string{}, logger, []string{})
	adminRoute(r, "PUT", "/email-domains/{id}", func() *upb.DomainInput { return &upb.DomainInput{} }, client.SaveDomain, true, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/email-domains/{id}/history", func() *mpb.HistoryQuery { return &mpb.HistoryQuery{} }, client.DomainHistory, false, 200, map[string]string{"id": "id"}, logger, []string{"limit", "offset"})
	adminRoute(r, "GET", "/achievements", func() *mpb.Query { return &mpb.Query{} }, client.ListAchievements, false, 200, map[string]string{}, logger, []string{"text", "status", "user_id", "kind", "created_from_unix", "created_to_unix", "limit", "offset"})
	adminRoute(r, "GET", "/achievements/{id}", func() *mpb.ID { return &mpb.ID{} }, client.GetAchievement, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "POST", "/achievements/{id}/decision", func() *upb.VerificationDecision { return &upb.VerificationDecision{} }, client.ReviewAchievement, true, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/achievements/{id}/history", func() *mpb.HistoryQuery { return &mpb.HistoryQuery{} }, client.AchievementHistory, false, 200, map[string]string{"id": "id"}, logger, []string{"limit", "offset"})
	adminRoute(r, "POST", "/achievements/{id}/evidence/download-url", func() *upb.AchievementID { return &upb.AchievementID{} }, client.AchievementEvidence, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "GET", "/projects", func() *ppb.ModerationQuery { return &ppb.ModerationQuery{} }, client.ListProjects, false, 200, map[string]string{}, logger, []string{"text", "status", "hidden_only", "limit", "offset"})
	adminRoute(r, "GET", "/projects/{id}", func() *ppb.ProjectID { return &ppb.ProjectID{} }, client.GetProject, false, 200, map[string]string{"id": "id"}, logger, []string{})
	adminRoute(r, "PUT", "/projects/{id}/moderation", func() *ppb.ModerationInput { return &ppb.ModerationInput{} }, client.ModerateProject, true, 200, map[string]string{"id": "project_id"}, logger, []string{})
	adminRoute(r, "GET", "/projects/{id}/history", func() *mpb.HistoryQuery { return &mpb.HistoryQuery{} }, client.ProjectHistory, false, 200, map[string]string{"id": "id"}, logger, []string{"limit", "offset"})
	adminRoute(r, "GET", "/statistics", func() *mpb.StatisticsQuery { return &mpb.StatisticsQuery{} }, client.GetStatistics, false, 200, map[string]string{}, logger, []string{"from_unix", "to_unix"})
}
func adminRoute[Q proto.Message, R proto.Message](router *mux.Router, method, path string, newQuery func() Q, call func(context.Context, Q, ...grpc.CallOption) (R, error), body bool, code int, bindings map[string]string, logger *slog.Logger, queryFields []string) {
	router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		q := newQuery()
		allowed := map[string]bool{}
		for _, name := range queryFields {
			allowed[name] = true
		}
		for name := range r.URL.Query() {
			if !allowed[name] {
				writeError(w, 400, "недопустимый query-параметр")
				return
			}
		}
		if body {
			if len(r.URL.Query()) > 0 {
				writeError(w, 400, "query-параметры недопустимы")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 65536)
			raw, e := io.ReadAll(r.Body)
			if e != nil || (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, q) != nil || !adminRequired(q, raw) {
				writeError(w, 400, "некорректное тело запроса")
				return
			}
		} else if !adminQuery(w, r, q) {
			return
		}
		v := q.ProtoReflect()
		for pathField, field := range bindings {
			descriptor := v.Descriptor().Fields().ByName(protoreflect.Name(field))
			value := mux.Vars(r)[pathField]
			if existing := v.Get(descriptor).String(); existing != "" && existing != value {
				writeError(w, 400, "идентификатор в теле не совпадает с путём")
				return
			}
			v.Set(descriptor, protoreflect.ValueOfString(value))
		}
		if method == "POST" && len(bindings) == 0 {
			if id := v.Descriptor().Fields().ByName("id"); id != nil && v.Get(id).String() != "" {
				writeError(w, 400, "id недопустим при создании")
				return
			}
		}
		ctx, cancel := profileContext(r)
		defer cancel()
		result, err := call(ctx, q)
		if status.Code(err) == codes.ResourceExhausted {
			w.Header().Set("Retry-After", "900")
			writeError(w, 429, status.Convert(err).Message())
			return
		}
		if status.Code(err) == codes.Internal {
			logger.ErrorContext(r.Context(), "admin RPC failed", "request_id", RequestID(r.Context()))
			writeError(w, 500, "ошибка сервиса Admin")
			return
		}
		(&profileHandler{logger: logger}).reply(w, r, result, err, code)
	}).Methods(method)
}
func adminQuery(w http.ResponseWriter, r *http.Request, q proto.Message) bool {
	v := q.ProtoReflect()
	for name, values := range r.URL.Query() {
		if len(values) != 1 {
			writeError(w, 400, "повторяющийся query-параметр")
			return false
		}
		target := v
		fieldName := name
		if name == "limit" || name == "offset" {
			page := v.Descriptor().Fields().ByName("page")
			if page == nil {
				writeError(w, 400, "пагинация недопустима")
				return false
			}
			target = v.Mutable(page).Message()
		}
		f := target.Descriptor().Fields().ByName(protoreflect.Name(fieldName))
		if f == nil || f.IsList() || f.Kind() == protoreflect.MessageKind {
			writeError(w, 400, "неизвестный query-параметр")
			return false
		}
		// Path identifiers are not accepted as query parameters.
		if name == "id" || name == "project_id" || name == "ticket_id" || name == "file_id" {
			writeError(w, 400, "используйте идентификатор в пути")
			return false
		}
		value := values[0]
		switch f.Kind() {
		case protoreflect.StringKind:
			target.Set(f, protoreflect.ValueOfString(value))
		case protoreflect.BoolKind:
			b, e := strconv.ParseBool(value)
			if e != nil {
				writeError(w, 400, "некорректный boolean query-параметр")
				return false
			}
			target.Set(f, protoreflect.ValueOfBool(b))
		case protoreflect.Int32Kind:
			n, e := strconv.ParseInt(value, 10, 32)
			if e != nil {
				writeError(w, 400, "некорректное число")
				return false
			}
			target.Set(f, protoreflect.ValueOfInt32(int32(n)))
		case protoreflect.Int64Kind:
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil {
				writeError(w, 400, "некорректное число")
				return false
			}
			target.Set(f, protoreflect.ValueOfInt64(n))
		default:
			writeError(w, 400, "недопустимый query-параметр")
			return false
		}
	}
	return true
}

func adminRequired(q proto.Message, raw []byte) bool {
	required := map[string][]string{
		"StaffUpdate":          {"display_name", "role", "active", "expected_revision", "reason"},
		"DomainInput":          {"domain", "organization_name", "is_active", "reason"},
		"VerificationDecision": {"approve", "expected_revision"},
		"ModerationInput":      {"hide", "reason", "expected_revision"},
	}
	fields := required[string(q.ProtoReflect().Descriptor().Name())]
	if len(fields) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	for _, name := range fields {
		f := q.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
		value, ok := object[name]
		if !ok {
			value, ok = object[f.JSONName()]
		}
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}
