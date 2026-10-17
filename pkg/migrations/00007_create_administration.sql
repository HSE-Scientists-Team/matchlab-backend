-- +goose Up
CREATE SCHEMA admin;
CREATE TABLE admin.account (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 email varchar(320) NOT NULL UNIQUE CHECK(email=lower(email) AND position('@' in email)>1),
 display_name varchar(120) NOT NULL CHECK(length(btrim(display_name))>0),
 password_hash varchar(255) NOT NULL,
 role text NOT NULL CHECK(role IN ('moderator','administrator')),
 active boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 session_version integer NOT NULL DEFAULT 1 CHECK(session_version>0),
 created_by_admin_id uuid REFERENCES admin.account(id),
 last_login_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE admin.session (
 token_hash char(64) PRIMARY KEY,
 admin_id uuid NOT NULL REFERENCES admin.account(id),
 session_version integer NOT NULL,expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_session_expiration ON admin.session(expires_at);
CREATE INDEX admin_session_account ON admin.session(admin_id);
CREATE TABLE admin.login_throttle (
 key_hash char(64) PRIMARY KEY,failures integer NOT NULL DEFAULT 0 CHECK(failures>=0),
 window_started_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE admin.audit_event (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),actor_admin_id uuid REFERENCES admin.account(id),
 action text NOT NULL,resource_id text NOT NULL DEFAULT '',comment text NOT NULL DEFAULT '',
 result text NOT NULL CHECK(result IN ('started','succeeded','failed','unknown')),
 created_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz
);
CREATE INDEX admin_audit_created ON admin.audit_event(created_at DESC,id);
CREATE INDEX admin_audit_actor ON admin.audit_event(actor_admin_id,created_at DESC,id);

ALTER TABLE users.user_account ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK(revision>0);
ALTER TABLE users.user_account ADD COLUMN status_changed_by_admin_id uuid;
ALTER TABLE users.trusted_email_domain ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK(revision>0);
CREATE TABLE users.admin_event (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),actor_admin_id uuid NOT NULL,
 action text NOT NULL,resource_id uuid NOT NULL,comment text NOT NULL,
 previous_status text NOT NULL DEFAULT '',status text NOT NULL DEFAULT '',revision integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_admin_resource ON users.admin_event(resource_id,created_at DESC,id);
ALTER TABLE users.achievement_decision ALTER COLUMN reviewer_user_id DROP NOT NULL;
ALTER TABLE users.achievement_decision ADD COLUMN reviewer_admin_id uuid;
ALTER TABLE users.achievement_decision ADD CONSTRAINT achievement_decision_actor CHECK(num_nonnulls(reviewer_user_id,reviewer_admin_id)=1);

ALTER TABLE support.support_ticket ADD COLUMN assigned_admin_id uuid;
ALTER TABLE support.support_ticket ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK(revision>0);
ALTER TABLE support.support_ticket ADD COLUMN resolution_comment text NOT NULL DEFAULT '';
CREATE INDEX support_admin_queue ON support.support_ticket(status,assigned_admin_id,created_at,id);
CREATE TABLE support.ticket_event (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),ticket_id uuid NOT NULL REFERENCES support.support_ticket(id),
 actor_admin_id uuid NOT NULL,action text NOT NULL,comment text NOT NULL,
 previous_status text NOT NULL,status text NOT NULL,revision integer NOT NULL,
 previous_assigned_admin_id uuid,assigned_admin_id uuid,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_history ON support.ticket_event(ticket_id,created_at DESC,id);
ALTER TABLE projects.moderation ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE projects.moderation ADD COLUMN actor_admin_id uuid;
ALTER TABLE projects.moderation ADD CONSTRAINT project_moderation_actor CHECK(num_nonnulls(actor_user_id,actor_admin_id)=1);

-- +goose Down
-- Do not silently erase attributable moderation history to restore legacy NOT NULL fields.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM users.achievement_decision WHERE reviewer_admin_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM projects.moderation WHERE actor_admin_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM admin.audit_event)
 OR EXISTS(SELECT 1 FROM users.admin_event)
 OR EXISTS(SELECT 1 FROM support.ticket_event) THEN
  RAISE EXCEPTION 'Administrative moderation history must be archived before reverting 00007';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE projects.moderation DROP CONSTRAINT project_moderation_actor;
ALTER TABLE projects.moderation DROP COLUMN actor_admin_id;
ALTER TABLE projects.moderation ALTER COLUMN actor_user_id SET NOT NULL;
DROP TABLE support.ticket_event;
DROP INDEX support.support_admin_queue;
ALTER TABLE support.support_ticket DROP COLUMN resolution_comment;
ALTER TABLE support.support_ticket DROP COLUMN revision;
ALTER TABLE support.support_ticket DROP COLUMN assigned_admin_id;
ALTER TABLE users.achievement_decision DROP CONSTRAINT achievement_decision_actor;
ALTER TABLE users.achievement_decision DROP COLUMN reviewer_admin_id;
ALTER TABLE users.achievement_decision ALTER COLUMN reviewer_user_id SET NOT NULL;
DROP TABLE users.admin_event;
ALTER TABLE users.trusted_email_domain DROP COLUMN revision;
ALTER TABLE users.user_account DROP COLUMN status_changed_by_admin_id;
ALTER TABLE users.user_account DROP COLUMN revision;
DROP TABLE admin.audit_event;
DROP TABLE admin.login_throttle;
DROP TABLE admin.session;
DROP TABLE admin.account;
DROP SCHEMA admin;
