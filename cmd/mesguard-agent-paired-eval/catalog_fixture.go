package main

import (
	"context"
	"errors"
	"fmt"

	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// sqlCatalogEvaluationFixture 将评测需要的 published Catalog 限制在一笔事务内。
// paired run 共用同一事务，退出命令时 rollback，避免把临时 Catalog 写进开发库。
type sqlCatalogEvaluationFixture struct {
	tx *gorm.DB
}

func beginSQLCatalogEvaluationFixture(
	ctx context.Context,
	db *gorm.DB,
	dataSourceID uuid.UUID,
) (*sqlCatalogEvaluationFixture, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	if dataSourceID == uuid.Nil {
		return nil, errors.New("SQL data source id is required")
	}

	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin transaction: %w", tx.Error)
	}
	rollback := func(err error) (*sqlCatalogEvaluationFixture, error) {
		_ = tx.Rollback().Error
		return nil, err
	}

	var source struct {
		ID     uuid.UUID
		Status string
	}
	if err := platformpostgres.ResolveDB(ctx, tx).Raw(
		"SELECT id, status FROM data_sources WHERE id = ?", dataSourceID,
	).Scan(&source).Error; err != nil {
		return rollback(fmt.Errorf("load SQL data source: %w", err))
	}
	if source.ID == uuid.Nil || source.Status != "active" {
		return rollback(errors.New("configured SQL data source is not active"))
	}

	var publisher struct {
		ID uuid.UUID
	}
	if err := platformpostgres.ResolveDB(ctx, tx).Raw(
		"SELECT id FROM users WHERE role = 'admin' ORDER BY created_at LIMIT 1",
	).Scan(&publisher).Error; err != nil {
		return rollback(fmt.Errorf("load Catalog publisher: %w", err))
	}
	if publisher.ID == uuid.Nil {
		return rollback(errors.New("an admin user is required for published Catalog evaluation"))
	}

	versionID := uuid.New()
	if err := platformpostgres.ResolveDB(ctx, tx).Exec(`
INSERT INTO schema_catalog_versions
    (id, data_source_id, version, status, scan_status, scan_attempt_count,
     source_introspected_at, created_by, published_by, published_at)
VALUES (?, ?, 1, 'published', 'succeeded', 1, now(), ?, ?, now())`,
		versionID, dataSourceID, publisher.ID, publisher.ID,
	).Error; err != nil {
		return rollback(fmt.Errorf("insert Catalog version: %w", err))
	}

	entries := []struct {
		objectName string
		objectType string
		columnName *string
		dataType   *string
		comment    string
		aliases    string
	}{
		{
			objectName: "v_MESGuardExternalCases", objectType: "VIEW",
			comment: "MESGuard 外部工单只读视图", aliases: `["工单", "外部工单", "状态"]`,
		},
		{
			objectName: "v_MESGuardExternalCases", objectType: "VIEW",
			columnName: stringPointer("TicketID"), dataType: stringPointer("nvarchar"),
			comment: "外部工单编号", aliases: `["工单号", "TicketID"]`,
		},
		{
			objectName: "v_MESGuardExternalCases", objectType: "VIEW",
			columnName: stringPointer("Status"), dataType: stringPointer("nvarchar"),
			comment: "工单状态", aliases: `["状态", "处理状态"]`,
		},
		{
			objectName: "v_MESGuardExternalCases", objectType: "VIEW",
			columnName: stringPointer("Title"), dataType: stringPointer("nvarchar"),
			comment: "工单标题", aliases: `["标题"]`,
		},
	}
	for _, entry := range entries {
		if err := platformpostgres.ResolveDB(ctx, tx).Exec(`
INSERT INTO schema_catalog_entries
    (id, catalog_version_id, object_schema, object_name, object_type,
     column_name, data_type, comment, semantic_aliases, queryable, sensitivity_level)
VALUES (?, ?, 'dbo', ?, ?, ?, ?, ?, ?::jsonb, true, 'internal')`,
			uuid.New(), versionID, entry.objectName, entry.objectType,
			entry.columnName, entry.dataType, entry.comment, entry.aliases,
		).Error; err != nil {
			return rollback(fmt.Errorf("insert Catalog entry %s.%s: %w", entry.objectName, pointerValue(entry.columnName), err))
		}
	}

	return &sqlCatalogEvaluationFixture{tx: tx}, nil
}

func (f *sqlCatalogEvaluationFixture) DB() *gorm.DB {
	if f == nil {
		return nil
	}
	return f.tx
}

func (f *sqlCatalogEvaluationFixture) Rollback() error {
	if f == nil || f.tx == nil {
		return nil
	}
	err := f.tx.Rollback().Error
	f.tx = nil
	return err
}

func stringPointer(value string) *string {
	return &value
}

func pointerValue(value *string) string {
	if value == nil {
		return "<object>"
	}
	return *value
}
