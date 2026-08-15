-- +goose Up
-- InvestigationPolicy v2：新任务在创建事务内冻结授权事实。旧任务不回填，
-- 两列为 NULL 是迁移前的明确兼容状态（Worker 仅对 NULL Policy 做 legacy 派生）。
ALTER TABLE diagnosis_tasks
    ADD COLUMN investigation_policy JSONB,
    ADD COLUMN investigation_policy_schema_version INTEGER,
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_object_check
        CHECK (investigation_policy IS NULL OR jsonb_typeof(investigation_policy) = 'object'),
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_version_check
        CHECK (investigation_policy_schema_version IS NULL OR investigation_policy_schema_version > 0),
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_pair_check CHECK (
        (investigation_policy IS NULL AND investigation_policy_schema_version IS NULL)
        OR
        (investigation_policy IS NOT NULL AND investigation_policy_schema_version IS NOT NULL)
    );

-- +goose Down
ALTER TABLE diagnosis_tasks
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_pair_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_version_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_object_check,
    DROP COLUMN IF EXISTS investigation_policy_schema_version,
    DROP COLUMN IF EXISTS investigation_policy;
