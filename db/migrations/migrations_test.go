package migrations

import (
	"strings"
	"testing"
)

// TestMigration00034AddsPolicyModeWithStrictPairChecks 静态校验 00034 的
// SQL 形状：列定义、三个约束名、Up/Down 的删除顺序。真实语法与约束行为由
// MESGUARD_TEST_POSTGRES_DSN 门控的集成测试验证。
func TestMigration00034AddsPolicyModeWithStrictPairChecks(t *testing.T) {
	raw, err := Files.ReadFile("00034_add_diagnosis_investigation_policy_mode.sql")
	if err != nil {
		t.Fatalf("read 00034 migration: %v", err)
	}
	sql := string(raw)
	upStart := strings.Index(sql, "-- +goose Up")
	downStart := strings.Index(sql, "-- +goose Down")
	if upStart < 0 || downStart < 0 || downStart <= upStart {
		t.Fatal("00034 must contain ordered -- +goose Up / -- +goose Down markers")
	}
	up := sql[upStart:downStart]
	for _, want := range []string{
		"ADD COLUMN investigation_policy_mode VARCHAR(16) NOT NULL DEFAULT 'legacy'",
		"CONSTRAINT diagnosis_tasks_investigation_policy_mode_check",
		"CONSTRAINT diagnosis_tasks_investigation_policy_mode_frozen_pair_check",
		"CONSTRAINT diagnosis_tasks_investigation_policy_mode_legacy_pair_check",
		"'legacy', 'frozen'",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("00034 Up missing %q", want)
		}
	}

	down := sql[downStart:]
	// Down 必须先删依赖 mode 的约束，再删字段本身。
	firstDropConstraint := strings.Index(down, "DROP CONSTRAINT")
	dropColumn := strings.Index(down, "DROP COLUMN")
	if firstDropConstraint < 0 || dropColumn < 0 || firstDropConstraint >= dropColumn {
		t.Fatal("00034 Down must drop the policy-mode constraints before the column")
	}
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_legacy_pair_check",
		"DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_frozen_pair_check",
		"DROP CONSTRAINT IF EXISTS diagnosis_tasks_investigation_policy_mode_check",
		"DROP COLUMN IF EXISTS investigation_policy_mode",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("00034 Down missing %q", want)
		}
	}
}

// TestMigration00033RemainsUnchangedWithoutPolicyMode 锁定 00033 不被本次
// 修正改写：mode 语义只属于 00034。
func TestMigration00033RemainsUnchangedWithoutPolicyMode(t *testing.T) {
	raw, err := Files.ReadFile("00033_add_diagnosis_investigation_policy.sql")
	if err != nil {
		t.Fatalf("read 00033 migration: %v", err)
	}
	if strings.Contains(string(raw), "investigation_policy_mode") {
		t.Fatal("00033 must not introduce investigation_policy_mode; mode belongs to 00034")
	}
}
