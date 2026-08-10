-- +goose Up
ALTER TABLE conversation_turns
    ADD COLUMN failure_code VARCHAR(64),
    ADD COLUMN retry_at TIMESTAMPTZ;

UPDATE conversation_turns
SET failure_code = 'agent_execution_failed'
WHERE status = 'failed' AND failure_code IS NULL;

ALTER TABLE conversation_turns
    DROP CONSTRAINT conversation_turns_completion_check;

ALTER TABLE conversation_turns
    ADD CONSTRAINT conversation_turns_completion_check CHECK (
        (status = 'queued' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL
            AND ((failure_code IS NULL AND retry_at IS NULL) OR (failure_code IS NOT NULL AND retry_at IS NOT NULL)))
        OR (status = 'running' AND lease_expires_at IS NOT NULL AND assistant_message_id IS NULL AND completed_at IS NULL
            AND failure_code IS NULL AND retry_at IS NULL)
        OR (status = 'failed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL
            AND failure_code IS NOT NULL AND retry_at IS NULL)
        OR (status = 'completed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NOT NULL AND completed_at IS NOT NULL
            AND failure_code IS NULL AND retry_at IS NULL)
    );

CREATE TABLE conversation_turn_events (
    turn_id UUID NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL CHECK (seq > 0),
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_schema_version INTEGER NOT NULL DEFAULT 1 CHECK (payload_schema_version > 0),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (turn_id, seq)
);

CREATE INDEX conversation_turn_events_conversation_idx
    ON conversation_turn_events (conversation_id, turn_id, seq);

-- +goose Down
DROP INDEX IF EXISTS conversation_turn_events_conversation_idx;
DROP TABLE IF EXISTS conversation_turn_events;

ALTER TABLE conversation_turns
    DROP CONSTRAINT conversation_turns_completion_check;

ALTER TABLE conversation_turns
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS retry_at;

ALTER TABLE conversation_turns
    ADD CONSTRAINT conversation_turns_completion_check CHECK (
        (status = 'queued' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'running' AND lease_expires_at IS NOT NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'failed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NOT NULL AND completed_at IS NOT NULL)
    );
