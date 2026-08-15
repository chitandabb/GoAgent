package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrReadonlyQueryUnavailable = errors.New("readonly query unavailable")

// ReadonlyQueryResult 是 SQL 只读执行器返回给 Agent 层的稳定 DTO。
// Rows 只包含已经经过结果大小限制和 JSON 安全转换的值。
type ReadonlyQueryResult struct {
	PolicyVersion    string                   `json:"policyVersion"`
	CatalogVersionID uuid.UUID                `json:"catalogVersionId"`
	CatalogVersion   int                      `json:"catalogVersion"`
	Objects          []SchemaCatalogObjectRef `json:"objects"`
	Columns          []string                 `json:"columns"`
	Rows             [][]any                  `json:"rows"`
	ReturnedRows     int                      `json:"returnedRows"`
	Truncated        bool                     `json:"truncated"`
	TruncationReason string                   `json:"truncationReason,omitempty"`
}

// ReadonlyQueryExecutor 只接受已经通过 QueryGuard 和已发布 Catalog 复核的查询。
// dataSourceID 是本轮 RunAccess 数据源 Grant 选中的只读源，不包含连接信息或凭证。
type ReadonlyQueryExecutor interface {
	Execute(ctx context.Context, dataSourceID uuid.UUID, query string) (ReadonlyQueryResult, error)
}
