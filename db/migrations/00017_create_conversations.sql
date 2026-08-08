-- +goose Up
CREATE TABLE conversations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title VARCHAR(200) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    last_message_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT conversations_status_check CHECK (status IN ('active', 'archived'))
);

CREATE INDEX conversations_user_updated_idx
    ON conversations (user_id, updated_at DESC, id DESC);

CREATE TABLE conversation_messages (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    role VARCHAR(16) NOT NULL,
    content TEXT NOT NULL,
    content_schema_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT conversation_messages_seq_positive CHECK (seq > 0),
    CONSTRAINT conversation_messages_role_check CHECK (role IN ('user', 'assistant', 'tool', 'system')),
    CONSTRAINT conversation_messages_content_not_blank CHECK (btrim(content) <> ''),
    CONSTRAINT conversation_messages_schema_positive CHECK (content_schema_version > 0),
    CONSTRAINT conversation_messages_conversation_seq_unique UNIQUE (conversation_id, seq)
);

CREATE INDEX conversation_messages_conversation_created_idx
    ON conversation_messages (conversation_id, created_at, seq);

CREATE TABLE conversation_case_references (
    message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    external_case_id UUID NOT NULL REFERENCES external_cases(id) ON DELETE RESTRICT,
    reference_kind VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, external_case_id),
    CONSTRAINT conversation_case_reference_kind_check CHECK (reference_kind IN ('selected', 'mentioned'))
);

CREATE INDEX conversation_case_references_case_idx
    ON conversation_case_references (external_case_id, created_at DESC);

CREATE TABLE conversation_task_references (
    message_id UUID NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    task_id UUID NOT NULL REFERENCES diagnosis_tasks(id) ON DELETE RESTRICT,
    reference_kind VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, task_id),
    CONSTRAINT conversation_task_reference_kind_check CHECK (reference_kind IN ('created', 'referenced'))
);

CREATE INDEX conversation_task_references_task_idx
    ON conversation_task_references (task_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS conversation_task_references;
DROP TABLE IF EXISTS conversation_case_references;
DROP TABLE IF EXISTS conversation_messages;
DROP TABLE IF EXISTS conversations;
