package migrations

import (
	"strings"
	"testing"
)

// TestMigration00035FinalizesDiagnosisPolicyHardCut 静态校验 00035 的 SQL
// 形状：fail-fast 前置条件、mode 列/约束删除、Policy NOT NULL、request_scope
// 删除与不可逆 Down（明确 RAISE EXCEPTION，不恢复旧列）。真实语法与行为由
// MESGUARD_TEST_POSTGRES_DSN 门控的集成测试验证。
func TestMigration00035FinalizesDiagnosisPolicyHardCut(t *testing.T) {
	raw, err := Files.ReadFile("00035_finalize_diagnosis_policy_hard_cut.sql")
	if err != nil {
		t.Fatalf("read 00035 migration: %v", err)
	}
	sql := string(raw)
	upStart := strings.Index(sql, "-- +goose Up")
	downStart := strings.Index(sql, "-- +goose Down")
	if upStart < 0 || downStart < 0 || downStart <= upStart {
		t.Fatal("00035 must contain ordered -- +goose Up / -- +goose Down markers")
	}
	up := sql[upStart:downStart]
	for _, want := range []string{
		"RAISE EXCEPTION 'migration 00035 precondition failed",
		"-- +goose StatementBegin",
		"-- +goose StatementEnd",
		"DROP COLUMN IF EXISTS investigation_policy_mode",
		"DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_check",
		"ALTER COLUMN investigation_policy SET NOT NULL",
		"ALTER COLUMN investigation_policy_schema_version SET NOT NULL",
		"DROP COLUMN IF EXISTS request_scope",
		"DROP COLUMN IF EXISTS request_scope_schema_version",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("00035 Up missing %q", want)
		}
	}
	// 硬切不得静默删除业务数据：Up 中不允许出现 DELETE/TRUNCATE 语句。
	for _, forbidden := range []string{"DELETE ", "TRUNCATE"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("00035 Up must not contain %q", forbidden)
		}
	}
	// object/positive version 校验（00033）必须保留。
	for _, want := range []string{
		"diagnosis_tasks_investigation_policy_object_check",
		"diagnosis_tasks_investigation_policy_version_check",
	} {
		if strings.Contains(up, "DROP CONSTRAINT IF EXISTS "+want) {
			t.Errorf("00035 Up must keep %q", want)
		}
	}

	// Down 必须明确失败且不可逆：只允许 RAISE EXCEPTION，不允许任何恢复旧列
	// 的 DDL/UPDATE。
	down := sql[downStart:]
	if !strings.Contains(down, "RAISE EXCEPTION 'migration 00035 is irreversible") {
		t.Errorf("00035 Down must raise an explicit irreversible exception")
	}
	for _, marker := range []string{"-- +goose StatementBegin", "-- +goose StatementEnd"} {
		if !strings.Contains(down, marker) {
			t.Errorf("00035 Down missing %q around its PL/pgSQL block", marker)
		}
	}
	for _, forbidden := range []string{
		"ADD COLUMN request_scope",
		"ADD COLUMN request_scope_schema_version",
		"ADD COLUMN investigation_policy_mode",
		"SET investigation_policy_mode",
		"ADD CONSTRAINT diagnosis_tasks_investigation_policy_mode_check",
		"ADD CONSTRAINT diagnosis_tasks_investigation_policy_pair_check",
		"ALTER COLUMN investigation_policy DROP NOT NULL",
		"ALTER TABLE diagnosis_tasks",
	} {
		if strings.Contains(down, forbidden) {
			t.Errorf("00035 Down must not restore legacy structure (%q)", forbidden)
		}
	}
}

// TestMigration00034RemainsHistoricalChainUnchanged 锁定 00034 作为已应用的
// 历史迁移不被改写：mode 概念只允许由 00035 删除。
func TestMigration00034RemainsHistoricalChainUnchanged(t *testing.T) {
	raw, err := Files.ReadFile("00034_add_diagnosis_investigation_policy_mode.sql")
	if err != nil {
		t.Fatalf("read 00034 migration: %v", err)
	}
	if !strings.Contains(string(raw), "ADD COLUMN investigation_policy_mode VARCHAR(16) NOT NULL DEFAULT 'legacy'") {
		t.Fatal("00034 historical chain was modified")
	}
}

// TestMigration00033RemainsUnchangedWithoutPolicyMode 锁定 00033 不被改写：
// mode 语义只属于 00034/00035。
func TestMigration00033RemainsUnchangedWithoutPolicyMode(t *testing.T) {
	raw, err := Files.ReadFile("00033_add_diagnosis_investigation_policy.sql")
	if err != nil {
		t.Fatalf("read 00033 migration: %v", err)
	}
	if strings.Contains(string(raw), "investigation_policy_mode") {
		t.Fatal("00033 must not introduce investigation_policy_mode")
	}
}
