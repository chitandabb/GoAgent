-- +goose Up
ALTER TABLE conversation_turn_run_observations
    ADD COLUMN error_type VARCHAR(64);

ALTER TABLE conversation_turn_run_observations
    ADD CONSTRAINT conversation_turn_run_observations_error_type_check
        CHECK (
            error_type IS NULL OR (
                btrim(error_type) <> '' AND
                error_type = btrim(error_type) AND
                error_type ~ '^[a-z0-9_-]+$'
            )
        ),
    ADD CONSTRAINT conversation_turn_run_observations_failure_outcome_check
        CHECK (
            (outcome = 'failed' AND error_type IS NOT NULL) OR
            (outcome <> 'failed' AND error_type IS NULL)
        );

-- +goose Down
ALTER TABLE conversation_turn_run_observations
    DROP CONSTRAINT IF EXISTS conversation_turn_run_observations_failure_outcome_check,
    DROP CONSTRAINT IF EXISTS conversation_turn_run_observations_error_type_check,
    DROP COLUMN IF EXISTS error_type;
