package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

type stubSchemaCatalogSearcher struct {
	entries    []repository.SchemaCatalogEntry
	err        error
	dataSource uuid.UUID
	keyword    string
	limit      int
}

func (s *stubSchemaCatalogSearcher) SearchPublished(
	_ context.Context, dataSourceID uuid.UUID, keyword string, limit int,
) ([]repository.SchemaCatalogEntry, error) {
	s.dataSource, s.keyword, s.limit = dataSourceID, keyword, limit
	return s.entries, s.err
}

func TestSearchSchemaCatalogToolUsesSingleAuthorizedDataSource(t *testing.T) {
	dataSourceID := uuid.New()
	searcher := &stubSchemaCatalogSearcher{entries: []repository.SchemaCatalogEntry{{
		CatalogVersion: 3, ObjectSchema: "dbo", ObjectName: "Tickets", ObjectType: "TABLE",
		ColumnName: "Status", Comment: "工单状态", SemanticAliases: []byte(`["状态"]`),
		SensitivityLevel: "internal",
	}}}
	current, err := NewSearchSchemaCatalogTool(searcher)
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	// 授权完全来自 RunAccess.Grants：唯一数据源在 Grant 内，省略 dataSourceId。
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID}))
	result, err := current.InvokableRun(
		ctx,
		`{"keyword":"状态","limit":99}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(result, "Tickets") || !strings.Contains(result, "状态") || !strings.Contains(result, "semanticAliases") {
		t.Fatalf("unexpected catalog result: %s", result)
	}
	if searcher.dataSource != dataSourceID || searcher.keyword != "状态" || searcher.limit != 20 {
		t.Fatalf("search request = %+v", searcher)
	}
}

func TestSearchSchemaCatalogToolRejectsUnauthorizedOrAmbiguousSource(t *testing.T) {
	current, err := NewSearchSchemaCatalogTool(&stubSchemaCatalogSearcher{})
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	allowedID := uuid.New()
	// 1. Grant 之外的数据源 ID：必须拒绝。
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{allowedID}))
	if _, err = current.InvokableRun(
		ctx,
		`{"dataSourceId":"`+uuid.NewString()+`","keyword":"ticket"}`,
	); err == nil {
		t.Fatal("catalog Tool accepted unauthorized data source")
	}

	// 2. 两个数据源在 Grant 但未指定 ID：歧义必须拒绝。
	secondID := uuid.New()
	ctx = agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{allowedID, secondID}))
	if _, err = current.InvokableRun(
		ctx, `{"keyword":"ticket"}`,
	); err == nil {
		t.Fatal("catalog Tool accepted an ambiguous data source")
	}

	// 3. 没有任何数据源 Grant：fail-closed。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationSQLAccess(t, nil))
	if _, err = current.InvokableRun(
		ctx, `{"keyword":"ticket"}`,
	); err == nil {
		t.Fatal("catalog Tool accepted a run with zero granted data sources")
	}
}

func TestSearchSchemaCatalogToolDoesNotLeakRepositoryError(t *testing.T) {
	current, err := NewSearchSchemaCatalogTool(&stubSchemaCatalogSearcher{
		err: errors.New("postgres://catalog.internal:5432/mesguard connection refused"),
	})
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{uuid.New()}))
	_, err = current.InvokableRun(
		ctx, `{"keyword":"ticket"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "schema catalog is unavailable") || strings.Contains(err.Error(), "catalog.internal") {
		t.Fatalf("unexpected repository error: %v", err)
	}
}
