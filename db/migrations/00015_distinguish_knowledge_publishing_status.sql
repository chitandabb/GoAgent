-- +goose Up
ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_status_check,
    DROP CONSTRAINT knowledge_document_versions_current_ready_check,
    DROP CONSTRAINT knowledge_document_versions_terminal_fields_check,
    ADD CONSTRAINT knowledge_document_versions_status_check CHECK (
        status IN (
            'processing', 'queued', 'quarantined', 'scanning', 'parsing',
            'chunking', 'indexing', 'publishing', 'partial_ready', 'ready',
            'failed', 'cancelled', 'retired'
        )
    ),
    ADD CONSTRAINT knowledge_document_versions_current_ready_check CHECK (
        NOT is_current OR status IN ('ready', 'partial_ready')
    ),
    ADD CONSTRAINT knowledge_document_versions_terminal_fields_check CHECK (
        (status IN ('ready', 'partial_ready', 'retired', 'cancelled')
            AND completed_at IS NOT NULL AND error_code IS NULL AND error_message IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error_code IS NOT NULL)
        OR (status IN (
            'processing', 'queued', 'quarantined', 'scanning', 'parsing',
            'chunking', 'indexing', 'publishing'
        ) AND completed_at IS NULL AND error_code IS NULL AND error_message IS NULL)
    );

-- +goose Down
UPDATE knowledge_document_versions
SET status = 'indexing'
WHERE status = 'publishing';

ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_status_check,
    DROP CONSTRAINT knowledge_document_versions_current_ready_check,
    DROP CONSTRAINT knowledge_document_versions_terminal_fields_check,
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
        OR (status IN (
            'processing', 'queued', 'quarantined', 'scanning', 'parsing',
            'chunking', 'indexing'
        ) AND completed_at IS NULL AND error_code IS NULL AND error_message IS NULL)
    );
