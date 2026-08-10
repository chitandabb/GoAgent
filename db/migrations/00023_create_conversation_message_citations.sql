-- +goose Up
CREATE TABLE conversation_message_citations (
    message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_ref TEXT NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT conversation_message_citations_position_check
        CHECK (position >= 0 AND position < 20),
    CONSTRAINT conversation_message_citations_source_type_check
        CHECK (source_type IN ('knowledge_chunk', 'attachment', 'web')),
    CONSTRAINT conversation_message_citations_source_ref_check
        CHECK (btrim(source_ref) <> '' AND source_ref = btrim(source_ref) AND octet_length(source_ref) <= 2048),
    CONSTRAINT conversation_message_citations_content_sha256_check
        CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT conversation_message_citations_position_unique
        UNIQUE (message_id, position),
    CONSTRAINT conversation_message_citations_source_unique
        UNIQUE (message_id, source_type, source_ref)
);

CREATE INDEX conversation_message_citations_source_idx
    ON conversation_message_citations (source_type, source_ref);

-- +goose Down
DROP INDEX IF EXISTS conversation_message_citations_source_idx;
DROP TABLE IF EXISTS conversation_message_citations;
