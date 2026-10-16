-- +goose Up
CREATE SCHEMA projects;
CREATE TABLE projects.project (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner_user_id uuid NOT NULL,
 data jsonb NOT NULL, status text NOT NULL DEFAULT 'recruiting'
 CHECK(status IN ('recruiting','unpublished','active','completed','cancelled')),
 moderation_hidden boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_owner ON projects.project(owner_user_id,created_at,id);
CREATE INDEX project_catalog ON projects.project(status,created_at,id) WHERE NOT moderation_hidden;
CREATE INDEX project_tags ON projects.project USING gin ((data->'tags'));
CREATE TABLE projects.role (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),project_id uuid NOT NULL REFERENCES projects.project(id),
 title varchar(255) NOT NULL,description text NOT NULL,skills jsonb NOT NULL,
 deleted_at timestamptz, slots integer NOT NULL CHECK(slots BETWEEN 1 AND 100), UNIQUE(project_id,id)
);
CREATE TABLE projects.member (
 project_id uuid NOT NULL REFERENCES projects.project(id),user_id uuid NOT NULL,
 role_id uuid,status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','left','removed')),
 PRIMARY KEY(project_id,user_id),
 FOREIGN KEY(project_id,role_id) REFERENCES projects.role(project_id,id)
);
CREATE INDEX project_member_user ON projects.member(user_id,project_id);
CREATE TABLE projects.join_request (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),project_id uuid NOT NULL REFERENCES projects.project(id),
 role_id uuid NOT NULL,candidate_user_id uuid NOT NULL,
 type text NOT NULL CHECK(type IN ('application','invitation')),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','rejected','cancelled')),
 message text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,role_id) REFERENCES projects.role(project_id,id)
);
CREATE UNIQUE INDEX project_join_pending ON projects.join_request(project_id,role_id,candidate_user_id) WHERE status='pending';
CREATE INDEX project_join_candidate ON projects.join_request(candidate_user_id,created_at,id);
CREATE TABLE projects.moderation (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,project_id uuid NOT NULL REFERENCES projects.project(id),
 actor_user_id uuid NOT NULL,hidden boolean NOT NULL,reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Each project mutation and its snapshot commit in one database transaction.
CREATE TABLE projects.context_outbox (
 project_id uuid NOT NULL REFERENCES projects.project(id),revision integer NOT NULL,
 payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 delivered_at timestamptz,PRIMARY KEY(project_id,revision)
);
CREATE INDEX project_outbox_pending ON projects.context_outbox(created_at,project_id,revision) WHERE delivered_at IS NULL;
ALTER TABLE users.project_context ADD COLUMN source_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE users.project_context ADD COLUMN state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','completed','cancelled'));
UPDATE users.project_context SET state='completed' WHERE completed_at IS NOT NULL;

-- +goose Down
ALTER TABLE users.project_context DROP COLUMN state;
ALTER TABLE users.project_context DROP COLUMN source_revision;
DROP TABLE projects.context_outbox;
DROP TABLE projects.moderation;
DROP TABLE projects.join_request;
DROP TABLE projects.member;
DROP TABLE projects.role;
DROP TABLE projects.project;
DROP SCHEMA projects;
