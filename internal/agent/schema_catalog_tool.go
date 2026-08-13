package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

const ToolSearchSchemaCatalog = "search_schema_catalog"

// SchemaCatalogSearcher 是已发布 Catalog 的只读查询能力。
// 具体 PostgreSQL 查询留在 platform/postgres，Agent 只依赖这个窄接口。
type SchemaCatalogSearcher interface {
	SearchPublished(ctx context.Context, dataSourceID uuid.UUID, keyword string, limit int) ([]repository.SchemaCatalogEntry, error)
}

type schemaCatalogInput struct {
	DataSourceID string `json:"dataSourceId,omitempty" jsonschema:"description=可选数据源 UUID；任务只有一个可用 SQL 数据源时可以省略"`
	Keyword      string `json:"keyword" jsonschema:"required,description=业务词、表名、字段名或字段语义"`
	Limit        int    `json:"limit,omitempty" jsonschema:"description=最多返回条数，默认 10，最大 20"`
}

type schemaCatalogResult struct {
	DataSourceID     string          `json:"dataSourceId"`
	CatalogVersion   int             `json:"catalogVersion"`
	ObjectSchema     string          `json:"objectSchema"`
	ObjectName       string          `json:"objectName"`
	ObjectType       string          `json:"objectType"`
	ColumnName       string          `json:"columnName,omitempty"`
	DataType         string          `json:"dataType,omitempty"`
	Nullable         *bool           `json:"nullable,omitempty"`
	Comment          string          `json:"comment,omitempty"`
	SemanticAliases  json.RawMessage `json:"semanticAliases,omitempty"`
	SensitivityLevel string          `json:"sensitivityLevel"`
}

func NewSearchSchemaCatalogTool(searcher SchemaCatalogSearcher) (tool.InvokableTool, error) {
	if searcher == nil {
		return nil, errors.New("schema catalog searcher is required")
	}
	return toolutils.InferTool(
		ToolSearchSchemaCatalog,
		"在管理员发布的 SQL Schema Catalog 中检索未知的表、视图和字段语义；只返回 queryable 元数据，不接受 SQL 片段、不读取业务行数据，也不替代实际数据查询",
		func(ctx context.Context, input schemaCatalogInput) ([]schemaCatalogResult, error) {
			scope, ok := TaskScopeFromContext(ctx)
			if !ok {
				return nil, ErrTaskScopeRequired
			}
			if !scope.DependencyAvailable(ToolDependencySQLServer) {
				return nil, resilience.RetryableFailure(errors.New("SQL Server dependency is unavailable"))
			}
			dataSourceID, err := resolveCatalogDataSource(scope, input.DataSourceID)
			if err != nil {
				return nil, resilience.StrictFailure(err)
			}
			keyword := strings.TrimSpace(input.Keyword)
			if keyword == "" || keyword != input.Keyword {
				return nil, errors.New("keyword must be non-empty and trimmed")
			}
			limit := input.Limit
			if limit < 1 {
				limit = 10
			}
			if limit > 20 {
				limit = 20
			}
			entries, err := searcher.SearchPublished(ctx, dataSourceID, keyword, limit)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				return nil, resilience.RetryableFailure(errors.New("schema catalog is unavailable"))
			}
			result := make([]schemaCatalogResult, 0, len(entries))
			for _, entry := range entries {
				aliases := json.RawMessage(nil)
				if len(entry.SemanticAliases) > 0 && json.Valid(entry.SemanticAliases) {
					aliases = append(json.RawMessage(nil), entry.SemanticAliases...)
				}
				result = append(result, schemaCatalogResult{
					DataSourceID: dataSourceID.String(), CatalogVersion: entry.CatalogVersion,
					ObjectSchema: entry.ObjectSchema, ObjectName: entry.ObjectName,
					ObjectType: entry.ObjectType, ColumnName: entry.ColumnName,
					DataType: entry.DataType, Nullable: entry.Nullable, Comment: entry.Comment,
					SemanticAliases: aliases, SensitivityLevel: entry.SensitivityLevel,
				})
			}
			return result, nil
		},
	)
}

func resolveCatalogDataSource(scope TaskScope, rawID string) (uuid.UUID, error) {
	if scope.taskType != TaskTypeDiagnosis {
		return uuid.Nil, errors.New("schema catalog is available only for diagnosis tasks")
	}
	allowed := make([]ScopedDataSource, 0, len(scope.dataSources))
	for _, source := range scope.dataSources {
		if source.SafetyMode == DataSourceSafetyReadOnly &&
			(source.Role == DataSourceRoleCaseSource || source.Role == DataSourceRoleProduction || source.Role == DataSourceRoleProductReplica) {
			allowed = append(allowed, source)
		}
	}
	if rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			return uuid.Nil, errors.New("dataSourceId must be a valid UUID")
		}
		for _, source := range allowed {
			if source.ID == id {
				return id, nil
			}
		}
		return uuid.Nil, errors.New("data source is not authorized for schema catalog")
	}
	if len(allowed) != 1 {
		return uuid.Nil, fmt.Errorf("dataSourceId is required when %d read-only data sources are authorized", len(allowed))
	}
	return allowed[0].ID, nil
}
