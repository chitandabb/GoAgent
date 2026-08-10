-- +goose Up
CREATE TABLE attachments (
    id UUID PRIMARY KEY,
    owner_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    scope VARCHAR(16) NOT NULL,
    conversation_id UUID REFERENCES conversations(id) ON DELETE RESTRICT,
    idempotency_key UUID NOT NULL,
    upload_request_fingerprint CHAR(64) NOT NULL,
    storage_bucket VARCHAR(32) NOT NULL,
    storage_object_key VARCHAR(1024) NOT NULL,
    storage_object_version VARCHAR(512),
    storage_etag VARCHAR(512) NOT NULL,
    original_filename VARCHAR(512) NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    processing_status VARCHAR(16) NOT NULL,
    uploaded_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT attachments_scope_check CHECK (scope IN ('session', 'personal')),
    CONSTRAINT attachments_scope_conversation_check CHECK (
        (scope = 'session' AND conversation_id IS NOT NULL)
        OR (scope = 'personal' AND conversation_id IS NULL)
    ),
    CONSTRAINT attachments_idempotency_key_not_nil CHECK (idempotency_key <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT attachments_fingerprint_check CHECK (upload_request_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT attachments_bucket_check CHECK (storage_bucket = 'attachments'),
    CONSTRAINT attachments_object_key_not_blank CHECK (btrim(storage_object_key) <> ''),
    CONSTRAINT attachments_etag_not_blank CHECK (btrim(storage_etag) <> ''),
    CONSTRAINT attachments_filename_not_blank CHECK (btrim(original_filename) <> ''),
    CONSTRAINT attachments_media_type_not_blank CHECK (btrim(content_type) <> ''),
    CONSTRAINT attachments_size_check CHECK (size_bytes > 0),
    CONSTRAINT attachments_sha256_check CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT attachments_status_check CHECK (processing_status = 'uploaded'),
    CONSTRAINT attachments_uploaded_fields_check CHECK (
        processing_status = 'uploaded' AND uploaded_at IS NOT NULL
    )
);

CREATE UNIQUE INDEX attachments_owner_idempotency_idx
    ON attachments (owner_user_id, idempotency_key);
CREATE INDEX attachments_conversation_idx
    ON attachments (owner_user_id, conversation_id, created_at DESC, id DESC);
CREATE INDEX attachments_sha256_idx
    ON attachments (owner_user_id, content_sha256);

CREATE TABLE conversation_message_attachments (
    message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    attachment_id UUID NOT NULL REFERENCES attachments(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL,
    purpose VARCHAR(64) NOT NULL DEFAULT 'context',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, attachment_id),
    CONSTRAINT conversation_message_attachments_position_check CHECK (position >= 0),
    CONSTRAINT conversation_message_attachments_purpose_check CHECK (btrim(purpose) <> ''),
    CONSTRAINT conversation_message_attachments_position_unique UNIQUE (message_id, position)
);

CREATE INDEX conversation_message_attachments_attachment_idx
    ON conversation_message_attachments (attachment_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS conversation_message_attachments_attachment_idx;
DROP TABLE IF EXISTS conversation_message_attachments;
DROP INDEX IF EXISTS attachments_sha256_idx;
DROP INDEX IF EXISTS attachments_conversation_idx;
DROP INDEX IF EXISTS attachments_owner_idempotency_idx;
DROP TABLE IF EXISTS attachments;
