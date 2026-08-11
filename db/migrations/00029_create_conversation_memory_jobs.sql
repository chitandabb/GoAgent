-- +goose Up
CREATE TABLE conversation_memory_jobs (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    source_turn_id UUID NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
    requested_through_seq BIGINT NOT NULL,
    base_snapshot_id UUID NULL REFERENCES conversation_memory_snapshots(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    claim_owner VARCHAR(128) NULL,
    lease_until TIMESTAMPTZ NULL,
    heartbeat_at TIMESTAMPTZ NULL,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL,
    activated_snapshot_id UUID NULL REFERENCES conversation_memory_snapshots(id) ON DELETE SET NULL,
    activation_result VARCHAR(32) NULL,
    failure_code VARCHAR(128) NULL,
    failure_summary VARCHAR(1000) NULL,
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT conversation_memory_jobs_coverage_unique UNIQUE (conversation_id, requested_through_seq),
    CONSTRAINT conversation_memory_jobs_coverage_check CHECK (requested_through_seq >= 1),
    CONSTRAINT conversation_memory_jobs_attempt_check CHECK (
        max_attempts BETWEEN 1 AND 10 AND attempt_count BETWEEN 0 AND max_attempts
    ),
    CONSTRAINT conversation_memory_jobs_fencing_check CHECK (fencing_token >= 0),
    CONSTRAINT conversation_memory_jobs_status_check CHECK (
        status IN ('pending', 'running', 'retry_wait', 'succeeded', 'failed')
    ),
    CONSTRAINT conversation_memory_jobs_claim_check CHECK (
        (status = 'running' AND claim_owner IS NOT NULL AND btrim(claim_owner) <> '' AND
         lease_until IS NOT NULL AND heartbeat_at IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NULL) OR
        (status <> 'running' AND claim_owner IS NULL AND lease_until IS NULL AND heartbeat_at IS NULL)
    ),
    CONSTRAINT conversation_memory_jobs_terminal_check CHECK (
        (status = 'succeeded' AND activated_snapshot_id IS NOT NULL AND
         activation_result IN ('activated', 'already_current', 'cas_winner') AND completed_at IS NOT NULL) OR
        (status = 'failed' AND activated_snapshot_id IS NULL AND activation_result IS NULL AND
         failure_code IS NOT NULL AND btrim(failure_code) <> '' AND
         failure_summary IS NOT NULL AND btrim(failure_summary) <> '' AND completed_at IS NOT NULL) OR
        (status IN ('pending', 'running', 'retry_wait') AND activated_snapshot_id IS NULL AND
         activation_result IS NULL AND completed_at IS NULL)
    )
);

CREATE INDEX conversation_memory_jobs_available_idx
    ON conversation_memory_jobs (available_at, created_at)
    WHERE status IN ('pending', 'retry_wait');

CREATE INDEX conversation_memory_jobs_conversation_idx
    ON conversation_memory_jobs (conversation_id, requested_through_seq DESC);

-- +goose Down
DROP TABLE IF EXISTS conversation_memory_jobs;
