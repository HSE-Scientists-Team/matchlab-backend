-- +goose Up
CREATE SCHEMA IF NOT EXISTS users;

CREATE TYPE users.system_role_code AS ENUM (
    'client',
    'moderator',
    'administrator'
);

CREATE TYPE users.user_account_status AS ENUM (
    'active',
    'blocked',
    'deleted'
);

CREATE TABLE users.user_account (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login varchar(32) NOT NULL,
    password_hash varchar(255) NOT NULL,
    role users.system_role_code NOT NULL DEFAULT 'client',
    status users.user_account_status NOT NULL DEFAULT 'active',
    status_changed_by_user_id uuid REFERENCES users.user_account(id),
    status_changed_at timestamptz,
    status_comment text,
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_account_login_format CHECK (login ~ '^[a-z0-9_.-]{1,32}$')
);
CREATE UNIQUE INDEX user_account_login_unique ON users.user_account (lower(login));
CREATE INDEX idx_user_account_status ON users.user_account(status);

-- Only confirmed addresses live here. An address can belong to exactly one
-- account, while one account has at most one current confirmed address.
CREATE TABLE users.user_email (
    user_id uuid PRIMARY KEY REFERENCES users.user_account(id),
    email varchar(320) NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_email_format CHECK (position('@' in email) > 1 AND email = lower(email))
);
CREATE UNIQUE INDEX user_email_email_unique ON users.user_email (lower(email));

-- Pending requests are account-specific. Different accounts may request the
-- same address, but only the user holding the matching token can confirm it.
CREATE TABLE users.email_verification_request (
    user_id uuid PRIMARY KEY REFERENCES users.user_account(id),
    email varchar(320) NOT NULL,
    token_hash char(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_verification_email_format CHECK (position('@' in email) > 1 AND email = lower(email))
);
CREATE INDEX email_verification_email_idx ON users.email_verification_request (email);
CREATE INDEX email_verification_expires_at_idx ON users.email_verification_request (expires_at);

CREATE TABLE users.trusted_email_domain (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain varchar(255) NOT NULL UNIQUE,
    organization_name varchar(255) NOT NULL,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS users.trusted_email_domain;
DROP TABLE IF EXISTS users.email_verification_request;
DROP TABLE IF EXISTS users.user_email;
DROP TABLE IF EXISTS users.user_account;
DROP TYPE IF EXISTS users.user_account_status;
DROP TYPE IF EXISTS users.system_role_code;
