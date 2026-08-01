package sqlserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type stubReadonlyRows struct {
	columns []string
	values  [][]any
	current int
	next    int
	err     error
	closed  bool
}

func (r *stubReadonlyRows) Columns() ([]string, error) {
	return append([]string(nil), r.columns...), nil
}

func (r *stubReadonlyRows) Next() bool {
	if r.next >= len(r.values) {
		return false
	}
	r.current = r.next
	r.next++
	return true
}

func (r *stubReadonlyRows) Scan(dest ...any) error {
	if r.current >= len(r.values) {
		return errors.New("scan called without current row")
	}
	values := r.values[r.current]
	if len(dest) != len(values) {
		return errors.New("scan destination count mismatch")
	}
	for index := range values {
		cell, ok := dest[index].(*any)
		if !ok {
			return errors.New("scan destination is not *any")
		}
		*cell = values[index]
	}
	return nil
}

func (r *stubReadonlyRows) Err() error { return r.err }

func (r *stubReadonlyRows) Close() error {
	r.closed = true
	return nil
}

type stubReadonlyQueryer struct {
	mu      sync.Mutex
	query   string
	calls   int
	rows    readonlyQueryRows
	err     error
	started chan struct{}
	block   <-chan struct{}
}

func (q *stubReadonlyQueryer) QueryContext(ctx context.Context, query string, _ ...any) (readonlyQueryRows, error) {
	q.mu.Lock()
	q.query = query
	q.calls++
	q.mu.Unlock()
	if q.started != nil {
		select {
		case <-q.started:
		default:
			close(q.started)
		}
	}
	if q.block != nil {
		select {
		case <-q.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return q.rows, q.err
}

type stubReadonlyAuthorizer struct {
	mu         sync.Mutex
	dataSource uuid.UUID
	objects    []repository.SchemaCatalogObjectRef
	result     repository.SchemaCatalogAuthorization
	err        error
	calls      int
}

func (a *stubReadonlyAuthorizer) AuthorizePublishedObjects(
	_ context.Context, dataSourceID uuid.UUID, objects []repository.SchemaCatalogObjectRef,
) (repository.SchemaCatalogAuthorization, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dataSource = dataSourceID
	a.objects = append([]repository.SchemaCatalogObjectRef(nil), objects...)
	a.calls++
	return a.result, a.err
}

func TestReadonlyQueryExecutorAuthorizesPublishedObjectsBeforeQuery(t *testing.T) {
	dataSourceID := uuid.New()
	authorizer := &stubReadonlyAuthorizer{result: repository.SchemaCatalogAuthorization{
		CatalogVersionID: uuid.New(), CatalogVersion: 7,
		Objects: []repository.SchemaCatalogObjectRef{{ObjectSchema: "dbo", ObjectName: "Tickets"}},
	}}
	queryer := &stubReadonlyQueryer{rows: &stubReadonlyRows{
		columns: []string{"TicketID", "Count"}, values: [][]any{{"TKT-1", int64(2)}},
	}}
	executor := newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 10, 4096, 1)

	result, err := executor.Execute(context.Background(), dataSourceID,
		"SELECT TicketID, Count FROM dbo.Tickets WHERE Status = 'Open'")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.PolicyVersion != ReadonlyQueryPolicyVersion || result.CatalogVersion != 7 ||
		result.ReturnedRows != 1 || result.Truncated || len(result.Rows) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Rows[0][0] != "TKT-1" || result.Rows[0][1] != int64(2) {
		t.Fatalf("unexpected row: %#v", result.Rows[0])
	}
	if queryer.query == "" || queryer.calls != 1 {
		t.Fatalf("query calls = %d query=%q", queryer.calls, queryer.query)
	}
	if authorizer.dataSource != dataSourceID || len(authorizer.objects) != 1 || authorizer.objects[0].ObjectName != "Tickets" {
		t.Fatalf("catalog authorization = %#v", authorizer)
	}
}

func TestReadonlyQueryExecutorRejectsBeforeCatalogOrDatabase(t *testing.T) {
	dataSourceID := uuid.New()
	authorizer := &stubReadonlyAuthorizer{}
	queryer := &stubReadonlyQueryer{}
	executor := newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 10, 4096, 1)

	_, err := executor.Execute(context.Background(), dataSourceID, "DELETE FROM dbo.Tickets")
	if !errors.Is(err, ErrReadonlyQueryRejected) {
		t.Fatalf("Execute error = %v, want query rejection", err)
	}
	if authorizer.calls != 0 || queryer.calls != 0 {
		t.Fatalf("unsafe query reached downstream: authorizer=%d queryer=%d", authorizer.calls, queryer.calls)
	}
}

func TestReadonlyQueryExecutorRejectsCatalogDenialAndSanitizesDatabaseError(t *testing.T) {
	dataSourceID := uuid.New()
	queryer := &stubReadonlyQueryer{err: errors.New("sqlserver.internal:1433 secret connection details")}
	authorizer := &stubReadonlyAuthorizer{err: repository.ErrSchemaCatalogAuthorizationDenied}
	executor := newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 10, 4096, 1)
	_, err := executor.Execute(context.Background(), dataSourceID, "SELECT * FROM dbo.Tickets")
	if !errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied) || queryer.calls != 0 {
		t.Fatalf("catalog denial = %v query calls=%d", err, queryer.calls)
	}

	authorizer.err = nil
	authorizer.result = repository.SchemaCatalogAuthorization{
		CatalogVersionID: uuid.New(), CatalogVersion: 1,
		Objects: []repository.SchemaCatalogObjectRef{{ObjectSchema: "dbo", ObjectName: "Tickets"}},
	}
	_, err = executor.Execute(context.Background(), dataSourceID, "SELECT * FROM dbo.Tickets")
	if !errors.Is(err, repository.ErrReadonlyQueryUnavailable) || strings.Contains(err.Error(), "sqlserver.internal") {
		t.Fatalf("database error = %v", err)
	}
}

func TestReadonlyQueryExecutorAppliesRowAndByteLimits(t *testing.T) {
	dataSourceID := uuid.New()
	authorizer := &stubReadonlyAuthorizer{result: repository.SchemaCatalogAuthorization{
		CatalogVersionID: uuid.New(), CatalogVersion: 1,
		Objects: []repository.SchemaCatalogObjectRef{{ObjectSchema: "dbo", ObjectName: "Tickets"}},
	}}
	queryer := &stubReadonlyQueryer{rows: &stubReadonlyRows{
		columns: []string{"TicketID"}, values: [][]any{{"TKT-1"}, {"TKT-2"}},
	}}
	executor := newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 1, 4096, 1)
	result, err := executor.Execute(context.Background(), dataSourceID, "SELECT TicketID FROM dbo.Tickets")
	if err != nil {
		t.Fatalf("row-limited Execute: %v", err)
	}
	if !result.Truncated || result.TruncationReason != "max_rows" || result.ReturnedRows != 1 {
		t.Fatalf("row limit result = %#v", result)
	}

	queryer.rows = &stubReadonlyRows{columns: []string{"TicketID"}, values: [][]any{{strings.Repeat("x", 128)}}}
	executor = newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 10, 64, 1)
	result, err = executor.Execute(context.Background(), dataSourceID, "SELECT TicketID FROM dbo.Tickets")
	if err != nil {
		t.Fatalf("byte-limited Execute: %v", err)
	}
	if !result.Truncated || result.TruncationReason != "max_result_bytes" || result.ReturnedRows != 0 {
		t.Fatalf("byte limit result = %#v", result)
	}
}

func TestReadonlyQueryExecutorLimitsConcurrentQueries(t *testing.T) {
	dataSourceID := uuid.New()
	authorizer := &stubReadonlyAuthorizer{result: repository.SchemaCatalogAuthorization{
		CatalogVersionID: uuid.New(), CatalogVersion: 1,
		Objects: []repository.SchemaCatalogObjectRef{{ObjectSchema: "dbo", ObjectName: "Tickets"}},
	}}
	block := make(chan struct{})
	queryer := &stubReadonlyQueryer{
		rows:    &stubReadonlyRows{columns: []string{"TicketID"}, values: [][]any{{"TKT-1"}}},
		started: make(chan struct{}), block: block,
	}
	executor := newReadonlyExecutorForTest(t, queryer, dataSourceID, authorizer, 10, 4096, 1)
	firstDone := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), dataSourceID, "SELECT TicketID FROM dbo.Tickets")
		firstDone <- err
	}()
	select {
	case <-queryer.started:
	case <-time.After(time.Second):
		t.Fatal("first readonly query did not start")
	}
	secondCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, secondErr := executor.Execute(secondCtx, dataSourceID, "SELECT TicketID FROM dbo.Tickets")
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("second query error = %v, want deadline", secondErr)
	}
	close(block)
	if err := <-firstDone; err != nil {
		t.Fatalf("first query error = %v", err)
	}
	if queryer.calls != 1 {
		t.Fatalf("query calls = %d, want only one admitted query", queryer.calls)
	}
}

func newReadonlyExecutorForTest(
	t *testing.T,
	queryer readonlyQueryer,
	dataSourceID uuid.UUID,
	authorizer repository.SchemaCatalogAuthorizer,
	maxRows, maxResultBytes, maxConcurrent int,
) *ReadonlyQueryExecutor {
	t.Helper()
	guard, err := NewReadonlyQueryGuard([]string{"dbo"}, 4096)
	if err != nil {
		t.Fatalf("NewReadonlyQueryGuard: %v", err)
	}
	executor, err := newReadonlyQueryExecutor(
		queryer, dataSourceID, guard, authorizer, time.Second,
		maxRows, maxResultBytes, maxConcurrent, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("newReadonlyQueryExecutor: %v", err)
	}
	return executor
}
