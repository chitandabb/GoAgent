-- +goose Up
CREATE TABLE conversation_turns (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    idempotency_key UUID NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL,
    user_message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE RESTRICT,
    assistant_message_id UUID REFERENCES conversation_messages(id) ON DELETE RESTRICT,
    attempt_count INTEGER NOT NULL DEFAULT 1,
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT conversation_turns_status_check CHECK (status IN ('running', 'failed', 'completed')),
    CONSTRAINT conversation_turns_fingerprint_check CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT conversation_turns_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT conversation_turns_completion_check CHECK (
        (status = 'running' AND lease_expires_at IS NOT NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'failed' AND lease_expires_at IS NULL AND assistant_message_id IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND lease_expires_at IS NULL AND assistant_message_id IS NOT NULL AND completed_at IS NOT NULL)
    ),
    CONSTRAINT conversation_turns_conversation_key_unique UNIQUE (conversation_id, idempotency_key),
    CONSTRAINT conversation_turns_user_message_unique UNIQUE (user_message_id),
    CONSTRAINT conversation_turns_assistant_message_unique UNIQUE (assistant_message_id)
);

CREATE UNIQUE INDEX conversation_turns_one_running_per_conversation_idx
    ON conversation_turns (conversation_id)
    WHERE status = 'running';

CREATE INDEX conversation_turns_user_updated_idx
    ON conversation_turns (user_id, updated_at DESC, id DESC);

CREATE INDEX conversation_turns_expired_lease_idx
    ON conversation_turns (lease_expires_at)
    WHERE status = 'running';

-- +goose Down
DROP TABLE IF EXISTS conversation_turns;
