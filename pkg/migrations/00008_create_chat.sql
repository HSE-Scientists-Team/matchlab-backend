-- +goose Up
CREATE SCHEMA chat;
CREATE TABLE chat.conversation (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_low_id uuid NOT NULL,
    user_high_id uuid NOT NULL,
    last_message_seq bigint NOT NULL DEFAULT 0 CHECK (last_message_seq >= 0),
    user_low_last_read_seq bigint NOT NULL DEFAULT 0,
    user_high_last_read_seq bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_message_at timestamptz,
    CHECK (user_low_id < user_high_id),
    CHECK (user_low_last_read_seq BETWEEN 0 AND last_message_seq),
    CHECK (user_high_last_read_seq BETWEEN 0 AND last_message_seq),
    CHECK ((last_message_seq = 0) = (last_message_at IS NULL)),
    UNIQUE (user_low_id, user_high_id)
);
CREATE INDEX conversation_low_activity ON chat.conversation (user_low_id, (coalesce(last_message_at, created_at)) DESC, id DESC);
CREATE INDEX conversation_high_activity ON chat.conversation (user_high_id, (coalesce(last_message_at, created_at)) DESC, id DESC);
CREATE TABLE chat.message (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES chat.conversation(id) ON DELETE NO ACTION,
    seq bigint NOT NULL CHECK (seq > 0),
    sender_user_id uuid NOT NULL,
    client_message_id uuid NOT NULL,
    body text NOT NULL CHECK (length(btrim(body, E' \t\n\r')) > 0 AND char_length(body) <= 10000),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq),
    UNIQUE (conversation_id, sender_user_id, client_message_id)
);

-- +goose Down
DROP TABLE chat.message;
DROP TABLE chat.conversation;
DROP SCHEMA chat;
