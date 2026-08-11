-- +goose Up
ALTER TABLE conversation_prompt_manifests
    ADD COLUMN summary_snapshot_id UUID NULL
        REFERENCES conversation_memory_snapshots(id) ON DELETE SET NULL,
    ADD COLUMN hard_compaction_triggered BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX conversation_prompt_manifests_summary_snapshot_idx
    ON conversation_prompt_manifests (summary_snapshot_id)
    WHERE summary_snapshot_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS conversation_prompt_manifests_summary_snapshot_idx;
ALTER TABLE conversation_prompt_manifests
    DROP COLUMN IF EXISTS hard_compaction_triggered,
    DROP COLUMN IF EXISTS summary_snapshot_id;
