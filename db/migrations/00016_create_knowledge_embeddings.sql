-- +goose Up
CREATE TABLE knowledge_embedding_profiles (
    id UUID PRIMARY KEY,
    profile_key VARCHAR(128) NOT NULL,
    provider VARCHAR(128) NOT NULL,
    model VARCHAR(128) NOT NULL,
    dimensions INTEGER NOT NULL,
    distance_metric VARCHAR(16) NOT NULL,
    query_input_type VARCHAR(16) NOT NULL,
    document_input_type VARCHAR(16) NOT NULL,
    normalized BOOLEAN NOT NULL,
    config_version VARCHAR(128) NOT NULL,
    fingerprint CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL,
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_embedding_profiles_key_not_blank CHECK (btrim(profile_key) <> ''),
    CONSTRAINT knowledge_embedding_profiles_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT knowledge_embedding_profiles_model_not_blank CHECK (btrim(model) <> ''),
    CONSTRAINT knowledge_embedding_profiles_dimensions_check CHECK (dimensions = 1024),
    CONSTRAINT knowledge_embedding_profiles_distance_check CHECK (distance_metric = 'cosine'),
    CONSTRAINT knowledge_embedding_profiles_input_type_check CHECK (
        query_input_type = 'query' AND document_input_type = 'document'
    ),
    CONSTRAINT knowledge_embedding_profiles_config_version_not_blank CHECK (btrim(config_version) <> ''),
    CONSTRAINT knowledge_embedding_profiles_fingerprint_check CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT knowledge_embedding_profiles_status_check CHECK (status IN ('staging', 'active', 'retired', 'failed')),
    CONSTRAINT knowledge_embedding_profiles_activation_check CHECK (
        (status = 'active' AND activated_at IS NOT NULL)
        OR (status <> 'active')
    )
);

CREATE UNIQUE INDEX knowledge_embedding_profiles_fingerprint_unique_idx
    ON knowledge_embedding_profiles (fingerprint);
CREATE UNIQUE INDEX knowledge_embedding_profiles_one_active_idx
    ON knowledge_embedding_profiles ((true))
    WHERE status = 'active';

CREATE TABLE knowledge_chunk_embeddings (
    chunk_id UUID NOT NULL REFERENCES knowledge_chunks(id) ON DELETE CASCADE,
    profile_id UUID NOT NULL REFERENCES knowledge_embedding_profiles(id) ON DELETE RESTRICT,
    content_sha256 CHAR(64) NOT NULL,
    embedding VECTOR(1024) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chunk_id, profile_id),
    CONSTRAINT knowledge_chunk_embeddings_sha256_check CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT knowledge_chunk_embeddings_dimensions_check CHECK (vector_dims(embedding) = 1024)
);

CREATE INDEX knowledge_chunk_embeddings_profile_chunk_idx
    ON knowledge_chunk_embeddings (profile_id, chunk_id);

-- +goose Down
DROP TABLE IF EXISTS knowledge_chunk_embeddings;
DROP TABLE IF EXISTS knowledge_embedding_profiles;
