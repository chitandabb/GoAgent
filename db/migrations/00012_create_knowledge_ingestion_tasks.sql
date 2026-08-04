-- +goose Up
ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_status_check,
    DROP CONSTRAINT knowledge_document_versions_current_ready_check,
    DROP CONSTRAINT knowledge_document_versions_terminal_fields_check;

ALTER TABLE knowledge_document_versions
    ALTER COLUMN parser_version DROP NOT NULL,
    ADD COLUMN pipeline_version VARCHAR(128),
    ADD COLUMN source_bucket VARCHAR(32),
    ADD COLUMN source_object_key VARCHAR(1024),
    ADD COLUMN source_object_version VARCHAR(512),
    ADD COLUMN source_etag VARCHAR(512),
    ADD COLUMN source_original_name VARCHAR(512),
    ADD COLUMN source_uploaded_at TIMESTAMPTZ,
    ADD CONSTRAINT knowledge_document_versions_status_check CHECK (
        status IN (
            'processing', 'queued', 'quarantined', 'scanning', 'parsing',
            'chunking', 'indexing', 'partial_ready', 'ready', 'failed',
            'cancelled', 'retired'
        )
    ),
    ADD CONSTRAINT knowledge_document_versions_current_ready_check CHECK (
        NOT is_current OR status IN ('ready', 'partial_ready')
    ),
    ADD CONSTRAINT knowledge_document_versions_terminal_fields_check CHECK (
        (status IN ('ready', 'partial_ready', 'retired', 'cancelled')
            AND completed_at IS NOT NULL AND error_code IS NULL AND error_message IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error_code IS NOT NULL)
        OR (status IN ('processing', 'queued', 'quarantined', 'scanning', 'parsing', 'chunking', 'indexing')
            AND completed_at IS NULL AND error_code IS NULL AND error_message IS NULL)
    ),
    ADD CONSTRAINT knowledge_document_versions_source_object_check CHECK (
        (source_bucket IS NULL AND source_object_key IS NULL AND source_object_version IS NULL
            AND source_etag IS NULL AND source_original_name IS NULL AND source_uploaded_at IS NULL
            AND pipeline_version IS NULL)
        OR (source_bucket = 'knowledge-source'
            AND btrim(source_object_key) <> '' AND btrim(source_object_version) <> ''
            AND btrim(source_etag) <> '' AND btrim(source_original_name) <> ''
            AND source_uploaded_at IS NOT NULL AND btrim(pipeline_version) <> '')
    ),
    ADD CONSTRAINT knowledge_document_versions_parser_status_check CHECK (
        status NOT IN ('ready', 'partial_ready', 'retired')
        OR (parser_version IS NOT NULL AND btrim(parser_version) <> '')
    );

CREATE UNIQUE INDEX knowledge_document_versions_source_object_idx
    ON knowledge_document_versions (source_bucket, source_object_key, source_object_version)
    WHERE source_object_key IS NOT NULL;

CREATE TABLE knowledge_ingestion_tasks (
    id UUID PRIMARY KEY,
    document_version_id UUID NOT NULL UNIQUE REFERENCES knowledge_document_versions(id) ON DELETE RESTRICT,
    status VARCHAR(24) NOT NULL,
    stage VARCHAR(24) NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    claim_owner VARCHAR(128),
    claimed_at TIMESTAMPTZ,
    lease_until TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    checkpoint JSONB NOT NULL DEFAULT '{}'::jsonb,
    progress_percent SMALLINT NOT NULL DEFAULT 0,
    cancel_requested_at TIMESTAMPTZ,
    last_error_code VARCHAR(128),
    last_error_message TEXT,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_ingestion_tasks_status_check CHECK (
        status IN (
            'pending', 'running', 'retry_wait', 'cancel_requested',
            'succeeded', 'partial_succeeded', 'failed', 'cancelled'
        )
    ),
    CONSTRAINT knowledge_ingestion_tasks_stage_check CHECK (
        stage IN ('uploaded', 'scanning', 'parsing', 'chunking', 'indexing', 'publishing', 'completed')
    ),
    CONSTRAINT knowledge_ingestion_tasks_attempt_check CHECK (
        attempt_count >= 0 AND max_attempts BETWEEN 1 AND 10 AND attempt_count <= max_attempts
    ),
    CONSTRAINT knowledge_ingestion_tasks_progress_check CHECK (progress_percent BETWEEN 0 AND 100),
    CONSTRAINT knowledge_ingestion_tasks_checkpoint_object_check CHECK (jsonb_typeof(checkpoint) = 'object'),
    CONSTRAINT knowledge_ingestion_tasks_claim_check CHECK (
        (claim_owner IS NULL AND claimed_at IS NULL AND lease_until IS NULL)
        OR (btrim(claim_owner) <> '' AND claimed_at IS NOT NULL AND lease_until IS NOT NULL AND lease_until > claimed_at)
    ),
    CONSTRAINT knowledge_ingestion_tasks_terminal_check CHECK (
        (status IN ('succeeded', 'partial_succeeded', 'failed', 'cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('succeeded', 'partial_succeeded', 'failed', 'cancelled') AND completed_at IS NULL)
    ),
    CONSTRAINT knowledge_ingestion_tasks_failure_check CHECK (
        (status = 'failed' AND last_error_code IS NOT NULL AND btrim(last_error_code) <> '')
        OR status <> 'failed'
    )
);

CREATE INDEX knowledge_ingestion_tasks_claim_idx
    ON knowledge_ingestion_tasks (available_at, lease_until, created_at)
    WHERE status IN ('pending', 'running', 'retry_wait', 'cancel_requested');
CREATE INDEX knowledge_ingestion_tasks_creator_idx
    ON knowledge_ingestion_tasks (created_by, created_at DESC);

CREATE TABLE knowledge_ingestion_events (
    task_id UUID NOT NULL REFERENCES knowledge_ingestion_tasks(id) ON DELETE RESTRICT,
    seq BIGINT NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_schema_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, seq),
    CONSTRAINT knowledge_ingestion_events_seq_positive CHECK (seq > 0),
    CONSTRAINT knowledge_ingestion_events_type_not_blank CHECK (btrim(event_type) <> ''),
    CONSTRAINT knowledge_ingestion_events_schema_version_positive CHECK (payload_schema_version > 0),
    CONSTRAINT knowledge_ingestion_events_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX knowledge_ingestion_events_task_created_idx
    ON knowledge_ingestion_events (task_id, created_at, seq);

-- +goose Down
DROP TABLE IF EXISTS knowledge_ingestion_events;
DROP TABLE IF EXISTS knowledge_ingestion_tasks;
DROP INDEX IF EXISTS knowledge_document_versions_source_object_idx;

ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_status_check,
    DROP CONSTRAINT knowledge_document_versions_current_ready_check,
    DROP CONSTRAINT knowledge_document_versions_terminal_fields_check,
    DROP CONSTRAINT knowledge_document_versions_source_object_check,
    DROP CONSTRAINT knowledge_document_versions_parser_status_check,
    DROP COLUMN source_bucket,
    DROP COLUMN source_object_key,
    DROP COLUMN source_object_version,
    DROP COLUMN source_etag,
    DROP COLUMN source_original_name,
    DROP COLUMN source_uploaded_at,
    DROP COLUMN pipeline_version,
    ADD CONSTRAINT knowledge_document_versions_status_check CHECK (
        status IN ('processing', 'ready', 'failed', 'retired')
    ),
    ADD CONSTRAINT knowledge_document_versions_current_ready_check CHECK (NOT is_current OR status = 'ready'),
    ADD CONSTRAINT knowledge_document_versions_terminal_fields_check CHECK (
        (status IN ('ready', 'retired') AND completed_at IS NOT NULL AND error_code IS NULL AND error_message IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error_code IS NOT NULL)
        OR (status = 'processing' AND completed_at IS NULL AND error_code IS NULL AND error_message IS NULL)
    );

UPDATE knowledge_document_versions
SET parser_version = 'legacy-rollback'
WHERE parser_version IS NULL;

ALTER TABLE knowledge_document_versions
    ALTER COLUMN parser_version SET NOT NULL;
