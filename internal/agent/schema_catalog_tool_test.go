package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
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
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: dataSourceID, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencySQLServer)
	result, err := current.InvokableRun(
		WithTaskScope(context.Background(), scope),
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
	unauthorizedScope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: allowedID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencySQLServer)
	if _, err = current.InvokableRun(
		WithTaskScope(context.Background(), unauthorizedScope),
		`{"dataSourceId":"`+uuid.NewString()+`","keyword":"ticket"}`,
	); err == nil {
		t.Fatal("catalog Tool accepted unauthorized data source")
	}

	secondID := uuid.New()
	ambiguousScope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{
		{ID: allowedID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly},
		{ID: secondID, Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyReadOnly},
	}, ToolDependencySQLServer)
	if _, err = current.InvokableRun(
		WithTaskScope(context.Background(), ambiguousScope), `{"keyword":"ticket"}`,
	); err == nil {
		t.Fatal("catalog Tool accepted an ambiguous data source")
	}
}

func TestSearchSchemaCatalogToolDoesNotLeakRepositoryError(t *testing.T) {
	current, err := NewSearchSchemaCatalogTool(&stubSchemaCatalogSearcher{
		err: errors.New("postgres://catalog.internal:5432/mesguard connection refused"),
	})
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencySQLServer)
	_, err = current.InvokableRun(
		WithTaskScope(context.Background(), scope), `{"keyword":"ticket"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "schema catalog is unavailable") || strings.Contains(err.Error(), "catalog.internal") {
		t.Fatalf("unexpected repository error: %v", err)
	}
}
