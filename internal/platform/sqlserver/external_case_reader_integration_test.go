//go:build integration

package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"go.uber.org/zap"
)

func TestExternalCaseReaderAgainstSQLServer(t *testing.T) {
	db := openIntegrationDB(t)
	reader, err := NewExternalCaseReader(db, integrationConfig(), zap.NewNop())
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	result, err := reader.List(context.Background(), externalcase.ListQuery{
		Status: externalcase.StatusOpen, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 2 || len(result.Items) != 2 {
		t.Fatalf("open tickets total=%d items=%d, want 2", result.Total, len(result.Items))
	}

	item, err := reader.GetByKey(context.Background(), "TKT-1003")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if item.Production.SerialNo != "SN-20260727-000881" || len(item.Attachments) != 2 {
		t.Fatalf("mapped item = %#v attachments=%d", item.Production, len(item.Attachments))
	}
	if item.Attributes["reporterDepartment"] != "客户成功部" || item.Attributes["impactScope"] != "单设备单批次" {
		t.Fatalf("mapped attributes = %#v", item.Attributes)
	}

	filtered, err := reader.List(context.Background(), externalcase.ListQuery{
		Keyword: "条码", Priority: externalcase.PriorityHigh,
		Page: 1, PageSize: 20, SortBy: "externalCaseKey", SortOrder: "asc",
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if filtered.Total != 1 || filtered.Items[0].ExternalCaseKey != "TKT-1003" {
		t.Fatalf("filtered result = %#v", filtered)
	}

	caseTypeFiltered, err := reader.List(context.Background(), externalcase.ListQuery{
		CaseType: "performance", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("case type filtered list: %v", err)
	}
	if caseTypeFiltered.Total != 1 || len(caseTypeFiltered.Items) != 1 || caseTypeFiltered.Items[0].ExternalCaseKey != "TKT-1004" {
		t.Fatalf("case type filtered result = %#v", caseTypeFiltered)
	}

	limitedConfig := integrationConfig()
	limitedConfig.MaxTextBytes = 1
	limitedConfig.MaxResultBytes = 1
	limitedReader, err := NewExternalCaseReader(db, limitedConfig, zap.NewNop())
	if err != nil {
		t.Fatalf("new limited reader: %v", err)
	}
	_, err = limitedReader.List(context.Background(), externalcase.ListQuery{Page: 1, PageSize: 20})
	if !errors.Is(err, externalcase.ErrResultLimit) {
		t.Fatalf("limited list error = %v, want ErrResultLimit", err)
	}
}

func TestCaseReaderCannotWriteOrExecuteDDL(t *testing.T) {
	db := openIntegrationDB(t)
	statements := []string{
		"INSERT INTO dbo.Tickets (TicketID, CaseType, Title, Description, Status, Priority, ReportedAt, SourceUpdatedAt, SourceSystem) VALUES ('DENIED', 'x', 'x', 'x', 'New', 'Low', SYSUTCDATETIME(), SYSUTCDATETIME(), 'test')",
		"UPDATE dbo.Tickets SET Title = 'DENIED' WHERE TicketID = 'TKT-1001'",
		"DELETE FROM dbo.Tickets WHERE TicketID = 'TKT-1001'",
		"CREATE TABLE dbo.MESGuardDeniedTest (ID INT)",
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err == nil {
			t.Fatalf("reader unexpectedly executed statement: %s", statement)
		}
	}
}

func TestCaseReaderCannotReadUnpublishedBaseTable(t *testing.T) {
	db := openIntegrationDB(t)
	rows, err := db.QueryContext(context.Background(), "SELECT TOP (1) TicketID FROM dbo.Tickets")
	if err == nil {
		_ = rows.Close()
		t.Fatal("reader unexpectedly queried an unpublished base table")
	}
}

func TestObjectDefinitionReaderAgainstSQLServer(t *testing.T) {
	db := openIntegrationDB(t)
	reader, err := NewObjectDefinitionReader(db, integrationConfig(), zap.NewNop())
	if err != nil {
		t.Fatalf("new object definition reader: %v", err)
	}
	definition, objectType, truncated, err := reader.GetObjectDefinition(
		context.Background(), "dbo", "v_MESGuardExternalCases",
	)
	if err != nil {
		t.Fatalf("get object definition: %v", err)
	}
	if objectType != "VIEW" || truncated || !strings.Contains(definition, "TicketProductionContexts") {
		t.Fatalf("unexpected object definition type=%q truncated=%t definition=%q", objectType, truncated, definition)
	}
}

func TestReadonlyQueryGuardAcceptsExecutableSQLServerQuery(t *testing.T) {
	db := openIntegrationDB(t)
	guard, err := NewReadonlyQueryGuard([]string{"dbo"}, 8192)
	if err != nil {
		t.Fatalf("new readonly query guard: %v", err)
	}
	const query = `
WITH active_cases AS (
    SELECT TicketID, Status
    FROM dbo.v_MESGuardExternalCases
    WHERE Status IN ('New', 'Investigating')
)
SELECT TicketID FROM active_cases
UNION ALL
SELECT TicketID FROM dbo.v_MESGuardExternalCases WHERE Status = 'Resolved'`
	analysis, err := guard.Analyze(query)
	if err != nil {
		t.Fatalf("analyze executable query: %v", err)
	}
	if !analysis.HasCTE || !analysis.HasUnion || len(analysis.Objects) != 1 || analysis.Objects[0].Name != "v_MESGuardExternalCases" {
		t.Fatalf("unexpected query analysis: %#v", analysis)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("execute guarded query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	rowCount := 0
	for rows.Next() {
		var ticketID string
		if err := rows.Scan(&ticketID); err != nil {
			t.Fatalf("scan guarded query: %v", err)
		}
		rowCount++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate guarded query: %v", err)
	}
	if rowCount == 0 {
		t.Fatal("guarded query returned no demo rows")
	}
}

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MESGUARD_TEST_SQLSERVER_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_SQLSERVER_DSN is not set")
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("open sqlserver: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping sqlserver: %v", err)
	}
	return db
}

func integrationConfig() config.SQLServerConfig {
	fields := map[string]string{
		"externalCaseKey": "TicketID", "caseType": "CaseType", "title": "Title",
		"description": "Description", "category": "Category", "module": "Module",
		"status": "Status", "priority": "Priority", "occurredAt": "OccurredAt",
		"reportedAt": "ReportedAt", "sourceUpdatedAt": "SourceUpdatedAt",
		"customerCode": "CustomerCode", "customerName": "CustomerName",
		"productCode": "ProductCode", "productName": "ProductName", "productVersion": "ProductVersion",
		"workOrderNo": "WorkOrderNo", "workpieceNo": "WorkpieceNo", "materialCode": "MaterialCode",
		"batchNo": "BatchNo", "serialNo": "SerialNo", "factoryCode": "FactoryCode",
		"workshopCode": "WorkshopCode", "productionLineCode": "ProductionLineCode",
		"workstationCode": "WorkstationCode", "equipmentCode": "EquipmentCode",
		"sourceSystem": "SourceSystem", "deploymentEnvironment": "DeploymentEnvironment",
		"businessDatabaseAlias": "BusinessDatabaseAlias",
	}
	return config.SQLServerConfig{
		Enabled: true, ID: "8d5c67dc-4c09-4ee5-9e80-4d822303dc35", Code: "erp",
		Name: "ERP", Environment: "integration", Host: "unused", Port: 1433,
		User: "unused", Database: "SUPPORT_DEMO", PasswordEnv: "UNUSED",
		Encrypt: "disable",
		MaxOpen: 5, MaxIdle: 1, QueryTimeoutMillis: 3000, MaxTextBytes: 65536, MaxResultBytes: 524288,
		CaseMapping: config.SQLServerCaseMapping{
			Relation: "dbo.v_MESGuardExternalCases", Fields: fields,
			Attributes:     map[string]string{"reporterDepartment": "ReporterDepartment", "impactScope": "ImpactScope"},
			StatusValues:   map[string]string{"New": "open", "Investigating": "processing", "Resolved": "closed"},
			PriorityValues: map[string]string{"Urgent": "high", "Normal": "medium", "Low": "low"},
		},
		AttachmentMapping: config.SQLServerObjectMapping{
			Relation: "dbo.v_MESGuardExternalCaseAttachments",
			Fields: map[string]string{
				"externalCaseKey": "TicketID", "externalAttachmentKey": "AttachmentID",
				"fileName": "FileName", "mediaType": "MediaType", "sizeBytes": "SizeBytes",
				"objectKey": "ObjectKey", "contentHash": "ContentHash", "sourceUpdatedAt": "SourceUpdatedAt",
			},
		},
		Investigation: config.SQLServerInvestigationConfig{
			AllowedSchemas: []string{"dbo"}, MaxQueryBytes: 8192, MaxRows: 100,
			MaxResultBytes: 262144, MaxConcurrentQueries: 2,
		},
	}
}
