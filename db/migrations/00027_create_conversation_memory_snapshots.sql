-- +goose Up
CREATE TABLE conversation_memory_snapshots (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    snapshot_version BIGINT NOT NULL,
    supersedes_snapshot_id UUID NULL,
    from_seq BIGINT NOT NULL,
    through_seq BIGINT NOT NULL,
    schema_version INTEGER NOT NULL,
    summary_model_profile VARCHAR(128) NOT NULL,
    summary_model_provider VARCHAR(128) NOT NULL,
    summary_model_id VARCHAR(256) NOT NULL,
    prompt_version VARCHAR(128) NOT NULL,
    payload JSONB NOT NULL,
    payload_sha256 CHAR(64) NOT NULL,
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    cached_tokens INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    activated_at TIMESTAMPTZ NULL,
    CONSTRAINT conversation_memory_snapshots_version_unique UNIQUE (conversation_id, snapshot_version),
    CONSTRAINT conversation_memory_snapshots_coverage_check CHECK (
        snapshot_version >= 1 AND from_seq >= 1 AND through_seq >= from_seq
    ),
    CONSTRAINT conversation_memory_snapshots_schema_check CHECK (schema_version = 1),
    CONSTRAINT conversation_memory_snapshots_identity_check CHECK (
        btrim(summary_model_profile) <> '' AND summary_model_profile = btrim(summary_model_profile) AND
        btrim(summary_model_provider) <> '' AND summary_model_provider = btrim(summary_model_provider) AND
        btrim(summary_model_id) <> '' AND summary_model_id = btrim(summary_model_id) AND
        btrim(prompt_version) <> '' AND prompt_version = btrim(prompt_version)
    ),
    CONSTRAINT conversation_memory_snapshots_payload_check CHECK (
        jsonb_typeof(payload) = 'object' AND payload_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT conversation_memory_snapshots_usage_check CHECK (
        prompt_tokens >= 0 AND completion_tokens >= 0 AND total_tokens >= prompt_tokens + completion_tokens AND
        cached_tokens >= 0 AND cached_tokens <= prompt_tokens
    ),
    CONSTRAINT conversation_memory_snapshots_status_check CHECK (
        (status = 'candidate' AND activated_at IS NULL) OR
        (status IN ('active', 'superseded') AND activated_at IS NOT NULL)
    )
);

ALTER TABLE conversation_memory_snapshots
    ADD CONSTRAINT conversation_memory_snapshots_predecessor_fk
    FOREIGN KEY (supersedes_snapshot_id) REFERENCES conversation_memory_snapshots(id)
    ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

CREATE UNIQUE INDEX conversation_memory_snapshots_active_unique
    ON conversation_memory_snapshots (conversation_id)
    WHERE status = 'active';

CREATE INDEX conversation_memory_snapshots_conversation_version_idx
    ON conversation_memory_snapshots (conversation_id, snapshot_version DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_conversation_memory_snapshot_content_update()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.conversation_id IS DISTINCT FROM OLD.conversation_id OR
       NEW.snapshot_version IS DISTINCT FROM OLD.snapshot_version OR
       NEW.supersedes_snapshot_id IS DISTINCT FROM OLD.supersedes_snapshot_id OR
       NEW.from_seq IS DISTINCT FROM OLD.from_seq OR
       NEW.through_seq IS DISTINCT FROM OLD.through_seq OR
       NEW.schema_version IS DISTINCT FROM OLD.schema_version OR
       NEW.summary_model_profile IS DISTINCT FROM OLD.summary_model_profile OR
       NEW.summary_model_provider IS DISTINCT FROM OLD.summary_model_provider OR
       NEW.summary_model_id IS DISTINCT FROM OLD.summary_model_id OR
       NEW.prompt_version IS DISTINCT FROM OLD.prompt_version OR
       NEW.payload IS DISTINCT FROM OLD.payload OR
       NEW.payload_sha256 IS DISTINCT FROM OLD.payload_sha256 OR
       NEW.prompt_tokens IS DISTINCT FROM OLD.prompt_tokens OR
       NEW.completion_tokens IS DISTINCT FROM OLD.completion_tokens OR
       NEW.total_tokens IS DISTINCT FROM OLD.total_tokens OR
       NEW.cached_tokens IS DISTINCT FROM OLD.cached_tokens OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'conversation memory snapshot content is immutable' USING ERRCODE = '23000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER conversation_memory_snapshots_content_immutable
    BEFORE UPDATE ON conversation_memory_snapshots
    FOR EACH ROW EXECUTE FUNCTION reject_conversation_memory_snapshot_content_update();

-- +goose Down
DROP TRIGGER IF EXISTS conversation_memory_snapshots_content_immutable ON conversation_memory_snapshots;
DROP FUNCTION IF EXISTS reject_conversation_memory_snapshot_content_update();
DROP INDEX IF EXISTS conversation_memory_snapshots_conversation_version_idx;
DROP INDEX IF EXISTS conversation_memory_snapshots_active_unique;
DROP TABLE IF EXISTS conversation_memory_snapshots;
