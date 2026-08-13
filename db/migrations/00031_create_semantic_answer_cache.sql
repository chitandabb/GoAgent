-- +goose Up
CREATE TABLE global_knowledge_generation (
    singleton SMALLINT PRIMARY KEY DEFAULT 1,
    generation BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT global_knowledge_generation_singleton_check CHECK (singleton = 1),
    CONSTRAINT global_knowledge_generation_positive_check CHECK (generation > 0)
);

INSERT INTO global_knowledge_generation (singleton, generation, updated_at)
VALUES (1, 1, now());

CREATE TABLE semantic_answer_cache (
    question_hash CHAR(64) NOT NULL,
    generation BIGINT NOT NULL,
    answer_content TEXT NOT NULL,
    citations JSONB NOT NULL,
    retrieved_sources JSONB NOT NULL,
    source_run_id UUID NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
    model_provider VARCHAR(64) NOT NULL,
    model_id VARCHAR(256) NOT NULL,
    prompt_version VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (question_hash, generation),
    CONSTRAINT semantic_answer_cache_question_hash_check CHECK (question_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT semantic_answer_cache_generation_check CHECK (generation > 0),
    CONSTRAINT semantic_answer_cache_answer_check CHECK (btrim(answer_content) <> '' AND octet_length(answer_content) <= 16384),
    CONSTRAINT semantic_answer_cache_citations_check CHECK (
        jsonb_typeof(citations) = 'array' AND jsonb_array_length(citations) BETWEEN 1 AND 8
    ),
    CONSTRAINT semantic_answer_cache_sources_check CHECK (
        jsonb_typeof(retrieved_sources) = 'array' AND jsonb_array_length(retrieved_sources) BETWEEN 1 AND 64
    ),
    CONSTRAINT semantic_answer_cache_model_provider_check CHECK (btrim(model_provider) <> ''),
    CONSTRAINT semantic_answer_cache_model_id_check CHECK (btrim(model_id) <> ''),
    CONSTRAINT semantic_answer_cache_prompt_version_check CHECK (btrim(prompt_version) <> ''),
    CONSTRAINT semantic_answer_cache_expiry_check CHECK (expires_at > created_at)
);

CREATE INDEX semantic_answer_cache_expiry_idx ON semantic_answer_cache (expires_at);

ALTER TABLE conversation_turn_run_observations
    ADD COLUMN execution_path VARCHAR(32) NOT NULL DEFAULT 'agent',
    ADD COLUMN tool_calls INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN answer_cache_eligible BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN cache_layer VARCHAR(16),
    ADD COLUMN source_run_id UUID REFERENCES conversation_turns(id) ON DELETE SET NULL,
    ADD CONSTRAINT conversation_turn_run_observations_execution_path_check
        CHECK (execution_path IN ('agent', 'semantic_cache_hit')),
    ADD CONSTRAINT conversation_turn_run_observations_tool_calls_check
        CHECK (tool_calls >= 0 AND tool_calls <= 128),
    ADD CONSTRAINT conversation_turn_run_observations_cache_layer_check
        CHECK (cache_layer IS NULL OR cache_layer = 'exact');

-- +goose Down
ALTER TABLE conversation_turn_run_observations
    DROP CONSTRAINT IF EXISTS conversation_turn_run_observations_cache_layer_check,
    DROP CONSTRAINT IF EXISTS conversation_turn_run_observations_tool_calls_check,
    DROP CONSTRAINT IF EXISTS conversation_turn_run_observations_execution_path_check,
    DROP COLUMN IF EXISTS source_run_id,
    DROP COLUMN IF EXISTS cache_layer,
    DROP COLUMN IF EXISTS answer_cache_eligible,
    DROP COLUMN IF EXISTS tool_calls,
    DROP COLUMN IF EXISTS execution_path;

DROP INDEX IF EXISTS semantic_answer_cache_expiry_idx;
DROP TABLE IF EXISTS semantic_answer_cache;
DROP TABLE IF EXISTS global_knowledge_generation;
