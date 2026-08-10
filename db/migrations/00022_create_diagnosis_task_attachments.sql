-- +goose Up
CREATE TABLE diagnosis_task_attachments (
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    attachment_id UUID NOT NULL REFERENCES attachments(id) ON DELETE RESTRICT,
    source_message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE RESTRICT,
    purpose VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, attachment_id),
    CONSTRAINT diagnosis_task_attachments_purpose_check CHECK (btrim(purpose) <> '')
);

CREATE INDEX diagnosis_task_attachments_attachment_idx
    ON diagnosis_task_attachments (attachment_id, created_at DESC);
CREATE INDEX diagnosis_task_attachments_source_message_idx
    ON diagnosis_task_attachments (source_message_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS diagnosis_task_attachments_source_message_idx;
DROP INDEX IF EXISTS diagnosis_task_attachments_attachment_idx;
DROP TABLE IF EXISTS diagnosis_task_attachments;
