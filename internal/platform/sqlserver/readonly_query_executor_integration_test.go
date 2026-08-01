//go:build integration

package sqlserver

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestReadonlyQueryExecutorAgainstPublishedPostgresCatalogAndSQLServer 串起真实的
// PostgreSQL 已发布 Catalog 授权和 SQL Server 只读查询，避免两个单测分别通过后
// 把跨数据库契约误认为已经联调。
func TestReadonlyQueryExecutorAgainstPublishedPostgresCatalogAndSQLServer(t *testing.T) {
	postgresDSN := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if postgresDSN == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	sqlDB := openIntegrationDB(t)
	postgresDB, err := gorm.Open(gormpostgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	postgresSQLDB, err := postgresDB.DB()
	if err != nil {
		t.Fatalf("get postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = postgresSQLDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := postgresSQLDB.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	userID := uuid.New()
	dataSourceID := uuid.New()
	catalogVersionID := uuid.New()
	entryID := uuid.New()
	t.Cleanup(func() {
		cleanup := postgresDB.WithContext(context.Background())
		_ = cleanup.Exec("DELETE FROM schema_catalog_entries WHERE catalog_version_id = ?", catalogVersionID).Error
		_ = cleanup.Exec("DELETE FROM schema_catalog_versions WHERE id = ?", catalogVersionID).Error
		_ = cleanup.Exec("DELETE FROM data_sources WHERE id = ?", dataSourceID).Error
		_ = cleanup.Exec("DELETE FROM users WHERE id = ?", userID).Error
	})

	mustExecReadonlyCatalog(t, postgresDB, ctx, `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Readonly Query Integration', 'integration-hash', 'admin')`,
		userID, "readonly_query_"+uuid.NewString()[:8])
	mustExecReadonlyCatalog(t, postgresDB, ctx, `
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'SQL Server Integration Source', 'sqlserver', 'production', 'integration', 'read_only', 'active')`,
		dataSourceID, "readonly-query-"+uuid.NewString()[:8])
	mustExecReadonlyCatalog(t, postgresDB, ctx, `
INSERT INTO schema_catalog_versions
    (id, data_source_id, version, status, scan_status, published_by, published_at)
VALUES (?, ?, 1, 'published', 'succeeded', ?, now())`,
		catalogVersionID, dataSourceID, userID)
	mustExecReadonlyCatalog(t, postgresDB, ctx, `
INSERT INTO schema_catalog_entries
    (id, catalog_version_id, object_schema, object_name, object_type, comment, semantic_aliases, queryable)
VALUES (?, ?, 'dbo', 'v_MESGuardExternalCases', 'VIEW', 'published integration view', '[]', true)`,
		entryID, catalogVersionID)

	cfg := integrationConfig()
	cfg.ID = dataSourceID.String()
	catalog := platformpostgres.NewSchemaCatalogRepository(postgresDB)
	executor, err := NewReadonlyQueryExecutor(sqlDB, cfg, catalog, zap.NewNop())
	if err != nil {
		t.Fatalf("NewReadonlyQueryExecutor: %v", err)
	}

	result, err := executor.Execute(ctx, dataSourceID,
		"SELECT TicketID, Status FROM dbo.v_MESGuardExternalCases")
	if err != nil {
		t.Fatalf("Execute(real catalog + sqlserver): %v", err)
	}
	if result.CatalogVersionID != catalogVersionID || result.CatalogVersion != 1 ||
		result.ReturnedRows != 4 || len(result.Rows) != 4 {
		t.Fatalf("unexpected cross-database result: %+v", result)
	}
	if len(result.Columns) != 2 || result.Columns[0] != "TicketID" || result.Columns[1] != "Status" {
		t.Fatalf("unexpected result columns: %+v", result.Columns)
	}

	if _, err := executor.Execute(ctx, dataSourceID, "SELECT TicketID FROM dbo.Tickets"); !errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied) {
		t.Fatalf("uncatalogued base table error = %v, want catalog denial", err)
	}
}

func mustExecReadonlyCatalog(t *testing.T, db *gorm.DB, ctx context.Context, query string, args ...any) {
	t.Helper()
	if err := db.WithContext(ctx).Exec(query, args...).Error; err != nil {
		t.Fatalf("prepare readonly query catalog: %v", err)
	}
}
