-- +goose Up
CREATE SCHEMA IF NOT EXISTS mail;

CREATE TABLE mail.email_outbox (
    id bigserial PRIMARY KEY,
    idempotency_key char(64) NOT NULL UNIQUE,
    recipient varchar(320) NOT NULL,
    token_ciphertext bytea,
    token_nonce bytea,
    expires_at timestamptz NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    CONSTRAINT email_outbox_status CHECK (status IN ('pending', 'sending', 'sent', 'dead')),
    CONSTRAINT email_outbox_attempts CHECK (attempts >= 0),
    CONSTRAINT email_outbox_payload CHECK (
        (token_ciphertext IS NULL AND token_nonce IS NULL) OR
        (token_ciphertext IS NOT NULL AND token_nonce IS NOT NULL)
    )
);

CREATE INDEX email_outbox_pending_idx
    ON mail.email_outbox (next_attempt_at, created_at)
    WHERE status = 'pending';
CREATE INDEX email_outbox_expired_lease_idx
    ON mail.email_outbox (lease_until)
    WHERE status = 'sending';
CREATE INDEX email_outbox_sent_cleanup_idx
    ON mail.email_outbox (sent_at)
    WHERE status IN ('sent', 'dead');

-- +goose Down
-- Pending and dead-lettered emails are permanently discarded.
DROP TABLE IF EXISTS mail.email_outbox;
