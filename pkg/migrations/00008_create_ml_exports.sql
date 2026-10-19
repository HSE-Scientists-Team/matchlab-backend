-- +goose Up
-- Versioned, minimized, read-only export contracts owned by User and Project.
-- Grant a batch reader SELECT on these views, never on the underlying tables.
CREATE VIEW users.ml_profiles_v1 AS
SELECT a.id::text AS id, a.status::text AS status, a.role::text AS role,
       EXISTS(SELECT 1 FROM users.user_email e WHERE e.user_id=a.id) AS confirmed,
       a.created_at, a.last_login_at, p.updated_at,
       coalesce(p.data->>'display_name','') <> '' AS has_name,
       coalesce(p.data->>'university','') AS university,
       coalesce(p.data->>'study_level','') AS study_level,
       coalesce(p.data->>'specialization','') AS specialization,
       coalesce(p.data->>'about','') AS about,
       coalesce((p.data->>'open_to_collaboration')::boolean,false) AS open_to_collaboration,
       coalesce(p.data->'skills','[]'::jsonb) AS skills,
       coalesce(p.data->'interests','[]'::jsonb) AS interests,
       coalesce(p.data->'project_types','[]'::jsonb) AS project_types
FROM users.user_account a LEFT JOIN users.user_profile p ON p.user_id=a.id
WHERE a.status <> 'deleted';

CREATE VIEW users.ml_blocks_v1 AS
SELECT blocker_user_id::text, blocked_user_id::text FROM users.user_block;

CREATE VIEW projects.ml_projects_v1 AS
SELECT id::text, owner_user_id::text, status, moderation_hidden, created_at, updated_at,
       coalesce(data->>'title','') AS title, coalesce(data->>'type','') AS type,
       coalesce(data->>'field','') AS field, coalesce(data->>'problem','') AS problem,
       coalesce(data->>'goal','') AS goal, coalesce(data->'tags','[]'::jsonb) AS tags,
       data->>'planned_start' AS planned_start, data->>'planned_end' AS planned_end
FROM projects.project;

CREATE VIEW projects.ml_roles_v1 AS
SELECT r.id::text, r.project_id::text, r.title, r.description, r.skills, r.slots,
       (SELECT count(*)::integer FROM projects.member m
        WHERE m.project_id=r.project_id AND m.role_id=r.id AND m.status='active') AS occupied
FROM projects.role r WHERE r.deleted_at IS NULL;

CREATE VIEW projects.ml_members_v1 AS
SELECT project_id::text, user_id::text, role_id::text, status FROM projects.member;

CREATE VIEW projects.ml_requests_v1 AS
SELECT id::text, project_id::text, role_id::text, candidate_user_id::text,
       type, status, created_at FROM projects.join_request;

REVOKE ALL ON users.ml_profiles_v1, users.ml_blocks_v1,
    projects.ml_projects_v1, projects.ml_roles_v1,
    projects.ml_members_v1, projects.ml_requests_v1 FROM PUBLIC;

-- +goose Down
DROP VIEW projects.ml_requests_v1;
DROP VIEW projects.ml_members_v1;
DROP VIEW projects.ml_roles_v1;
DROP VIEW projects.ml_projects_v1;
DROP VIEW users.ml_blocks_v1;
DROP VIEW users.ml_profiles_v1;
