-- +goose Up
CREATE TABLE conversation_turn_run_observations (
    turn_id UUID PRIMARY KEY REFERENCES conversation_turns(id) ON DELETE CASCADE,
    model_provider VARCHAR(64) NOT NULL,
    model_id VARCHAR(256) NOT NULL,
    prompt_version VARCHAR(128) NOT NULL,
    outcome VARCHAR(32) NOT NULL,
    model_calls INTEGER NOT NULL,
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    cached_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    duration_millis BIGINT NOT NULL,
    degraded_channels JSONB NOT NULL DEFAULT '[]'::jsonb,
    sources_truncated BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT conversation_turn_run_observations_model_provider_check
        CHECK (btrim(model_provider) <> '' AND model_provider = btrim(model_provider)),
    CONSTRAINT conversation_turn_run_observations_model_id_check
        CHECK (btrim(model_id) <> '' AND model_id = btrim(model_id)),
    CONSTRAINT conversation_turn_run_observations_prompt_version_check
        CHECK (btrim(prompt_version) <> '' AND prompt_version = btrim(prompt_version)),
    CONSTRAINT conversation_turn_run_observations_outcome_check
        CHECK (outcome IN ('answered', 'insufficient_evidence', 'degraded', 'failed')),
    CONSTRAINT conversation_turn_run_observations_usage_check
        CHECK (
            model_calls >= 0 AND prompt_tokens >= 0 AND completion_tokens >= 0 AND
            total_tokens >= prompt_tokens + completion_tokens AND cached_tokens >= 0 AND reasoning_tokens >= 0
        ),
    CONSTRAINT conversation_turn_run_observations_duration_check
        CHECK (duration_millis >= 0 AND duration_millis <= 300000),
    CONSTRAINT conversation_turn_run_observations_degraded_channels_check
        CHECK (jsonb_typeof(degraded_channels) = 'array' AND jsonb_array_length(degraded_channels) <= 32)
);

CREATE TABLE conversation_turn_retrieved_sources (
    turn_id UUID NOT NULL REFERENCES conversation_turn_run_observations(turn_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_ref TEXT NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (turn_id, position),
    CONSTRAINT conversation_turn_retrieved_sources_position_check
        CHECK (position >= 0 AND position < 200),
    CONSTRAINT conversation_turn_retrieved_sources_source_type_check
        CHECK (source_type IN ('knowledge_chunk', 'attachment', 'web')),
    CONSTRAINT conversation_turn_retrieved_sources_source_ref_check
        CHECK (btrim(source_ref) <> '' AND source_ref = btrim(source_ref) AND octet_length(source_ref) <= 2048),
    CONSTRAINT conversation_turn_retrieved_sources_content_sha256_check
        CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT conversation_turn_retrieved_sources_source_unique
        UNIQUE (turn_id, source_type, source_ref)
);

CREATE INDEX conversation_turn_retrieved_sources_source_idx
    ON conversation_turn_retrieved_sources (source_type, source_ref);

-- +goose Down
DROP INDEX IF EXISTS conversation_turn_retrieved_sources_source_idx;
DROP TABLE IF EXISTS conversation_turn_retrieved_sources;
DROP TABLE IF EXISTS conversation_turn_run_observations;
