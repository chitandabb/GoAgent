-- +goose Up
ALTER TABLE conversation_turns
    ADD COLUMN lease_owner VARCHAR(128);

ALTER TABLE conversation_turns
    DROP CONSTRAINT conversation_turns_status_check,
    DROP CONSTRAINT conversation_turns_completion_check,
    DROP CONSTRAINT conversation_turns_attempt_positive;

ALTER TABLE conversation_turns
    ADD CONSTRAINT conversation_turns_status_check CHECK (status IN ('queued', 'running', 'failed', 'completed')),
    ADD CONSTRAINT conversation_turns_attempt_nonnegative CHECK (attempt_count >= 0),
    ADD CONSTRAINT conversation_turns_completion_check CHECK (
        (status = 'queued' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'running' AND lease_expires_at IS NOT NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'failed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND lease_expires_at IS NULL AND lease_owner IS NULL AND assistant_message_id IS NOT NULL AND completed_at IS NOT NULL)
    );

DROP INDEX conversation_turns_one_running_per_conversation_idx;
CREATE UNIQUE INDEX conversation_turns_one_active_per_conversation_idx
    ON conversation_turns (conversation_id)
    WHERE status IN ('queued', 'running');

CREATE INDEX conversation_turns_claim_idx
    ON conversation_turns (status, lease_expires_at, updated_at, id)
    WHERE status IN ('queued', 'running');

-- +goose Down
UPDATE conversation_turns
SET status = 'failed', attempt_count = GREATEST(attempt_count, 1), updated_at = CURRENT_TIMESTAMP
WHERE status = 'queued';

DROP INDEX IF EXISTS conversation_turns_claim_idx;
DROP INDEX IF EXISTS conversation_turns_one_active_per_conversation_idx;
CREATE UNIQUE INDEX conversation_turns_one_running_per_conversation_idx
    ON conversation_turns (conversation_id)
    WHERE status = 'running';
ALTER TABLE conversation_turns
    DROP CONSTRAINT conversation_turns_completion_check,
    DROP CONSTRAINT conversation_turns_status_check,
    DROP CONSTRAINT IF EXISTS conversation_turns_attempt_nonnegative,
    DROP CONSTRAINT IF EXISTS conversation_turns_attempt_positive;
ALTER TABLE conversation_turns
    ADD CONSTRAINT conversation_turns_status_check CHECK (status IN ('running', 'failed', 'completed')),
    ADD CONSTRAINT conversation_turns_attempt_positive CHECK (attempt_count > 0),
    ADD CONSTRAINT conversation_turns_completion_check CHECK (
        (status = 'running' AND lease_expires_at IS NOT NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'failed' AND lease_expires_at IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND lease_expires_at IS NULL AND assistant_message_id IS NOT NULL AND completed_at IS NOT NULL)
    );
ALTER TABLE conversation_turns DROP COLUMN lease_owner;
