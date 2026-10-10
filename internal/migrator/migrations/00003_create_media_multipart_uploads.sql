-- +goose Up
CREATE TABLE media.multipart_upload (
    file_id uuid PRIMARY KEY REFERENCES media.file(id),
    s3_upload_id text NOT NULL CHECK (s3_upload_id <> ''),
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes > 0),
    part_size_bytes bigint NOT NULL CHECK (part_size_bytes BETWEEN 5242880 AND 5368709120),
    status text NOT NULL DEFAULT 'uploading'
        CHECK (status IN ('uploading', 'completing', 'completed', 'aborted')),
    completion_manifest jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(completion_manifest) = 'array'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CHECK (expected_size_bytes <= part_size_bytes * 10000),
    CHECK (expires_at > created_at)
);
CREATE INDEX media_multipart_recovery_idx ON media.multipart_upload (expires_at)
    WHERE status IN ('uploading', 'completing');

-- +goose Down
-- Removes upload tracking only; abort unfinished S3 uploads before rollback.
DROP TABLE media.multipart_upload;
