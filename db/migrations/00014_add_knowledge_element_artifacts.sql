-- +goose Up
ALTER TABLE knowledge_document_versions
    ADD COLUMN element_artifact_bucket VARCHAR(32),
    ADD COLUMN element_artifact_object_key VARCHAR(1024),
    ADD COLUMN element_artifact_object_version VARCHAR(512),
    ADD COLUMN element_artifact_etag VARCHAR(512),
    ADD COLUMN element_artifact_size_bytes BIGINT,
    ADD COLUMN element_artifact_sha256 CHAR(64),
    ADD CONSTRAINT knowledge_document_versions_element_artifact_check CHECK (
        (element_artifact_bucket IS NULL AND element_artifact_object_key IS NULL
            AND element_artifact_object_version IS NULL AND element_artifact_etag IS NULL
            AND element_artifact_size_bytes IS NULL AND element_artifact_sha256 IS NULL)
        OR (element_artifact_bucket = 'knowledge-artifact'
            AND btrim(element_artifact_object_key) <> ''
            AND (element_artifact_object_version IS NULL OR btrim(element_artifact_object_version) <> '')
            AND btrim(element_artifact_etag) <> ''
            AND element_artifact_size_bytes >= 0
            AND element_artifact_sha256 ~ '^[0-9a-f]{64}$')
    );

CREATE UNIQUE INDEX knowledge_document_versions_element_artifact_idx
    ON knowledge_document_versions (
        element_artifact_bucket,
        element_artifact_object_key,
        COALESCE(element_artifact_object_version, '')
    )
    WHERE element_artifact_object_key IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS knowledge_document_versions_element_artifact_idx;
ALTER TABLE knowledge_document_versions
    DROP CONSTRAINT knowledge_document_versions_element_artifact_check,
    DROP COLUMN element_artifact_sha256,
    DROP COLUMN element_artifact_size_bytes,
    DROP COLUMN element_artifact_etag,
    DROP COLUMN element_artifact_object_version,
    DROP COLUMN element_artifact_object_key,
    DROP COLUMN element_artifact_bucket;
