-- +goose Up
CREATE TABLE task_events (
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    seq BIGINT NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_schema_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, seq),
    CONSTRAINT task_events_seq_positive CHECK (seq > 0),
    CONSTRAINT task_events_type_not_blank CHECK (btrim(event_type) <> ''),
    CONSTRAINT task_events_schema_version_positive CHECK (payload_schema_version > 0),
    CONSTRAINT task_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX task_events_task_created_idx
    ON task_events (task_id, created_at, seq);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id UUID NOT NULL,
    correlation_id UUID NOT NULL,
    causation_id UUID,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_schema_version INTEGER NOT NULL DEFAULT 1,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at TIMESTAMPTZ,
    locked_by VARCHAR(128),
    locked_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    requeue_count INTEGER NOT NULL DEFAULT 0,
    last_requeued_at TIMESTAMPTZ,
    last_requeued_by UUID REFERENCES users(id) ON DELETE SET NULL,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT outbox_events_type_not_blank CHECK (btrim(event_type) <> ''),
    CONSTRAINT outbox_events_aggregate_type_not_blank CHECK (btrim(aggregate_type) <> ''),
    CONSTRAINT outbox_events_schema_version_positive CHECK (payload_schema_version > 0),
    CONSTRAINT outbox_events_attempt_non_negative CHECK (attempt_count >= 0),
    CONSTRAINT outbox_events_requeue_non_negative CHECK (requeue_count >= 0),
    CONSTRAINT outbox_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX outbox_events_available_idx
    ON outbox_events (available_at, locked_until, created_at)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_aggregate_idx
    ON outbox_events (aggregate_type, aggregate_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS task_events;
