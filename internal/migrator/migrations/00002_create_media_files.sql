-- +goose Up
CREATE SCHEMA IF NOT EXISTS media;

CREATE TYPE media.file_status AS ENUM (
    'pending',
    'ready',
    'deleting',
    'deleted',
    'failed'
);

CREATE TABLE media.file (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL,
    bucket_name varchar(63) NOT NULL,
    object_key text NOT NULL,
    original_name varchar(255) NOT NULL,
    content_type varchar(255) NOT NULL,
    size_bytes bigint,
    etag text,
    checksum_sha256 char(64),
    status media.file_status NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    uploaded_at timestamptz,
    deleted_at timestamptz,
    CONSTRAINT media_file_storage_location_unique UNIQUE (bucket_name, object_key),
    CONSTRAINT media_file_bucket_name_not_empty CHECK (bucket_name <> ''),
    CONSTRAINT media_file_object_key_format CHECK (
        object_key <> '' AND octet_length(object_key) <= 1024
    ),
    CONSTRAINT media_file_original_name_not_empty CHECK (original_name <> ''),
    CONSTRAINT media_file_content_type_not_empty CHECK (content_type <> ''),
    CONSTRAINT media_file_size_non_negative CHECK (size_bytes IS NULL OR size_bytes >= 0),
    CONSTRAINT media_file_checksum_sha256_format CHECK (
        checksum_sha256 IS NULL OR checksum_sha256 ~ '^[0-9a-f]{64}$'
    )
);

CREATE INDEX media_file_owner_created_idx
    ON media.file (owner_user_id, created_at DESC);

CREATE INDEX media_file_status_idx
    ON media.file (status);

-- +goose Down
DROP TABLE IF EXISTS media.file;
DROP TYPE IF EXISTS media.file_status;
