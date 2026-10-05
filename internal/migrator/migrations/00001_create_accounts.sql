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
    email varchar(320) NOT NULL,
    password_hash varchar(255) NOT NULL,
    role users.system_role_code NOT NULL DEFAULT 'client',
    status users.user_account_status NOT NULL DEFAULT 'active',
    status_changed_by_user_id uuid REFERENCES users.user_account(id),
    status_changed_at timestamptz,
    status_comment text,
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_account_email_format CHECK (email = lower(email) AND position('@' in email) > 1),
    CONSTRAINT user_account_id_email_unique UNIQUE (id, email)
);
CREATE UNIQUE INDEX user_account_email_unique ON users.user_account (lower(email));
CREATE INDEX idx_user_account_status ON users.user_account(status);

-- Здесь хранятся только подтверждённые адреса. Адрес принадлежит одной учётной
-- записи, а учётная запись имеет не более одного текущего подтверждённого адреса.
CREATE TABLE users.user_email (
    user_id uuid PRIMARY KEY REFERENCES users.user_account(id),
    email varchar(320) NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_email_format CHECK (position('@' in email) > 1 AND email = lower(email)),
    CONSTRAINT user_email_account_email_fk
        FOREIGN KEY (user_id, email) REFERENCES users.user_account (id, email)
);
CREATE UNIQUE INDEX user_email_email_unique ON users.user_email (lower(email));

-- До подтверждения аккаунта нет. Новая регистрация того же email заменяет
-- пароль и токен заявки; заявка не резервирует адрес за пользователем.
CREATE TABLE users.registration_request (
    email varchar(320) PRIMARY KEY,
    password_hash varchar(255) NOT NULL,
    token_hash char(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT registration_email_format CHECK (position('@' in email) > 1 AND email = lower(email))
);
CREATE INDEX registration_expires_at_idx ON users.registration_request (expires_at);

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
DROP TABLE IF EXISTS users.registration_request;
DROP TABLE IF EXISTS users.user_email;
DROP TABLE IF EXISTS users.user_account;
DROP TYPE IF EXISTS users.user_account_status;
DROP TYPE IF EXISTS users.system_role_code;
