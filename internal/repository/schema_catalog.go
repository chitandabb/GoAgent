package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrSchemaCatalogAuthorizationDenied = errors.New("schema catalog authorization denied")

// SchemaCatalogEntry 是已发布 Catalog 的跨层只读 DTO。
// SemanticAliases 保留 JSONB 原文，避免仓储层依赖 Agent 或某个具体序列化模型。
type SchemaCatalogEntry struct {
	ID               uuid.UUID `json:"id"`
	CatalogVersionID uuid.UUID `json:"catalogVersionId"`
	DataSourceID     uuid.UUID `json:"dataSourceId"`
	CatalogVersion   int       `json:"catalogVersion"`
	ObjectSchema     string    `json:"objectSchema"`
	ObjectName       string    `json:"objectName"`
	ObjectType       string    `json:"objectType"`
	ColumnName       string    `json:"columnName,omitempty"`
	DataType         string    `json:"dataType,omitempty"`
	Nullable         *bool     `json:"nullable,omitempty"`
	Comment          string    `json:"comment,omitempty"`
	SemanticAliases  []byte    `json:"semanticAliases,omitempty"`
	Queryable        bool      `json:"queryable"`
	SensitivityLevel string    `json:"sensitivityLevel"`
}

type SchemaCatalogSearcher interface {
	SearchPublished(ctx context.Context, dataSourceID uuid.UUID, keyword string, limit int) ([]SchemaCatalogEntry, error)
}

// SchemaCatalogObjectRef 是 QueryGuard 提取出的对象级引用。授权检查不接受模糊关键词。
type SchemaCatalogObjectRef struct {
	ObjectSchema string `json:"objectSchema"`
	ObjectName   string `json:"objectName"`
}

// SchemaCatalogAuthorization 固化本次检查命中的发布版本，供执行证据和审计使用。
type SchemaCatalogAuthorization struct {
	CatalogVersionID uuid.UUID                `json:"catalogVersionId"`
	CatalogVersion   int                      `json:"catalogVersion"`
	Objects          []SchemaCatalogObjectRef `json:"objects"`
}

type SchemaCatalogAuthorizer interface {
	AuthorizePublishedObjects(
		ctx context.Context,
		dataSourceID uuid.UUID,
		objects []SchemaCatalogObjectRef,
	) (SchemaCatalogAuthorization, error)
}
