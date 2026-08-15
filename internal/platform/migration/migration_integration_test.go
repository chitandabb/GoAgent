package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// TestMigrationsAgainstPostgres 使用一次性空数据库验证真实 PostgreSQL 迁移。
// 默认跳过；本地或 CI 设置 MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestMigrationsAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, versionTable := openIsolatedMigrationTestDB(t, dsn)
	var err error
	provider, err := newProvider(db, goose.WithTableName(versionTable))
	if err != nil {
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
		`SELECT EXISTS (
SELECT 1
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relname = 'goose_db_version'
)`,
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
	if err == nil || !strings.Contains(err.Error(), "00035 is irreversible") {
		t.Fatalf("migrate down = %v, want explicit irreversible failure", err)
	}
	if err := CheckCurrent(ctx, db); err != nil {
		t.Fatalf("CheckCurrent() after rejected down = %v, want current schema", err)
	}
}

// TestMigration00035PolicyHardCutAgainstPostgres 在真实 PostgreSQL 上验证
// 00035：NULL Policy 旧行 fail-fast、干净库上得到 Policy NOT NULL + 无 mode +
// 无 request_scope 的最终结构，以及不可逆 Down（明确失败且不恢复旧列）。
// 默认跳过；设置 MESGUARD_TEST_POSTGRES_DSN 后执行。
func TestMigration00035PolicyHardCutAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, versionTable := openIsolatedMigrationTestDB(t, dsn)
	var err error
	provider, err := newProvider(db, goose.WithTableName(versionTable))
	if err != nil {
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
	// SAVEPOINT/行操作固定在同一连接上执行。
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	execContext := func(query string, args ...any) error {
		_, err := conn.ExecContext(ctx, query, args...)
		return err
	}

	// 1. 迁移到 00034 状态，插入 legacy 旧行（mode=legacy + Policy 双 NULL）。
	if _, err := provider.UpTo(ctx, 34); err != nil {
		t.Fatalf("migrate up to 00034: %v", err)
	}
	userID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e90"
	sourceID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e91"
	caseID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e92"
	snapshotID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e93"
	taskID := "0197f0ca-8f83-7a33-9c20-1a2b3c4d5e94"
	fixture := []struct {
		name  string
		query string
		args  []any
	}{
		{"user", `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES ($1, 'policy-hardcut-owner', 'Hard Cut', 'hash', 'analyst')`, []any{userID}},
		{"data source", `
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode)
VALUES ($1, 'policy-hardcut-source', 'Hard Cut Source', 'sqlserver', 'case_source', 'integration', 'read_only')`, []any{sourceID}},
		{"external case", `
INSERT INTO external_cases (id, data_source_id, external_case_key, last_seen_at)
VALUES ($1, $2, 'HC-1', now())`, []any{caseID, sourceID}},
		{"case snapshot", `
INSERT INTO case_snapshots (id, external_case_id, snapshot_no, payload, content_hash, source_read_at)
VALUES ($1, $2, 1, '{}', 'sha256:hc', now())`, []any{snapshotID, caseID}},
	}
	for _, step := range fixture {
		if err := execContext(step.query, step.args...); err != nil {
			t.Fatalf("insert %s: %v", step.name, err)
		}
	}
	if err := execContext(`
INSERT INTO diagnosis_tasks
    (id, created_by, external_case_id, case_snapshot_id, idempotency_key, request_fingerprint, request_text)
VALUES ($1, $2, $3, $4, 'hc-key-1', 'sha256:hc-fp', '检查')`,
		taskID, userID, caseID, snapshotID); err != nil {
		t.Fatalf("insert legacy task: %v", err)
	}

	// 2. 旧行存在时 00035 必须 fail-fast，且不静默删除数据。
	_, migrationErr := provider.UpTo(ctx, 35)
	if migrationErr == nil {
		t.Fatal("00035 applied over legacy NULL-policy tasks")
	}
	if !strings.Contains(migrationErr.Error(), "00035 precondition failed") {
		t.Fatalf("00035 failure = %v, want explicit precondition message", migrationErr)
	}
	var remainingLegacy int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM diagnosis_tasks WHERE id = $1", taskID).Scan(&remainingLegacy); err != nil {
		t.Fatalf("count legacy row after failed migration: %v", err)
	}
	if remainingLegacy != 1 {
		t.Fatalf("legacy row count = %d, want 1 (no silent delete)", remainingLegacy)
	}

	// 3. 清理旧数据后迁移成功，得到最终结构。
	if err := execContext(`DELETE FROM diagnosis_tasks WHERE id = $1`, taskID); err != nil {
		t.Fatalf("clean legacy task: %v", err)
	}
	if err := execContext(`DELETE FROM case_snapshots WHERE id = $1`, snapshotID); err != nil {
		t.Fatalf("clean legacy snapshot: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up to 00035: %v", err)
	}

	columnState := func(column string) (bool, bool) {
		var nullable string
		var defaultVal sql.NullString
		row := conn.QueryRowContext(ctx, `
SELECT is_nullable, column_default FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'diagnosis_tasks' AND column_name = $1`, column)
		if err := row.Scan(&nullable, &defaultVal); err != nil {
			t.Fatalf("read %s column state: %v", column, err)
		}
		return nullable == "YES", defaultVal.Valid
	}
	nullable, _ := columnState("investigation_policy")
	if nullable {
		t.Fatal("investigation_policy must be NOT NULL after 00035")
	}
	nullable, _ = columnState("investigation_policy_schema_version")
	if nullable {
		t.Fatal("investigation_policy_schema_version must be NOT NULL after 00035")
	}
	for _, removed := range []string{
		"investigation_policy_mode", "request_scope", "request_scope_schema_version",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'diagnosis_tasks' AND column_name = $1`, removed).Scan(&count); err != nil {
			t.Fatalf("count %s after 00035: %v", removed, err)
		}
		if count != 0 {
			t.Fatalf("00035 must drop %s", removed)
		}
	}
	// 00033 的 object/positive version 校验必须保留。
	for _, constraint := range []string{
		"diagnosis_tasks_investigation_policy_object_check",
		"diagnosis_tasks_investigation_policy_version_check",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_constraint
WHERE connamespace = current_schema()::regnamespace AND conname = $1`, constraint).Scan(&count); err != nil {
			t.Fatalf("count constraint %s: %v", constraint, err)
		}
		if count != 1 {
			t.Fatalf("00035 must keep constraint %s", constraint)
		}
	}

	// 4. Down 不可逆：必须明确失败，失败后 00035 的新结构保持不变，
	//    旧列不得恢复。
	_, downErr := provider.Down(ctx)
	if downErr == nil {
		t.Fatal("00035 Down succeeded, want explicit irreversible failure")
	}
	if !strings.Contains(downErr.Error(), "00035 is irreversible") {
		t.Fatalf("00035 Down failure = %v, want explicit irreversible message", downErr)
	}
	nullable, _ = columnState("investigation_policy")
	if nullable {
		t.Fatal("investigation_policy must remain NOT NULL after failed 00035 Down")
	}
	nullable, _ = columnState("investigation_policy_schema_version")
	if nullable {
		t.Fatal("investigation_policy_schema_version must remain NOT NULL after failed 00035 Down")
	}
	for _, removed := range []string{
		"investigation_policy_mode", "request_scope", "request_scope_schema_version",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'diagnosis_tasks' AND column_name = $1`, removed).Scan(&count); err != nil {
			t.Fatalf("count %s after failed down: %v", removed, err)
		}
		if count != 0 {
			t.Fatalf("00035 Down must not restore %s", removed)
		}
	}
	for _, constraint := range []string{
		"diagnosis_tasks_investigation_policy_object_check",
		"diagnosis_tasks_investigation_policy_version_check",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_constraint
WHERE connamespace = current_schema()::regnamespace AND conname = $1`, constraint).Scan(&count); err != nil {
			t.Fatalf("count constraint %s after failed down: %v", constraint, err)
		}
		if count != 1 {
			t.Fatalf("00035 Down must not drop constraint %s", constraint)
		}
	}
}

// openIsolatedMigrationTestDB gives every migration test its own schema so the
// irreversible 00035 Down cannot contaminate the next test using the same DSN.
// The generated schema is the first search_path entry; public remains visible
// for database-level extensions such as vector.
func openIsolatedMigrationTestDB(t *testing.T, dsn string) (*sql.DB, string) {
	t.Helper()
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open migration test postgres: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := adminDB.PingContext(ctx); err != nil {
		_ = adminDB.Close()
		t.Fatalf("ping migration test postgres: %v", err)
	}
	schemaName := "mesguard_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create isolated migration schema: %v", err)
	}

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		_, _ = adminDB.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("parse migration test postgres DSN: %v", err)
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["search_path"] = quotedSchema + ",public"
	db := stdlib.OpenDB(*config)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_, _ = adminDB.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("open isolated migration schema: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, dropErr := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); dropErr != nil {
			t.Errorf("drop isolated migration schema: %v", dropErr)
		}
		_ = adminDB.Close()
	})
	return db, schemaName + "." + goose.DefaultTablename
}
