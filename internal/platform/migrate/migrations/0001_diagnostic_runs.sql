CREATE TABLE IF NOT EXISTS mesguard_diagnostic_runs (
    id UUID PRIMARY KEY,
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    request_text TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    summary TEXT,
    error_text TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mesguard_diagnostic_runs_subject
    ON mesguard_diagnostic_runs (subject_type, subject_id, created_at DESC);

CREATE TABLE IF NOT EXISTS mesguard_diagnostic_events (
    id BIGSERIAL PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES mesguard_diagnostic_runs (id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (run_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_mesguard_diagnostic_events_run
    ON mesguard_diagnostic_events (run_id, sequence);
