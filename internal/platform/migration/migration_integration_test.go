package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
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
