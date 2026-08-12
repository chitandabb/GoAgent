-- +goose Up
CREATE TABLE conversation_report_references (
    message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    report_id UUID NOT NULL REFERENCES diagnosis_reports(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, report_id)
);

CREATE INDEX conversation_report_references_report_idx
    ON conversation_report_references (report_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS conversation_report_references_report_idx;
DROP TABLE IF EXISTS conversation_report_references;
