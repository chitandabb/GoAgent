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

type stubReadonlyQueryExecutor struct {
	dataSourceID uuid.UUID
	query        string
	calls        int
	result       repository.ReadonlyQueryResult
	err          error
}

func (s *stubReadonlyQueryExecutor) Execute(
	_ context.Context, dataSourceID uuid.UUID, query string,
) (repository.ReadonlyQueryResult, error) {
	s.dataSourceID, s.query, s.calls = dataSourceID, query, s.calls+1
	return s.result, s.err
}

func TestExecuteReadonlyQueryToolUsesAuthorizedSingleDataSource(t *testing.T) {
	dataSourceID := uuid.New()
	executor := &stubReadonlyQueryExecutor{result: repository.ReadonlyQueryResult{
		PolicyVersion: ReadonlyQueryPolicyVersionForTest,
		ReturnedRows:  1,
	}}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: dataSourceID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencySQLServer)
	result, err := current.InvokableRun(
		WithTaskScope(context.Background(), scope),
		`{"query":"SELECT * FROM dbo.Tickets"}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if executor.calls != 1 || executor.dataSourceID != dataSourceID || executor.query != "SELECT * FROM dbo.Tickets" {
		t.Fatalf("executor call = %#v", executor)
	}
	if result == "" {
		t.Fatal("readonly query result is empty")
	}
}

func TestExecuteReadonlyQueryToolRejectsMissingDependencyOrAmbiguousSource(t *testing.T) {
	executor := &stubReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	dataSourceID := uuid.New()
	withoutDependency := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: dataSourceID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
	}})
	if _, err = current.InvokableRun(
		WithTaskScope(context.Background(), withoutDependency),
		`{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil {
		t.Fatal("query Tool accepted unavailable SQL Server dependency")
	}
	if executor.calls != 0 {
		t.Fatal("query Tool reached executor without SQL dependency")
	}

	secondID := uuid.New()
	ambiguous := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{
		{ID: dataSourceID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly},
		{ID: secondID, Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyReadOnly},
	}, ToolDependencySQLServer)
	if _, err = current.InvokableRun(
		WithTaskScope(context.Background(), ambiguous),
		`{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil {
		t.Fatal("query Tool accepted ambiguous data source")
	}
	if executor.calls != 0 {
		t.Fatal("query Tool reached executor with ambiguous source")
	}
}

func TestExecuteReadonlyQueryToolPropagatesContextAndSafeExecutorErrors(t *testing.T) {
	executor := &stubReadonlyQueryExecutor{err: errors.New("readonly query unavailable")}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencySQLServer)
	if _, err = current.InvokableRun(
		WithTaskScope(context.Background(), scope), `{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil || !strings.Contains(err.Error(), "readonly query unavailable") {
		t.Fatalf("executor error = %v", err)
	}
}

// The Agent package intentionally does not import platform/sqlserver merely for a
// test fixture; the production executor supplies this same policy-version field.
const ReadonlyQueryPolicyVersionForTest = "tsql-readonly-v1"
