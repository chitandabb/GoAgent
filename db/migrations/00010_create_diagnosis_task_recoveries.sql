-- +goose Up
CREATE TABLE diagnosis_task_recoveries (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    recovered_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    idempotency_key VARCHAR(128) NOT NULL,
    reason TEXT NOT NULL,
    previous_error_code VARCHAR(64) NOT NULL,
    previous_error_message TEXT NOT NULL,
    previous_attempt_count INTEGER NOT NULL,
    task_event_seq BIGINT NOT NULL,
    outbox_event_id UUID NOT NULL REFERENCES outbox_events(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT diagnosis_task_recoveries_key_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT diagnosis_task_recoveries_reason_not_blank CHECK (btrim(reason) <> ''),
    CONSTRAINT diagnosis_task_recoveries_reason_length CHECK (char_length(reason) <= 1000),
    CONSTRAINT diagnosis_task_recoveries_error_code_not_blank CHECK (btrim(previous_error_code) <> ''),
    CONSTRAINT diagnosis_task_recoveries_error_message_not_blank CHECK (btrim(previous_error_message) <> ''),
    CONSTRAINT diagnosis_task_recoveries_attempt_positive CHECK (previous_attempt_count > 0),
    CONSTRAINT diagnosis_task_recoveries_event_seq_positive CHECK (task_event_seq > 0),
    CONSTRAINT diagnosis_task_recoveries_task_event_fk
        FOREIGN KEY (task_id, task_event_seq)
        REFERENCES task_events(task_id, seq) ON DELETE RESTRICT,
    CONSTRAINT diagnosis_task_recoveries_idempotency_unique
        UNIQUE (task_id, recovered_by, idempotency_key),
    CONSTRAINT diagnosis_task_recoveries_task_event_unique
        UNIQUE (task_id, task_event_seq)
);

CREATE INDEX diagnosis_task_recoveries_task_created_idx
    ON diagnosis_task_recoveries (task_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS diagnosis_task_recoveries;
