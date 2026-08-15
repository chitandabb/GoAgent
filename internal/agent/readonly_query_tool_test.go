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
	// 授权完全来自 RunAccess.Grants：唯一数据源在 Grant 内，省略 dataSourceId。
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID}))
	result, err := current.InvokableRun(
		ctx,
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

func TestExecuteReadonlyQueryToolRejectsMissingGrantOrAmbiguousSource(t *testing.T) {
	executor := &stubReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	dataSourceID := uuid.New()
	// 1. 没有任何数据源 Grant：fail-closed，executor 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationSQLAccess(t, nil))
	if _, err = current.InvokableRun(
		ctx,
		`{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil {
		t.Fatal("query Tool accepted a run with zero granted data sources")
	}
	if executor.calls != 0 {
		t.Fatal("query Tool reached executor without a granted data source")
	}

	// 2. 两个数据源在 Grant 但未指定 ID：歧义必须拒绝。
	secondID := uuid.New()
	ctx = agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID, secondID}))
	if _, err = current.InvokableRun(
		ctx,
		`{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil {
		t.Fatal("query Tool accepted an ambiguous data source")
	}
	if executor.calls != 0 {
		t.Fatal("query Tool reached executor with ambiguous source")
	}

	// 3. 指定了 Grant 之外的 dataSourceId：必须拒绝。
	ctx = agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID}))
	if _, err = current.InvokableRun(
		ctx,
		`{"dataSourceId":"`+secondID.String()+`","query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil {
		t.Fatal("query Tool accepted a dataSourceId outside the Grant")
	}
	if executor.calls != 0 {
		t.Fatal("query Tool reached executor with an un-granted data source")
	}
}

func TestExecuteReadonlyQueryToolPropagatesContextAndSafeExecutorErrors(t *testing.T) {
	executor := &stubReadonlyQueryExecutor{err: errors.New("readonly query unavailable")}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{uuid.New()}))
	if _, err = current.InvokableRun(
		ctx, `{"query":"SELECT * FROM dbo.Tickets"}`,
	); err == nil || !strings.Contains(err.Error(), "readonly query unavailable") {
		t.Fatalf("executor error = %v", err)
	}
}

// The Agent package intentionally does not import platform/sqlserver merely for a
// test fixture; the production executor supplies this same policy-version field.
const ReadonlyQueryPolicyVersionForTest = "tsql-readonly-v1"
