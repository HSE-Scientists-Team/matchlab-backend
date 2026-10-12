-- +goose Up
CREATE SCHEMA support;
CREATE TYPE support.support_ticket_status AS ENUM ('open', 'resolved', 'declined');

CREATE TABLE support.support_ticket (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    author_user_id uuid NOT NULL,
    assigned_user_id uuid,
    status support.support_ticket_status NOT NULL DEFAULT 'open',
    subject varchar(255) NOT NULL CHECK (length(btrim(subject)) > 0),
    description text NOT NULL CHECK (length(btrim(description)) > 0),
    target_user_id uuid,
    target_project_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    CONSTRAINT support_ticket_one_target CHECK (num_nonnulls(target_user_id, target_project_id) = 1)
);
CREATE INDEX idx_support_ticket_author ON support.support_ticket (author_user_id);
CREATE INDEX idx_support_ticket_assignee ON support.support_ticket (assigned_user_id);
CREATE INDEX idx_support_ticket_status ON support.support_ticket (status);
CREATE INDEX idx_support_ticket_target_user ON support.support_ticket (target_user_id);
CREATE INDEX idx_support_ticket_target_project ON support.support_ticket (target_project_id);

CREATE TABLE support.support_ticket_attachment (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    support_ticket_id uuid NOT NULL REFERENCES support.support_ticket (id) ON DELETE NO ACTION,
    file_id uuid NOT NULL,
    CONSTRAINT support_ticket_attachment_file_unique UNIQUE (support_ticket_id, file_id)
);
CREATE INDEX idx_support_ticket_attachment_ticket ON support.support_ticket_attachment (support_ticket_id);

-- +goose Down
DROP TABLE support.support_ticket_attachment;
DROP TABLE support.support_ticket;
DROP TYPE support.support_ticket_status;
DROP SCHEMA support;
