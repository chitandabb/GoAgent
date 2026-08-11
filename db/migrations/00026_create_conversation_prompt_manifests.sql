-- +goose Up
CREATE TABLE conversation_prompt_manifests (
    turn_id UUID PRIMARY KEY REFERENCES conversation_turn_run_observations(turn_id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL,
    preflight_status VARCHAR(16) NOT NULL,
    failure_stage VARCHAR(64) NOT NULL DEFAULT '',
    prompt_identity_available BOOLEAN NOT NULL,
    estimate_available BOOLEAN NOT NULL,
    prompt_epoch_id VARCHAR(64) NOT NULL,
    stable_prefix_fingerprint VARCHAR(64) NOT NULL,
    model_profile VARCHAR(128) NOT NULL,
    model_profile_fingerprint VARCHAR(64) NOT NULL,
    system_prompt_version VARCHAR(128) NOT NULL,
    system_prompt_fingerprint VARCHAR(64) NOT NULL,
    tool_schema_fingerprint VARCHAR(64) NOT NULL,
    skill_prompt_fingerprint VARCHAR(64) NOT NULL,
    summary_fingerprint VARCHAR(64) NOT NULL,
    tail_from_seq BIGINT NOT NULL,
    tail_through_seq BIGINT NOT NULL,
    available_input_tokens INTEGER NOT NULL,
    estimated_prompt_tokens INTEGER NOT NULL,
    estimated_upper_bound_tokens INTEGER NOT NULL,
    tool_growth_reserve_tokens INTEGER NOT NULL,
    estimation_method VARCHAR(32) NOT NULL,
    soft_threshold_ratio DOUBLE PRECISION NOT NULL,
    hard_threshold_ratio DOUBLE PRECISION NOT NULL,
    soft_threshold_reached BOOLEAN NOT NULL,
    hard_threshold_reached BOOLEAN NOT NULL,
    exceeds_hard_window BOOLEAN NOT NULL,
    actual_usage_available BOOLEAN NOT NULL,
    actual_prompt_tokens INTEGER NOT NULL,
    cache_hit_tokens INTEGER NOT NULL,
    cache_miss_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    estimation_error_ratio DOUBLE PRECISION NOT NULL,
    preflight_duration_micros BIGINT NOT NULL,
    run_duration_millis BIGINT NOT NULL,
    context_degraded BOOLEAN NOT NULL,
    degraded_reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT conversation_prompt_manifests_schema_check CHECK (schema_version = 1),
    CONSTRAINT conversation_prompt_manifests_status_check CHECK (
        (preflight_status = 'succeeded' AND failure_stage = '' AND
         prompt_identity_available AND estimate_available) OR
        (preflight_status = 'failed' AND failure_stage ~ '^[A-Za-z0-9][A-Za-z0-9._-]*$' AND
         NOT estimate_available AND context_degraded)
    ),
    CONSTRAINT conversation_prompt_manifests_identity_check CHECK (
        (prompt_identity_available AND
         prompt_epoch_id ~ '^[0-9a-f]{64}$' AND stable_prefix_fingerprint ~ '^[0-9a-f]{64}$' AND
         model_profile_fingerprint ~ '^[0-9a-f]{64}$' AND
         system_prompt_fingerprint ~ '^[0-9a-f]{64}$' AND
         tool_schema_fingerprint ~ '^[0-9a-f]{64}$' AND
         skill_prompt_fingerprint ~ '^[0-9a-f]{64}$' AND summary_fingerprint ~ '^[0-9a-f]{64}$') OR
        (NOT prompt_identity_available AND prompt_epoch_id = '' AND stable_prefix_fingerprint = '' AND
         model_profile_fingerprint = '' AND system_prompt_fingerprint = '' AND
         tool_schema_fingerprint = '' AND skill_prompt_fingerprint = '' AND summary_fingerprint = '')
    ),
    CONSTRAINT conversation_prompt_manifests_profile_check
        CHECK (btrim(model_profile) <> '' AND model_profile = btrim(model_profile)),
    CONSTRAINT conversation_prompt_manifests_prompt_version_check
        CHECK (btrim(system_prompt_version) <> '' AND system_prompt_version = btrim(system_prompt_version)),
    CONSTRAINT conversation_prompt_manifests_tail_check
        CHECK (tail_from_seq >= 1 AND tail_through_seq >= tail_from_seq),
    CONSTRAINT conversation_prompt_manifests_estimate_check CHECK (
        available_input_tokens >= 1 AND tool_growth_reserve_tokens >= 0 AND
        ((estimate_available AND estimated_prompt_tokens >= 0 AND
          estimated_upper_bound_tokens >= estimated_prompt_tokens + tool_growth_reserve_tokens AND
          estimation_method IN ('local_exact', 'local_calibrated', 'conservative_heuristic') AND
          exceeds_hard_window = (estimated_upper_bound_tokens > available_input_tokens)) OR
         (NOT estimate_available AND estimated_prompt_tokens = 0 AND estimated_upper_bound_tokens = 0 AND
          estimation_method = '' AND NOT soft_threshold_reached AND NOT hard_threshold_reached AND
          NOT exceeds_hard_window))
    ),
    CONSTRAINT conversation_prompt_manifests_threshold_check CHECK (
        soft_threshold_ratio > 0 AND hard_threshold_ratio > soft_threshold_ratio AND hard_threshold_ratio < 1
    ),
    CONSTRAINT conversation_prompt_manifests_actual_usage_check CHECK (
        (actual_usage_available AND actual_prompt_tokens >= 0 AND cache_hit_tokens >= 0 AND
         cache_hit_tokens <= actual_prompt_tokens AND cache_miss_tokens = actual_prompt_tokens - cache_hit_tokens AND
         completion_tokens >= 0 AND (estimate_available OR estimation_error_ratio = 0)) OR
        (NOT actual_usage_available AND actual_prompt_tokens = 0 AND cache_hit_tokens = 0 AND
         cache_miss_tokens = 0 AND completion_tokens = 0 AND estimation_error_ratio = 0)
    ),
    CONSTRAINT conversation_prompt_manifests_duration_check CHECK (
        preflight_duration_micros >= 0 AND preflight_duration_micros <= 300000000 AND
        run_duration_millis >= 0 AND run_duration_millis <= 300000
    ),
    CONSTRAINT conversation_prompt_manifests_degraded_reasons_check CHECK (
        jsonb_typeof(degraded_reasons) = 'array' AND jsonb_array_length(degraded_reasons) <= 16
    )
);

CREATE INDEX conversation_prompt_manifests_profile_created_idx
    ON conversation_prompt_manifests (model_profile, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS conversation_prompt_manifests_profile_created_idx;
DROP TABLE IF EXISTS conversation_prompt_manifests;
