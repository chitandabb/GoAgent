-- +goose Up
-- InvestigationPolicy v2：migration 00033 只用"双 NULL"表达迁移前旧任务，
-- 无法区分"旧任务"与"新任务漏写 Policy"。00034 增加 policy mode 显式标识：
--   legacy = 迁移前旧任务（Policy 两列必须同时为 NULL，Worker 做 legacy 派生）；
--   frozen = 新任务（Policy 两列必须同时非 NULL，Worker 直接使用冻结 Policy）。
-- 迁移执行时既有行全部默认 legacy；新代码创建任务必须显式写 frozen，
-- mode 与两列组合违反约束时由数据库拒绝。
ALTER TABLE diagnosis_tasks
    ADD COLUMN investigation_policy_mode VARCHAR(16) NOT NULL DEFAULT 'legacy',
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_mode_check
        CHECK (investigation_policy_mode IN ('legacy', 'frozen')),
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_mode_frozen_pair_check
        CHECK (investigation_policy_mode <> 'frozen' OR (
            investigation_policy IS NOT NULL
            AND investigation_policy_schema_version IS NOT NULL
        )),
    ADD CONSTRAINT diagnosis_tasks_investigation_policy_mode_legacy_pair_check
        CHECK (investigation_policy_mode <> 'legacy' OR (
            investigation_policy IS NULL
            AND investigation_policy_schema_version IS NULL
        ));

-- +goose Down
ALTER TABLE diagnosis_tasks
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_legacy_pair_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_frozen_pair_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_check,
    DROP COLUMN IF EXISTS investigation_policy_mode;
