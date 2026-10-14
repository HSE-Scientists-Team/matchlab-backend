-- +goose Up
-- Exact domains, not wildcards. Preserve administrator changes to existing rules.
INSERT INTO users.trusted_email_domain(domain,organization_name,is_active)
VALUES ('hse.ru','НИУ ВШЭ',true),('edu.hse.ru','НИУ ВШЭ',true)
ON CONFLICT (domain) DO NOTHING;

CREATE TABLE users.user_profile (
    user_id uuid PRIMARY KEY REFERENCES users.user_account(id),
    data jsonb NOT NULL DEFAULT '{}',
    show_contacts boolean NOT NULL DEFAULT false,
    show_participating_projects boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users.user_profile(user_id) SELECT id FROM users.user_account;
-- +goose StatementBegin
CREATE FUNCTION users.initialize_profile() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO users.user_profile(user_id) VALUES (NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER initialize_profile AFTER INSERT ON users.user_account
FOR EACH ROW EXECUTE FUNCTION users.initialize_profile();
CREATE INDEX profile_skills_gin ON users.user_profile USING gin ((data->'skills'));
CREATE INDEX profile_interests_gin ON users.user_profile USING gin ((data->'interests'));

CREATE TABLE users.achievement (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users.user_account(id),
    kind text NOT NULL CHECK (kind IN ('education','certificate','scientific','project')),
    data jsonb NOT NULL,
    evidence_file_id uuid,
    status text NOT NULL DEFAULT 'unverified'
        CHECK (status IN ('unverified','pending_review','verified','needs_evidence')),
    revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
    moderation_comment text NOT NULL DEFAULT '',
    submitted_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX achievement_owner ON users.achievement(user_id,created_at,id);
CREATE INDEX achievement_queue ON users.achievement(submitted_at,id) WHERE status='pending_review';
CREATE TABLE users.achievement_decision (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    achievement_id uuid NOT NULL REFERENCES users.achievement(id),
    revision integer NOT NULL,
    reviewer_user_id uuid NOT NULL REFERENCES users.user_account(id),
    approved boolean NOT NULL,
    reviewed_data jsonb NOT NULL,
    evidence_file_id uuid,
    comment text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(achievement_id,revision)
);
CREATE TABLE users.user_block (
    blocker_user_id uuid NOT NULL REFERENCES users.user_account(id),
    blocked_user_id uuid NOT NULL REFERENCES users.user_account(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(blocker_user_id,blocked_user_id),
    CHECK(blocker_user_id<>blocked_user_id)
);
CREATE INDEX user_block_reverse ON users.user_block(blocked_user_id,blocker_user_id);

-- Read-only completion snapshots for reviews, not a project lifecycle model.
CREATE TABLE users.project_context (
    project_id uuid PRIMARY KEY,
    title varchar(255) NOT NULL,
    initiator_user_id uuid NOT NULL REFERENCES users.user_account(id),
    completed_at timestamptz
);
CREATE TABLE users.project_context_member (
    project_id uuid NOT NULL REFERENCES users.project_context(project_id),
    user_id uuid NOT NULL REFERENCES users.user_account(id),
    PRIMARY KEY(project_id,user_id)
);
CREATE INDEX project_context_user ON users.project_context_member(user_id,project_id);
CREATE TABLE users.profile_review (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES users.project_context(project_id),
    author_user_id uuid NOT NULL REFERENCES users.user_account(id),
    target_user_id uuid NOT NULL REFERENCES users.user_account(id),
    title varchar(255) NOT NULL,
    text text NOT NULL,
    rating integer NOT NULL CHECK(rating BETWEEN 1 AND 5),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK(author_user_id<>target_user_id),
    UNIQUE(project_id,author_user_id,target_user_id),
    FOREIGN KEY(project_id,author_user_id) REFERENCES users.project_context_member(project_id,user_id),
    FOREIGN KEY(project_id,target_user_id) REFERENCES users.project_context_member(project_id,user_id)
);
CREATE INDEX profile_review_target ON users.profile_review(target_user_id,created_at,id);

-- +goose Down
DROP TABLE users.profile_review;
DROP TABLE users.project_context_member;
DROP TABLE users.project_context;
DROP TABLE users.user_block;
DROP TABLE users.achievement_decision;
DROP TABLE users.achievement;
DROP TRIGGER initialize_profile ON users.user_account;
DROP FUNCTION users.initialize_profile();
DROP TABLE users.user_profile;
-- Domain rules intentionally survive rollback: they may have been edited by an
-- administrator or existed before Up; rollback must not revoke that decision.
