-- +goose Up
ALTER TABLE semantic_answer_cache
    ADD COLUMN question_text TEXT,
    ADD COLUMN embedding_profile_id UUID REFERENCES knowledge_embedding_profiles(id) ON DELETE RESTRICT,
    ADD COLUMN embedding_profile_fingerprint CHAR(64),
    ADD COLUMN normalization_version VARCHAR(64),
    ADD COLUMN question_embedding VECTOR(1024),
    ADD CONSTRAINT semantic_answer_cache_question_text_check
        CHECK (question_text IS NULL OR (btrim(question_text) <> '' AND char_length(question_text) <= 32000)),
    ADD CONSTRAINT semantic_answer_cache_profile_fingerprint_check
        CHECK (embedding_profile_fingerprint IS NULL OR embedding_profile_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT semantic_answer_cache_normalization_version_check
        CHECK (normalization_version IS NULL OR btrim(normalization_version) <> ''),
    ADD CONSTRAINT semantic_answer_cache_embedding_dimensions_check
        CHECK (question_embedding IS NULL OR vector_dims(question_embedding) = 1024),
    ADD CONSTRAINT semantic_answer_cache_semantic_fields_check CHECK (
        (question_text IS NULL AND embedding_profile_id IS NULL AND embedding_profile_fingerprint IS NULL
            AND normalization_version IS NULL AND question_embedding IS NULL)
        OR
        (question_text IS NOT NULL AND embedding_profile_id IS NOT NULL AND embedding_profile_fingerprint IS NOT NULL
            AND normalization_version IS NOT NULL AND question_embedding IS NOT NULL)
    );

CREATE INDEX semantic_answer_cache_semantic_profile_idx
    ON semantic_answer_cache (generation, embedding_profile_id, normalization_version)
    WHERE question_embedding IS NOT NULL;

ALTER TABLE conversation_turn_run_observations
    DROP CONSTRAINT conversation_turn_run_observations_cache_layer_check,
    ADD CONSTRAINT conversation_turn_run_observations_cache_layer_check
        CHECK (cache_layer IS NULL OR cache_layer IN ('exact', 'semantic'));

-- +goose Down
-- Version 00031 cannot represent semantic cache observations. Removing only
-- the disposable cache-hit observation preserves conversation messages while
-- avoiding a false rewrite from semantic to exact.
DELETE FROM conversation_turn_run_observations
WHERE execution_path = 'semantic_cache_hit' AND cache_layer = 'semantic';

ALTER TABLE conversation_turn_run_observations
    DROP CONSTRAINT conversation_turn_run_observations_cache_layer_check,
    ADD CONSTRAINT conversation_turn_run_observations_cache_layer_check
        CHECK (cache_layer IS NULL OR cache_layer = 'exact');

DROP INDEX IF EXISTS semantic_answer_cache_semantic_profile_idx;

ALTER TABLE semantic_answer_cache
    DROP CONSTRAINT IF EXISTS semantic_answer_cache_semantic_fields_check,
    DROP CONSTRAINT IF EXISTS semantic_answer_cache_embedding_dimensions_check,
    DROP CONSTRAINT IF EXISTS semantic_answer_cache_normalization_version_check,
    DROP CONSTRAINT IF EXISTS semantic_answer_cache_profile_fingerprint_check,
    DROP CONSTRAINT IF EXISTS semantic_answer_cache_question_text_check,
    DROP COLUMN IF EXISTS question_embedding,
    DROP COLUMN IF EXISTS normalization_version,
    DROP COLUMN IF EXISTS embedding_profile_fingerprint,
    DROP COLUMN IF EXISTS embedding_profile_id,
    DROP COLUMN IF EXISTS question_text;
