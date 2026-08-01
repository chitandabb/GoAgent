//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	domainrepo "github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestSchemaCatalogRepositoryAgainstPostgres 验证 Catalog 的发布状态、数据源状态和
// queryable 边界最终由真实 PostgreSQL 查询共同执行，而不只依赖应用层过滤。
func TestSchemaCatalogRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	userID := uuid.New()
	activeSourceID := uuid.New()
	disabledSourceID := uuid.New()
	publishedVersionID := uuid.New()
	draftVersionID := uuid.New()
	retiredVersionID := uuid.New()
	disabledSourceVersionID := uuid.New()
	ids := []uuid.UUID{publishedVersionID, draftVersionID, retiredVersionID, disabledSourceVersionID}
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		_ = cleanup.Exec("DELETE FROM schema_catalog_entries WHERE catalog_version_id IN ?", ids).Error
		_ = cleanup.Exec("DELETE FROM schema_catalog_versions WHERE id IN ?", ids).Error
		_ = cleanup.Exec("DELETE FROM data_sources WHERE id IN ?", []uuid.UUID{activeSourceID, disabledSourceID}).Error
		_ = cleanup.Exec("DELETE FROM users WHERE id = ?", userID).Error
	})

	mustExecCatalogTest(t, db, ctx, `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Catalog Test', 'test-hash', 'admin')`, userID, "catalog_"+uuid.NewString()[:8])
	mustExecCatalogTest(t, db, ctx, `
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES
    (?, ?, 'Active Catalog Source', 'sqlserver', 'production', 'integration', 'read_only', 'active'),
    (?, ?, 'Disabled Catalog Source', 'sqlserver', 'production', 'integration', 'read_only', 'disabled')`,
		activeSourceID, "catalog-active-"+uuid.NewString()[:8],
		disabledSourceID, "catalog-disabled-"+uuid.NewString()[:8])
	mustExecCatalogTest(t, db, ctx, `
INSERT INTO schema_catalog_versions
    (id, data_source_id, version, status, scan_status, published_by, published_at)
VALUES
    (?, ?, 1, 'published', 'succeeded', ?, now()),
    (?, ?, 2, 'draft', 'succeeded', NULL, NULL),
	(?, ?, 3, 'retired', 'succeeded', NULL, NULL),
    (?, ?, 1, 'published', 'succeeded', ?, now())`,
		publishedVersionID, activeSourceID, userID,
		draftVersionID, activeSourceID,
		retiredVersionID, activeSourceID,
		disabledSourceVersionID, disabledSourceID, userID)
	mustExecCatalogTest(t, db, ctx, `
INSERT INTO schema_catalog_entries
    (id, catalog_version_id, object_schema, object_name, object_type, column_name, data_type, comment, semantic_aliases, queryable)
VALUES
	(?, ?, 'dbo', 'Tickets', 'TABLE', NULL, NULL, 'queryable object', '[]', true),
    (?, ?, 'dbo', 'Tickets', 'TABLE', 'TicketID', 'nvarchar', '工单编号', '["故障单"]', true),
	(?, ?, 'reporting', 'TicketSummary', 'VIEW', NULL, NULL, 'queryable view', '[]', true),
	(?, ?, 'dbo', 'NormalizeTicket', 'SQL_SCALAR_FUNCTION', NULL, NULL, 'queryable function', '[]', true),
	(?, ?, 'dbo', 'TicketSecrets', 'TABLE', NULL, NULL, 'blocked object', '[]', false),
    (?, ?, 'dbo', 'TicketSecrets', 'TABLE', 'Secret', 'nvarchar', '工单密钥', '[]', false),
	(?, ?, 'dbo', 'DraftTickets', 'TABLE', NULL, NULL, 'draft object', '[]', true),
    (?, ?, 'dbo', 'DraftTickets', 'TABLE', 'TicketID', 'nvarchar', '工单草稿', '[]', true),
	(?, ?, 'dbo', 'RetiredTickets', 'TABLE', NULL, NULL, 'retired object', '[]', true),
	(?, ?, 'dbo', 'DisabledTickets', 'TABLE', NULL, NULL, 'disabled source object', '[]', true),
    (?, ?, 'dbo', 'DisabledTickets', 'TABLE', 'TicketID', 'nvarchar', '工单停用库', '[]', true)`,
		uuid.New(), publishedVersionID,
		uuid.New(), publishedVersionID,
		uuid.New(), publishedVersionID,
		uuid.New(), publishedVersionID,
		uuid.New(), publishedVersionID,
		uuid.New(), publishedVersionID,
		uuid.New(), draftVersionID,
		uuid.New(), draftVersionID,
		uuid.New(), retiredVersionID,
		uuid.New(), disabledSourceVersionID,
		uuid.New(), disabledSourceVersionID)

	repository := NewSchemaCatalogRepository(db)
	entries, err := repository.SearchPublished(ctx, activeSourceID, "工单", 10)
	if err != nil {
		t.Fatalf("SearchPublished(): %v", err)
	}
	if len(entries) != 1 || entries[0].ObjectName != "Tickets" || entries[0].ColumnName != "TicketID" || entries[0].SensitivityLevel != "internal" {
		t.Fatalf("SearchPublished() entries = %#v, want the single published queryable entry", entries)
	}

	escaped, err := repository.SearchPublished(ctx, activeSourceID, "%_", 10)
	if err != nil {
		t.Fatalf("SearchPublished(wildcards): %v", err)
	}
	if len(escaped) != 0 {
		t.Fatalf("SearchPublished(wildcards) returned %d entries, want 0", len(escaped))
	}

	disabled, err := repository.SearchPublished(ctx, disabledSourceID, "工单", 10)
	if err != nil {
		t.Fatalf("SearchPublished(disabled source): %v", err)
	}
	if len(disabled) != 0 {
		t.Fatalf("SearchPublished(disabled source) returned %d entries, want 0", len(disabled))
	}

	authorization, err := repository.AuthorizePublishedObjects(ctx, activeSourceID, []domainrepo.SchemaCatalogObjectRef{
		{ObjectSchema: "dbo", ObjectName: "Tickets"},
		{ObjectSchema: "reporting", ObjectName: "TicketSummary"},
		{ObjectSchema: "dbo", ObjectName: "NormalizeTicket"},
		{ObjectSchema: "DBO", ObjectName: "TICKETS"},
	})
	if err != nil {
		t.Fatalf("AuthorizePublishedObjects(): %v", err)
	}
	if authorization.CatalogVersionID != publishedVersionID || authorization.CatalogVersion != 1 || len(authorization.Objects) != 3 {
		t.Fatalf("AuthorizePublishedObjects() = %#v, want published version and three deduplicated objects", authorization)
	}

	deniedCases := []struct {
		name       string
		sourceID   uuid.UUID
		objectName string
	}{
		{name: "queryable false", sourceID: activeSourceID, objectName: "TicketSecrets"},
		{name: "draft", sourceID: activeSourceID, objectName: "DraftTickets"},
		{name: "retired", sourceID: activeSourceID, objectName: "RetiredTickets"},
		{name: "inactive data source", sourceID: disabledSourceID, objectName: "DisabledTickets"},
		{name: "missing", sourceID: activeSourceID, objectName: "MissingTickets"},
	}
	for _, test := range deniedCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.AuthorizePublishedObjects(ctx, test.sourceID, []domainrepo.SchemaCatalogObjectRef{{
				ObjectSchema: "dbo", ObjectName: test.objectName,
			}})
			if !errors.Is(err, domainrepo.ErrSchemaCatalogAuthorizationDenied) {
				t.Fatalf("AuthorizePublishedObjects() error = %v, want authorization denied", err)
			}
		})
	}

	_, err = repository.AuthorizePublishedObjects(ctx, activeSourceID, []domainrepo.SchemaCatalogObjectRef{
		{ObjectSchema: "dbo", ObjectName: "Tickets"},
		{ObjectSchema: "dbo", ObjectName: "MissingTickets"},
	})
	if !errors.Is(err, domainrepo.ErrSchemaCatalogAuthorizationDenied) {
		t.Fatalf("AuthorizePublishedObjects(partial match) error = %v, want all-or-nothing denial", err)
	}
}

func mustExecCatalogTest(t *testing.T, db *gorm.DB, ctx context.Context, query string, args ...any) {
	t.Helper()
	if err := db.WithContext(ctx).Exec(query, args...).Error; err != nil {
		t.Fatalf("prepare catalog fixture: %v", err)
	}
}
