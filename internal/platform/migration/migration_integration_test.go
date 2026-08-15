package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMigrationsAgainstPostgres 使用一次性空数据库验证真实 PostgreSQL 迁移。
// 默认跳过；本地或 CI 设置 MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestMigrationsAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	provider, err := NewProvider(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create provider: %v", err)
	}
	defer func() { _ = provider.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sources := provider.ListSources()
	wantTarget := sources[len(sources)-1].Version
	if err := CheckCurrent(ctx, db); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("CheckCurrent() before up = %v, want ErrSchemaNotCurrent", err)
	}
	var versionTableCreated bool
	if err := db.QueryRowContext(
		ctx,
		"SELECT to_regclass('goose_db_version') IS NOT NULL",
	).Scan(&versionTableCreated); err != nil {
		t.Fatalf("check version table: %v", err)
	}
	if versionTableCreated {
		t.Fatal("CheckCurrent() created goose_db_version; startup checks must be read-only")
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		t.Fatalf("get initial versions: %v", err)
	}
	if current != 0 {
		t.Fatalf("test database must be empty: current version = %d", current)
	}
	if target != wantTarget {
		t.Fatalf("target version = %d, want %d", target, wantTarget)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if len(results) != len(sources) {
		t.Fatalf("applied migrations = %d, want %d", len(results), len(sources))
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = provider.DownTo(cleanupCtx, 0)
	}()

	if err := CheckCurrent(ctx, db); err != nil {
		t.Fatalf("CheckCurrent() after up = %v", err)
	}
	repeated, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("repeat migrate up: %v", err)
	}
	if len(repeated) != 0 {
		t.Fatalf("repeat up applied %d migrations, want 0", len(repeated))
	}

	userID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e6f"
	_, err = db.ExecContext(ctx, `
		INSERT INTO users (id, username, display_name, password_hash, role)
		VALUES ($1, 'analyst01', '分析员', 'argon2id-hash', 'analyst')
	`, userID)
	if err != nil {
		t.Fatalf("insert valid user: %v", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO users (id, username, display_name, password_hash, role)
		VALUES ('0197f0ca-8f83-7a33-9c20-1a2b3c4d5e70', 'invalid-role', '测试', 'hash', 'owner')
	`)
	if err == nil {
		t.Fatal("invalid user role was accepted")
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO sessions (
			id, user_id, token_hash, csrf_token_hash, idle_expires_at, absolute_expires_at
		) VALUES (
			'0197f0ca-8f83-7a33-9c20-1a2b3c4d5e71', $1, '\x01', '\x02',
			now() + interval '12 hours', now() + interval '2 hours'
		)
	`, userID)
	if err == nil {
		t.Fatal("session with idle expiry after absolute expiry was accepted")
	}

	statuses, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("migration status: %v", err)
	}
	for _, status := range statuses {
		if status.State != "applied" {
			t.Fatalf("migration %d state = %s, want applied", status.Source.Version, status.State)
		}
	}

	_, err = provider.Down(ctx)
	if err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := CheckCurrent(ctx, db); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("CheckCurrent() after down = %v, want ErrSchemaNotCurrent", err)
	}
}

// TestMigration00034PolicyModeConstraintsAgainstPostgres 在真实 PostgreSQL
// 上验证 00034：列定义与 legacy 默认值、三个 CHECK 约束的命名与拒绝行为、
// frozen 合法组合，以及 Down 删除约束与字段。默认跳过；设置
// MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestMigration00034PolicyModeConstraintsAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	provider, err := NewProvider(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create provider: %v", err)
	}
	defer func() { _ = provider.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	current, _, err := provider.GetVersions(ctx)
	if err != nil {
		t.Fatalf("get initial versions: %v", err)
	}
	if current != 0 {
		t.Fatalf("test database must be empty: current version = %d", current)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = provider.DownTo(cleanupCtx, 0)
	}()

	var columnDefault sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT column_default FROM information_schema.columns
WHERE table_name = 'diagnosis_tasks' AND column_name = 'investigation_policy_mode'`).Scan(&columnDefault); err != nil {
		t.Fatalf("read policy mode column default: %v", err)
	}
	if !columnDefault.Valid || !strings.Contains(columnDefault.String, "'legacy'") {
		t.Fatalf("investigation_policy_mode default = %q, want legacy default", columnDefault.String)
	}

	// SAVEPOINT 必须固定在同一连接上执行。
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	userID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e80"
	sourceID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e81"
	caseID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e82"
	snapshotID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e83"
	taskID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e84"
	execContext := func(query string, args ...any) error {
		_, err := conn.ExecContext(ctx, query, args...)
		return err
	}
	fixture := []struct {
		name  string
		query string
	}{
		{"user", `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES ($1, 'policy-mode-owner', 'Policy Mode', 'hash', 'analyst')`},
		{"data source", `
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode)
VALUES ($1, 'policy-mode-source', 'Policy Mode Source', 'sqlserver', 'case_source', 'integration', 'read_only')`},
		{"external case", `
INSERT INTO external_cases (id, data_source_id, external_case_key, last_seen_at)
VALUES ($1, $2, 'PM-1', now())`},
		{"case snapshot", `
INSERT INTO case_snapshots (id, external_case_id, snapshot_no, payload, content_hash, source_read_at)
VALUES ($1, $2, 1, '{}', 'sha256:pm', now())`},
	}
	args := [][]any{{userID}, {sourceID}, {caseID, sourceID}, {snapshotID, caseID}}
	for index, step := range fixture {
		if err := execContext(step.query, args[index]...); err != nil {
			t.Fatalf("insert %s: %v", step.name, err)
		}
	}

	// 迁移执行时既有任务默认 legacy：省略 mode 的 INSERT 使用 DEFAULT。
	if err := execContext(`
INSERT INTO diagnosis_tasks
    (id, created_by, external_case_id, case_snapshot_id, idempotency_key, request_fingerprint, request_text)
VALUES ($1, $2, $3, $4, 'pm-key-1', 'sha256:pm-fp', '检查')`,
		taskID, userID, caseID, snapshotID); err != nil {
		t.Fatalf("insert legacy-default task: %v", err)
	}
	var storedMode string
	if err := conn.QueryRowContext(ctx,
		"SELECT investigation_policy_mode FROM diagnosis_tasks WHERE id = $1", taskID).Scan(&storedMode); err != nil {
		t.Fatalf("read stored mode: %v", err)
	}
	if storedMode != "legacy" {
		t.Fatalf("default policy mode = %q, want legacy", storedMode)
	}

	// 合法 frozen 组合可写入。
	if err := execContext(`
UPDATE diagnosis_tasks SET investigation_policy_mode = 'frozen',
    investigation_policy = '{"schemaVersion":1,"permissions":["case.read"],"grants":{}}'::jsonb,
    investigation_policy_schema_version = 1 WHERE id = $1`, taskID); err != nil {
		t.Fatalf("valid frozen update: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		"SELECT investigation_policy_mode FROM diagnosis_tasks WHERE id = $1", taskID).Scan(&storedMode); err != nil {
		t.Fatalf("re-read stored mode: %v", err)
	}
	if storedMode != "frozen" {
		t.Fatalf("stored mode after frozen update = %q", storedMode)
	}

	// 违例矩阵：每条用 SAVEPOINT 隔离并核对约束名。
	violations := []struct {
		name       string
		constraint string
		query      string
	}{
		{
			name:       "illegal mode value",
			constraint: "diagnosis_tasks_investigation_policy_mode_check",
			query:      `UPDATE diagnosis_tasks SET investigation_policy_mode = 'Frozen' WHERE id = $1`,
		},
		{
			name:       "legacy with policy columns",
			constraint: "diagnosis_tasks_investigation_policy_mode_legacy_pair_check",
			query: `UPDATE diagnosis_tasks SET investigation_policy_mode = 'legacy',
    investigation_policy = '{}'::jsonb, investigation_policy_schema_version = 1 WHERE id = $1`,
		},
		{
			name:       "frozen with NULL policy columns",
			constraint: "diagnosis_tasks_investigation_policy_mode_frozen_pair_check",
			query: `UPDATE diagnosis_tasks SET investigation_policy_mode = 'frozen',
    investigation_policy = NULL, investigation_policy_schema_version = NULL WHERE id = $1`,
		},
	}
	for _, violation := range violations {
		if err := execContext("SAVEPOINT policy_mode_violation"); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		err := execContext(violation.query, taskID)
		if err == nil {
			t.Fatalf("violation %q was accepted", violation.name)
		}
		if !strings.Contains(err.Error(), violation.constraint) {
			t.Fatalf("violation %q error = %v, want constraint %q",
				violation.name, err, violation.constraint)
		}
		if err := execContext("ROLLBACK TO SAVEPOINT policy_mode_violation"); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}

	// Down 删除 00034 的约束与字段，且 00033 的两列仍然存在。
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("migrate down 00034: %v", err)
	}
	var remaining int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_name = 'diagnosis_tasks' AND column_name = 'investigation_policy_mode'`).Scan(&remaining); err != nil {
		t.Fatalf("count policy mode column after down: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("investigation_policy_mode still present after down")
	}
	for _, column := range []string{"investigation_policy", "investigation_policy_schema_version"} {
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_name = 'diagnosis_tasks' AND column_name = $1`, column).Scan(&remaining); err != nil {
			t.Fatalf("count %s after down: %v", column, err)
		}
		if remaining != 1 {
			t.Fatalf("00034 down must not remove 00033 column %s", column)
		}
	}
}
