-- +goose Up
-- Unified Agent Runtime v2 硬切清理：不再兼容 migration 前 Diagnosis 任务。
--
-- 前置条件 fail-fast：任何 investigation_policy 或 schema version 为 NULL
-- 的旧任务（包括 00034 的 legacy mode 行）都会让本迁移明确失败，本迁移
-- 绝不静默删除或清空业务数据。操作者需先清理旧 diagnosis 数据或重建开发
-- 数据库。这只是迁移前置条件，不是运行时兼容路径。
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM diagnosis_tasks
        WHERE investigation_policy IS NULL
           OR investigation_policy_schema_version IS NULL
    ) THEN
        RAISE EXCEPTION 'migration 00035 precondition failed: legacy diagnosis tasks with NULL investigation policy exist; clean old diagnosis data or rebuild the development database';
    END IF;
END
$$;
-- +goose StatementEnd

-- 删除 legacy/frozen mode 概念及其全部 CHECK，Policy 两列强制 NOT NULL；
-- 00033 的 pair check 被 NOT NULL 取代，object/positive version 校验保留。
-- request_scope 授权体系随本次硬切一并删除（列级 CHECK 随列自动删除）。
ALTER TABLE diagnosis_tasks
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_legacy_pair_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_frozen_pair_check,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_check,
    DROP COLUMN IF EXISTS investigation_policy_mode,
    DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_pair_check,
    ALTER COLUMN investigation_policy SET NOT NULL,
    ALTER COLUMN investigation_policy_schema_version SET NOT NULL,
    DROP COLUMN IF EXISTS request_scope,
    DROP COLUMN IF EXISTS request_scope_schema_version;

-- +goose Down
-- 本迁移不可逆：request_scope 与 investigation_policy_mode 所代表的旧授权语义
-- 已随硬切永久删除。Down 不恢复旧列（恢复只会重新引入已被删除的授权路径），
-- 而是明确失败；需要回退时只能从 00033 之前的历史备份重建数据库。
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'migration 00035 is irreversible: legacy request_scope and investigation_policy_mode authorization semantics are permanently deleted; restore from a backup taken before 00033 instead of running Down';
END
$$;
-- +goose StatementEnd
