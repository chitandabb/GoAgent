-- +goose Up
CREATE TABLE knowledge_documents (
    id UUID PRIMARY KEY,
    scope VARCHAR(16) NOT NULL,
    owner_user_id UUID REFERENCES users(id) ON DELETE RESTRICT,
    title VARCHAR(512) NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_documents_scope_check CHECK (scope IN ('global', 'personal')),
    CONSTRAINT knowledge_documents_scope_owner_check CHECK (
        (scope = 'global' AND owner_user_id IS NULL)
        OR (scope = 'personal' AND owner_user_id IS NOT NULL)
    ),
    CONSTRAINT knowledge_documents_title_not_blank CHECK (btrim(title) <> '')
);

CREATE INDEX knowledge_documents_owner_active_idx
    ON knowledge_documents (owner_user_id, created_at DESC)
    WHERE scope = 'personal' AND deleted_at IS NULL;
CREATE INDEX knowledge_documents_global_active_idx
    ON knowledge_documents (created_at DESC)
    WHERE scope = 'global' AND deleted_at IS NULL;

CREATE TABLE knowledge_document_versions (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES knowledge_documents(id) ON DELETE RESTRICT,
    version INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL,
    is_current BOOLEAN NOT NULL DEFAULT false,
    source_media_type VARCHAR(255) NOT NULL,
    source_size_bytes BIGINT NOT NULL,
    source_sha256 CHAR(64) NOT NULL,
    parser_version VARCHAR(128) NOT NULL,
    parser_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_code VARCHAR(128),
    error_message TEXT,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_document_versions_version_positive CHECK (version > 0),
    CONSTRAINT knowledge_document_versions_status_check CHECK (
        status IN ('processing', 'ready', 'failed', 'retired')
    ),
    CONSTRAINT knowledge_document_versions_size_check CHECK (source_size_bytes >= 0),
    CONSTRAINT knowledge_document_versions_sha256_check CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT knowledge_document_versions_media_type_not_blank CHECK (btrim(source_media_type) <> ''),
    CONSTRAINT knowledge_document_versions_parser_not_blank CHECK (btrim(parser_version) <> ''),
    CONSTRAINT knowledge_document_versions_metadata_object_check CHECK (jsonb_typeof(parser_metadata) = 'object'),
    CONSTRAINT knowledge_document_versions_current_ready_check CHECK (NOT is_current OR status = 'ready'),
    CONSTRAINT knowledge_document_versions_terminal_fields_check CHECK (
        (status IN ('ready', 'retired') AND completed_at IS NOT NULL AND error_code IS NULL AND error_message IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error_code IS NOT NULL)
        OR (status = 'processing' AND completed_at IS NULL AND error_code IS NULL AND error_message IS NULL)
    )
);

CREATE UNIQUE INDEX knowledge_document_versions_document_version_unique_idx
    ON knowledge_document_versions (document_id, version);
CREATE UNIQUE INDEX knowledge_document_versions_one_current_idx
    ON knowledge_document_versions (document_id)
    WHERE is_current;
CREATE INDEX knowledge_document_versions_status_created_idx
    ON knowledge_document_versions (status, created_at);

CREATE TABLE knowledge_chunks (
    id UUID PRIMARY KEY,
    document_version_id UUID NOT NULL REFERENCES knowledge_document_versions(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    page_number INTEGER,
    element_index INTEGER,
    element_type VARCHAR(32) NOT NULL,
    section_path JSONB NOT NULL DEFAULT '[]'::jsonb,
    content_text TEXT NOT NULL,
    search_text TEXT NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_chunks_ordinal_check CHECK (ordinal >= 0),
    CONSTRAINT knowledge_chunks_page_check CHECK (page_number IS NULL OR page_number > 0),
    CONSTRAINT knowledge_chunks_element_index_check CHECK (element_index IS NULL OR element_index >= 0),
    CONSTRAINT knowledge_chunks_element_type_check CHECK (
        element_type IN ('text', 'table', 'ocr_text', 'image_description')
    ),
    CONSTRAINT knowledge_chunks_section_path_array_check CHECK (jsonb_typeof(section_path) = 'array'),
    CONSTRAINT knowledge_chunks_content_not_blank CHECK (btrim(content_text) <> ''),
    CONSTRAINT knowledge_chunks_search_not_blank CHECK (btrim(search_text) <> ''),
    CONSTRAINT knowledge_chunks_sha256_check CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT knowledge_chunks_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX knowledge_chunks_version_ordinal_unique_idx
    ON knowledge_chunks (document_version_id, ordinal);
CREATE INDEX knowledge_chunks_version_idx
    ON knowledge_chunks (document_version_id);
CREATE INDEX knowledge_chunks_search_vector_idx
    ON knowledge_chunks USING GIN (search_vector);

-- +goose Down
DROP TABLE IF EXISTS knowledge_chunks;
DROP TABLE IF EXISTS knowledge_document_versions;
DROP TABLE IF EXISTS knowledge_documents;
