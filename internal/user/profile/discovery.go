package profile

import (
	"context"
	"encoding/json"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"strings"
)

func (s *Service) GetIdentity(ctx context.Context, _ *pb.Empty) (*pb.Identity, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	role, _, err := s.Repo.Identity(ctx, id)
	return &pb.Identity{UserId: id, Role: role}, rpcError(err)
}
func (s *Service) ParticipationVisibility(ctx context.Context, q *pb.UserID) (*pb.ParticipationFlags, error) {
	if _, err := s.actor(ctx, false, false); err != nil {
		return nil, err
	}
	if !validID(q.UserId) {
		return nil, invalid()
	}
	_, p, err := s.Repo.Profile(ctx, q.UserId)
	if err != nil {
		return nil, rpcError(err)
	}
	return &pb.ParticipationFlags{ShowParticipatingProjects: p.ShowParticipatingProjects}, nil
}
func (s *Service) SearchPeople(ctx context.Context, q *pb.PeopleQuery) (*pb.PeopleList, error) {
	id, err := s.actor(ctx, q.MyUniversity, false)
	if err != nil {
		return nil, err
	}
	limit, offset, err := page(q.Page)
	if err != nil {
		return nil, err
	}
	if !text(q.Text, 255, false) || !text(q.University, 255, false) || !tags(q.Skills, nil) || !tags(q.Interests, nil) || (q.ProjectType != "" && q.ProjectType != "scientific" && q.ProjectType != "practical") {
		return nil, invalid()
	}
	university := strings.TrimSpace(q.University)
	if q.MyUniversity {
		p, _, e := s.Repo.Profile(ctx, id)
		if e != nil {
			return nil, rpcError(e)
		}
		university = p.Details.University
		if strings.TrimSpace(university) == "" {
			return &pb.PeopleList{Items: []*pb.Person{}}, nil
		}
	}
	skills := q.Skills
	if skills == nil {
		skills = []string{}
	}
	interests := q.Interests
	if interests == nil {
		interests = []string{}
	}
	sb, _ := json.Marshal(skills)
	ib, _ := json.Marshal(interests)
	where := ` FROM users.user_profile p JOIN users.user_account a ON a.id=p.user_id JOIN users.user_email e ON e.user_id=a.id WHERE a.status='active' AND coalesce(p.data->>'display_name','')<>'' AND p.user_id::text<>$1 AND NOT EXISTS(SELECT 1 FROM users.user_block b WHERE (b.blocker_user_id=p.user_id AND b.blocked_user_id::text=$1) OR (b.blocked_user_id=p.user_id AND b.blocker_user_id::text=$1)) AND ($2='' OR to_tsvector('simple',coalesce(p.data->>'display_name','')||' '||coalesce(p.data->>'university','')||' '||coalesce(p.data->>'specialization','')||' '||coalesce(p.data->>'about','')||' '||coalesce((p.data->'skills')::text,'')||' '||coalesce((p.data->'interests')::text,'')) @@ plainto_tsquery('simple',$2)) AND ($3='' OR lower(p.data->>'university')=lower($3)) AND (NOT $4 OR p.data->>'open_to_collaboration'='true') AND (NOT $5 OR split_part(e.email,'@',2) IN ('hse.ru','edu.hse.ru')) AND (NOT $6 OR EXISTS(SELECT 1 FROM users.achievement x WHERE x.user_id=p.user_id AND x.kind='project' AND x.status='verified' AND x.deleted_at IS NULL)) AND (NOT $7 OR EXISTS(SELECT 1 FROM users.achievement x WHERE x.user_id=p.user_id AND x.kind='scientific' AND x.status='verified' AND x.deleted_at IS NULL)) AND ($8='' OR coalesce(p.data->'project_types','[]'::jsonb) ? $8) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text($9::jsonb) wanted WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(p.data->'skills','[]'::jsonb)) owned WHERE lower(owned)=lower(wanted))) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text($10::jsonb) wanted WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(p.data->'interests','[]'::jsonb)) owned WHERE lower(owned)=lower(wanted)))`
	args := []any{id, q.Text, university, q.OpenOnly, q.VerifiedOnly, q.VerifiedProjects, q.Publications, q.ProjectType, string(sb), string(ib)}
	res := &pb.PeopleList{Items: []*pb.Person{}}
	if err = s.Repo.DB.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&res.Total); err != nil {
		return nil, rpcError(err)
	}
	order := ` ORDER BY lower(p.data->>'display_name'),p.user_id`
	if q.RandomOrder {
		order = ` ORDER BY random(),p.user_id`
	}
	args = append(args, limit, offset)
	rows, err := s.Repo.DB.QueryContext(ctx, `SELECT p.user_id::text,p.data,split_part(e.email,'@',2) IN ('hse.ru','edu.hse.ru'),(SELECT coalesce(avg(rating),0) FROM users.profile_review WHERE target_user_id=p.user_id),(SELECT count(*) FROM users.project_context_member m JOIN users.project_context x ON x.project_id=m.project_id WHERE m.user_id=p.user_id AND x.state='completed')`+where+order+` LIMIT $11 OFFSET $12`, args...)
	if err != nil {
		return nil, rpcError(err)
	}
	defer rows.Close()
	for rows.Next() {
		v := &pb.Person{}
		var raw []byte
		var details pb.ProfileInput
		if err = rows.Scan(&v.UserId, &raw, &v.Verified, &v.Rating, &v.CompletedProjectCount); err != nil {
			return nil, rpcError(err)
		}
		if err = json.Unmarshal(raw, &details); err != nil {
			return nil, rpcError(err)
		}
		v.DisplayName = details.DisplayName
		v.AvatarFileId = details.AvatarFileId
		v.University = details.University
		v.StudyLevel = details.StudyLevel
		v.StudyYear = details.StudyYear
		v.Specialization = details.Specialization
		v.Skills = details.Skills
		v.Interests = details.Interests
		v.OpenToCollaboration = details.OpenToCollaboration
		res.Items = append(res.Items, v)
	}
	return res, rpcError(rows.Err())
}
