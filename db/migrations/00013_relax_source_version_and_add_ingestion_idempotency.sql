-- +goose Up
ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_source_object_check,
    ADD CONSTRAINT knowledge_document_versions_source_object_check CHECK (
        (source_bucket IS NULL AND source_object_key IS NULL AND source_object_version IS NULL
            AND source_etag IS NULL AND source_original_name IS NULL AND source_uploaded_at IS NULL
            AND pipeline_version IS NULL)
        OR (source_bucket = 'knowledge-source'
            AND btrim(source_object_key) <> ''
            AND (source_object_version IS NULL OR btrim(source_object_version) <> '')
            AND btrim(source_etag) <> '' AND btrim(source_original_name) <> ''
            AND source_uploaded_at IS NOT NULL AND btrim(pipeline_version) <> '')
    );

DROP INDEX knowledge_document_versions_source_object_idx;
CREATE UNIQUE INDEX knowledge_document_versions_source_object_idx
    ON knowledge_document_versions (
        source_bucket, source_object_key, COALESCE(source_object_version, '')
    )
    WHERE source_object_key IS NOT NULL;

ALTER TABLE knowledge_ingestion_tasks
    ADD COLUMN idempotency_key VARCHAR(128),
    ADD COLUMN request_fingerprint CHAR(64),
    ADD CONSTRAINT knowledge_ingestion_tasks_idempotency_key_check CHECK (
        idempotency_key IS NULL OR btrim(idempotency_key) <> ''
    ),
    ADD CONSTRAINT knowledge_ingestion_tasks_request_fingerprint_check CHECK (
        request_fingerprint IS NULL OR request_fingerprint ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT knowledge_ingestion_tasks_idempotency_pair_check CHECK (
        (idempotency_key IS NULL AND request_fingerprint IS NULL)
        OR (idempotency_key IS NOT NULL AND request_fingerprint IS NOT NULL)
    );

CREATE UNIQUE INDEX knowledge_ingestion_tasks_creator_idempotency_idx
    ON knowledge_ingestion_tasks (created_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS knowledge_ingestion_tasks_creator_idempotency_idx;
ALTER TABLE knowledge_ingestion_tasks
    DROP CONSTRAINT knowledge_ingestion_tasks_idempotency_pair_check,
    DROP CONSTRAINT knowledge_ingestion_tasks_request_fingerprint_check,
    DROP CONSTRAINT knowledge_ingestion_tasks_idempotency_key_check,
    DROP COLUMN request_fingerprint,
    DROP COLUMN idempotency_key;

DROP INDEX knowledge_document_versions_source_object_idx;
CREATE UNIQUE INDEX knowledge_document_versions_source_object_idx
    ON knowledge_document_versions (source_bucket, source_object_key, source_object_version)
    WHERE source_object_key IS NOT NULL;

ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_source_object_check,
    ADD CONSTRAINT knowledge_document_versions_source_object_check CHECK (
        (source_bucket IS NULL AND source_object_key IS NULL AND source_object_version IS NULL
            AND source_etag IS NULL AND source_original_name IS NULL AND source_uploaded_at IS NULL
            AND pipeline_version IS NULL)
        OR (source_bucket = 'knowledge-source'
            AND btrim(source_object_key) <> '' AND btrim(source_object_version) <> ''
            AND btrim(source_etag) <> '' AND btrim(source_original_name) <> ''
            AND source_uploaded_at IS NOT NULL AND btrim(pipeline_version) <> '')
    );
