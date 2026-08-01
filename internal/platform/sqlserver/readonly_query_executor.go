package sqlserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const maxReadonlyQueryColumns = 256

// readonlyQueryRows 把 database/sql 的行迭代器缩小为可测试的只读边界。
type readonlyQueryRows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type readonlyQueryer interface {
	QueryContext(context.Context, string, ...any) (readonlyQueryRows, error)
}

type sqlDBReadonlyQueryer struct{ db *sql.DB }

func (q sqlDBReadonlyQueryer) QueryContext(
	ctx context.Context, query string, args ...any,
) (readonlyQueryRows, error) {
	return q.db.QueryContext(ctx, query, args...)
}

// ReadonlyQueryExecutor 在 SQL Server 连接池前再加一层应用侧安全闸门：
// QueryGuard -> 已发布 Catalog 精确授权 -> 查询超时/并发限制 -> 行和字节限制。
type ReadonlyQueryExecutor struct {
	queryer            readonlyQueryer
	guard              *ReadonlyQueryGuard
	authorizer         repository.SchemaCatalogAuthorizer
	expectedDataSource uuid.UUID
	queryTimeout       time.Duration
	maxRows            int
	maxResultBytes     int
	concurrency        chan struct{}
	log                *zap.Logger
}

var _ repository.ReadonlyQueryExecutor = (*ReadonlyQueryExecutor)(nil)

func NewReadonlyQueryExecutor(
	db *sql.DB,
	cfg config.SQLServerConfig,
	authorizer repository.SchemaCatalogAuthorizer,
	log *zap.Logger,
) (*ReadonlyQueryExecutor, error) {
	if db == nil || authorizer == nil || log == nil {
		return nil, errors.New("readonly query executor dependencies are nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dataSourceID, err := uuid.Parse(cfg.ID)
	if err != nil {
		return nil, errors.New("readonly query executor data source id is invalid")
	}
	guard, err := NewReadonlyQueryGuard(
		cfg.Investigation.AllowedSchemas,
		cfg.Investigation.MaxQueryBytes,
	)
	if err != nil {
		return nil, err
	}
	return newReadonlyQueryExecutor(
		sqlDBReadonlyQueryer{db: db}, dataSourceID, guard, authorizer,
		time.Duration(cfg.QueryTimeoutMillis)*time.Millisecond,
		cfg.Investigation.MaxRows, cfg.Investigation.MaxResultBytes,
		cfg.Investigation.MaxConcurrentQueries, log,
	)
}

func newReadonlyQueryExecutor(
	queryer readonlyQueryer,
	expectedDataSourceID uuid.UUID,
	guard *ReadonlyQueryGuard,
	authorizer repository.SchemaCatalogAuthorizer,
	queryTimeout time.Duration,
	maxRows, maxResultBytes, maxConcurrentQueries int,
	log *zap.Logger,
) (*ReadonlyQueryExecutor, error) {
	if queryer == nil || expectedDataSourceID == uuid.Nil || guard == nil || authorizer == nil || log == nil {
		return nil, errors.New("readonly query executor dependencies are invalid")
	}
	if queryTimeout <= 0 || maxRows < 1 || maxResultBytes < 1 || maxConcurrentQueries < 1 {
		return nil, errors.New("readonly query executor limits are invalid")
	}
	return &ReadonlyQueryExecutor{
		queryer: queryer, guard: guard, authorizer: authorizer,
		expectedDataSource: expectedDataSourceID, queryTimeout: queryTimeout,
		maxRows: maxRows, maxResultBytes: maxResultBytes,
		concurrency: make(chan struct{}, maxConcurrentQueries), log: log,
	}, nil
}

func (e *ReadonlyQueryExecutor) Execute(
	ctx context.Context,
	dataSourceID uuid.UUID,
	query string,
) (result repository.ReadonlyQueryResult, err error) {
	if e == nil || e.queryer == nil || e.guard == nil || e.authorizer == nil {
		return repository.ReadonlyQueryResult{}, errors.New("readonly query executor is unavailable")
	}
	if dataSourceID == uuid.Nil || dataSourceID != e.expectedDataSource {
		return repository.ReadonlyQueryResult{}, errors.New("data source is not configured for readonly query")
	}
	if err := ctx.Err(); err != nil {
		return repository.ReadonlyQueryResult{}, err
	}
	analysis, err := e.guard.Analyze(query)
	if err != nil {
		return repository.ReadonlyQueryResult{}, err
	}

	queryCtx, cancel := context.WithTimeout(ctx, e.queryTimeout)
	defer cancel()
	objects := make([]repository.SchemaCatalogObjectRef, 0, len(analysis.Objects))
	for _, object := range analysis.Objects {
		objects = append(objects, repository.SchemaCatalogObjectRef{
			ObjectSchema: object.Schema, ObjectName: object.Name,
		})
	}
	authorization, err := e.authorizer.AuthorizePublishedObjects(queryCtx, dataSourceID, objects)
	if err != nil {
		if queryCtx.Err() != nil {
			return repository.ReadonlyQueryResult{}, queryCtx.Err()
		}
		if errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied) {
			return repository.ReadonlyQueryResult{}, err
		}
		e.logFailure(ctx, query, "catalog_authorization", err)
		return repository.ReadonlyQueryResult{}, repository.ErrReadonlyQueryUnavailable
	}
	if !sameCatalogObjects(objects, authorization.Objects) {
		return repository.ReadonlyQueryResult{}, repository.ErrSchemaCatalogAuthorizationDenied
	}

	select {
	case e.concurrency <- struct{}{}:
		defer func() { <-e.concurrency }()
	case <-queryCtx.Done():
		return repository.ReadonlyQueryResult{}, queryCtx.Err()
	}

	startedAt := time.Now()
	rows, err := e.queryer.QueryContext(queryCtx, query)
	if err != nil {
		if queryCtx.Err() != nil {
			return repository.ReadonlyQueryResult{}, queryCtx.Err()
		}
		e.logFailure(ctx, query, "query", err)
		return repository.ReadonlyQueryResult{}, repository.ErrReadonlyQueryUnavailable
	}
	defer rows.Close()

	result = repository.ReadonlyQueryResult{
		PolicyVersion:    analysis.PolicyVersion,
		CatalogVersionID: authorization.CatalogVersionID,
		CatalogVersion:   authorization.CatalogVersion,
		Objects:          append([]repository.SchemaCatalogObjectRef(nil), authorization.Objects...),
	}
	if err = e.scanRows(rows, &result); err != nil {
		if queryCtx.Err() != nil {
			return repository.ReadonlyQueryResult{}, queryCtx.Err()
		}
		e.logFailure(ctx, query, "scan", err)
		return repository.ReadonlyQueryResult{}, repository.ErrReadonlyQueryUnavailable
	}
	e.logSuccess(ctx, query, startedAt, result.ReturnedRows, result.Truncated)
	return result, nil
}

func (e *ReadonlyQueryExecutor) scanRows(rows readonlyQueryRows, result *repository.ReadonlyQueryResult) error {
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(columns) == 0 || len(columns) > maxReadonlyQueryColumns {
		return errors.New("readonly query returned an unsupported column count")
	}
	for index := range columns {
		columns[index] = strings.TrimSpace(columns[index])
		if columns[index] == "" {
			return errors.New("readonly query returned an unnamed column")
		}
	}
	result.Columns = append([]string(nil), columns...)

	for rows.Next() {
		if len(result.Rows) >= e.maxRows {
			result.Truncated = true
			result.TruncationReason = "max_rows"
			break
		}
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		normalized, err := normalizeReadonlyValues(values)
		if err != nil {
			return err
		}
		candidateRows := append(append([][]any(nil), result.Rows...), normalized)
		candidate := *result
		candidate.Rows = candidateRows
		candidate.ReturnedRows = len(candidateRows)
		if size, err := readonlyResultSize(candidate); err != nil {
			return err
		} else if size > e.maxResultBytes {
			result.Truncated = true
			result.TruncationReason = "max_result_bytes"
			break
		}
		result.Rows = candidateRows
		result.ReturnedRows = len(candidateRows)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func normalizeReadonlyValues(values []any) ([]any, error) {
	result := make([]any, len(values))
	for index, value := range values {
		switch current := value.(type) {
		case nil, string, bool, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, float32, float64:
			result[index] = current
		case []byte:
			result[index] = "base64:" + base64.StdEncoding.EncodeToString(current)
		case time.Time:
			result[index] = current.UTC().Format(time.RFC3339Nano)
		default:
			result[index] = fmt.Sprint(current)
		}
	}
	return result, nil
}

func readonlyResultSize(result repository.ReadonlyQueryResult) (int, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func sameCatalogObjects(left, right []repository.SchemaCatalogObjectRef) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(right))
	for _, object := range right {
		key := strings.ToLower(object.ObjectSchema) + "\x00" + strings.ToLower(object.ObjectName)
		seen[key] = struct{}{}
	}
	for _, object := range left {
		key := strings.ToLower(object.ObjectSchema) + "\x00" + strings.ToLower(object.ObjectName)
		if _, ok := seen[key]; !ok {
			return false
		}
	}
	return true
}

func readonlyQueryFingerprint(query string) string {
	digest := sha256.Sum256([]byte(query))
	return hex.EncodeToString(digest[:8])
}

func (e *ReadonlyQueryExecutor) logSuccess(
	ctx context.Context, query string, startedAt time.Time, rows int, truncated bool,
) {
	platformlogger.FromContext(ctx, e.log).Info("SQL readonly query completed",
		zap.String("query_fingerprint", readonlyQueryFingerprint(query)),
		zap.Int("rows", rows), zap.Bool("truncated", truncated),
		zap.Duration("duration", time.Since(startedAt)))
}

func (e *ReadonlyQueryExecutor) logFailure(
	ctx context.Context, query, stage string, err error,
) {
	platformlogger.FromContext(ctx, e.log).Warn("SQL readonly query failed",
		zap.String("query_fingerprint", readonlyQueryFingerprint(query)),
		zap.String("stage", stage), zap.Error(err))
}
