package http

import (
	"context"
	"encoding/json"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	rpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"log/slog"
	"net/http"
	"strconv"
)

type projectHandler struct {
	project        ppb.ProjectServiceClient
	search         spb.SearchServiceClient
	recommendation rpb.RecommendationServiceClient
	reply          *profileHandler
}

func RegisterProjects(router *mux.Router, project ppb.ProjectServiceClient, search spb.SearchServiceClient, recommendation rpb.RecommendationServiceClient, logger *slog.Logger) {
	h := &projectHandler{project: project, search: search, recommendation: recommendation, reply: &profileHandler{logger: logger}}
	r := router.PathPrefix("/api/v1").Subrouter()
	r.HandleFunc("/projects", h.projects).Methods("GET", "POST")
	r.HandleFunc("/projects/{id}", h.projectItem).Methods("GET", "PUT")
	r.HandleFunc("/projects/{id}/transitions", h.transition).Methods("POST")
	r.HandleFunc("/projects/{id}/roles", h.role).Methods("POST")
	r.HandleFunc("/projects/{id}/roles/{role_id}", h.role).Methods("PUT", "DELETE")
	r.HandleFunc("/projects/{id}/applications", h.apply).Methods("POST")
	r.HandleFunc("/projects/{id}/invitations", h.invite).Methods("POST")
	r.HandleFunc("/projects/{id}/joins", h.joins).Methods("GET")
	r.HandleFunc("/projects/{id}/members/{user_id}", h.removeMember).Methods("DELETE")
	r.HandleFunc("/projects/{id}/membership", h.leave).Methods("DELETE")
	r.HandleFunc("/project-joins", h.joins).Methods("GET")
	r.HandleFunc("/project-joins/{id}/decision", h.decide).Methods("POST")
	r.HandleFunc("/project-joins/{id}", h.cancelJoin).Methods("DELETE")
	r.HandleFunc("/search/projects", h.searchProjects).Methods("GET")
	r.HandleFunc("/search/people", h.searchPeople).Methods("GET")
	r.HandleFunc("/recommendations/projects", h.recommendProjects).Methods("GET")
	r.HandleFunc("/recommendations/people", h.recommendPeople).Methods("GET")
}
func (h *projectHandler) run(w http.ResponseWriter, r *http.Request, code int, f func(context.Context) (proto.Message, error)) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := profileContext(r)
	defer cancel()
	p, err := f(ctx)
	h.reply.reply(w, r, p, err, code)
}
func projectQuery(w http.ResponseWriter, r *http.Request) *ppb.ProjectQuery {
	page := profilePage(w, r)
	if page == nil {
		return nil
	}
	v := r.URL.Query()
	return &ppb.ProjectQuery{Text: v.Get("q"), Type: v.Get("type"), Field: v.Get("field"), Tags: v["tag"], Duration: v.Get("duration"), Status: v.Get("status"), Mine: v.Get("mine"), ProfileUserId: v.Get("profile_user_id"), Page: &ppb.Page{Limit: page.Limit, Offset: page.Offset}}
}
func (h *projectHandler) projects(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		p := &ppb.ProjectInput{}
		if !decodeJSONLimit(w, r, p, 1<<20) {
			return
		}
		h.run(w, r, 201, func(ctx context.Context) (proto.Message, error) { return h.project.Create(ctx, p) })
		return
	}
	q := projectQuery(w, r)
	if q == nil {
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.project.List(ctx, q) })
}
func (h *projectHandler) projectItem(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if r.Method == "PUT" {
		p := &ppb.ProjectInput{}
		if !decodeJSONLimit(w, r, p, 65536) {
			return
		}
		if p.Id != "" && p.Id != id {
			writeError(w, 400, "id не соответствует пути")
			return
		}
		p.Id = id
		h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.project.Update(ctx, p) })
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.project.Get(ctx, &ppb.ProjectID{Id: id}) })
}
func (h *projectHandler) role(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	if r.Method == "DELETE" {
		revision, err := strconv.ParseInt(r.URL.Query().Get("expected_revision"), 10, 32)
		if err != nil {
			writeError(w, 400, "требуется expected_revision")
			return
		}
		h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
			return h.project.DeleteRole(ctx, &ppb.RoleID{Id: v["role_id"], ProjectId: v["id"], ExpectedRevision: int32(revision)})
		})
		return
	}
	p := &ppb.RoleInput{}
	if !decodeJSONLimit(w, r, p, 65536) {
		return
	}
	if (p.Id != "" && p.Id != v["role_id"]) || (p.ProjectId != "" && p.ProjectId != v["id"]) {
		writeError(w, 400, "id не соответствует пути")
		return
	}
	p.Id = v["role_id"]
	p.ProjectId = v["id"]
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.project.SaveRole(ctx, p) })
}
func (h *projectHandler) transition(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Action           string `json:"action"`
		ExpectedRevision int32  `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &p) {
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.Transition(ctx, &ppb.TransitionRequest{Id: mux.Vars(r)["id"], Action: p.Action, ExpectedRevision: p.ExpectedRevision})
	})
}
func (h *projectHandler) apply(w http.ResponseWriter, r *http.Request)  { h.join(w, r, false) }
func (h *projectHandler) invite(w http.ResponseWriter, r *http.Request) { h.join(w, r, true) }
func (h *projectHandler) join(w http.ResponseWriter, r *http.Request, invite bool) {
	p := &ppb.JoinInput{}
	if !decodeJSON(w, r, p) {
		return
	}
	if p.ProjectId != "" && p.ProjectId != mux.Vars(r)["id"] {
		writeError(w, 400, "id не соответствует пути")
		return
	}
	p.ProjectId = mux.Vars(r)["id"]
	h.run(w, r, 201, func(ctx context.Context) (proto.Message, error) {
		if invite {
			return h.project.Invite(ctx, p)
		}
		return h.project.Apply(ctx, p)
	})
}
func (h *projectHandler) joins(w http.ResponseWriter, r *http.Request) {
	page := profilePage(w, r)
	if page == nil {
		return
	}
	v := r.URL.Query()
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.ListJoins(ctx, &ppb.JoinQuery{ProjectId: mux.Vars(r)["id"], RoleId: v.Get("role_id"), Status: v.Get("status"), Page: &ppb.Page{Limit: page.Limit, Offset: page.Offset}})
	})
}
func (h *projectHandler) decide(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Accept *bool `json:"accept"`
	}
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.Accept == nil {
		writeError(w, 400, "требуется accept")
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.DecideJoin(ctx, &ppb.JoinDecision{Id: mux.Vars(r)["id"], Accept: *p.Accept})
	})
}
func (h *projectHandler) cancelJoin(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.CancelJoin(ctx, &ppb.JoinID{Id: mux.Vars(r)["id"]})
	})
}
func (h *projectHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.RemoveMember(ctx, &ppb.MemberID{ProjectId: v["id"], UserId: v["user_id"]})
	})
}
func (h *projectHandler) leave(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		return h.project.Leave(ctx, &ppb.ProjectID{Id: mux.Vars(r)["id"]})
	})
}
func (h *projectHandler) searchProjects(w http.ResponseWriter, r *http.Request) {
	q := projectQuery(w, r)
	if q == nil {
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.search.Projects(ctx, q) })
}
func (h *projectHandler) searchPeople(w http.ResponseWriter, r *http.Request) {
	page := profilePage(w, r)
	if page == nil {
		return
	}
	v := r.URL.Query()
	flags := map[string]*bool{}
	q := &upb.PeopleQuery{Text: v.Get("q"), University: v.Get("university"), ProjectType: v.Get("project_type"), Skills: v["skill"], Interests: v["interest"], Page: page}
	flags["my_university"] = &q.MyUniversity
	flags["open_only"] = &q.OpenOnly
	flags["verified_only"] = &q.VerifiedOnly
	flags["verified_projects"] = &q.VerifiedProjects
	flags["publications"] = &q.Publications
	for key, ptr := range flags {
		if value := v.Get(key); value != "" {
			n, err := strconv.ParseBool(value)
			if err != nil {
				writeError(w, 400, "некорректный фильтр")
				return
			}
			*ptr = n
		}
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) { return h.search.People(ctx, q) })
}
func (h *projectHandler) recommendProjects(w http.ResponseWriter, r *http.Request) {
	h.recommend(w, r, false)
}
func (h *projectHandler) recommendPeople(w http.ResponseWriter, r *http.Request) {
	h.recommend(w, r, true)
}
func (h *projectHandler) recommend(w http.ResponseWriter, r *http.Request, people bool) {
	page := profilePage(w, r)
	if page == nil {
		return
	}
	if page.Offset != 0 {
		writeError(w, 400, "рекомендации не поддерживают offset")
		return
	}
	h.run(w, r, 200, func(ctx context.Context) (proto.Message, error) {
		q := &rpb.RecommendationQuery{Limit: page.Limit}
		var headers metadata.MD
		var value proto.Message
		var err error
		if people {
			value, err = h.recommendation.People(ctx, q, grpc.Header(&headers))
		} else {
			value, err = h.recommendation.Projects(ctx, q, grpc.Header(&headers))
		}
		if err != nil {
			return nil, err
		}
		return recommendationEnvelope(value, headers)
	})
}

// Preserve existing protobuf cards and add batch provenance/explanations only on
// recommendation routes. Older internal servers without metadata still work.
func recommendationEnvelope(value proto.Message, headers metadata.MD) (proto.Message, error) {
	info := headers.Get("matchlab-recommendations-bin")
	if len(info) == 0 {
		return value, nil
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(value)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var recommendation map[string]any
	if err := json.Unmarshal([]byte(info[0]), &recommendation); err != nil {
		return nil, err
	}
	body["recommendation"] = recommendation
	return structpb.NewStruct(body)
}
