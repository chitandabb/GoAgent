package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const ToolExecuteReadonlyQuery = "execute_readonly_query"

type readonlyQueryInput struct {
	DataSourceID string `json:"dataSourceId,omitempty" jsonschema:"description=可选数据源 UUID；任务只有一个可用 SQL 数据源时可以省略"`
	Query        string `json:"query" jsonschema:"required,description=根据用户请求生成的单条只读 T-SQL；执行器会使用已发布 Schema Catalog 和 QueryGuard 校验"`
}

type readonlyQueryToolOutput struct {
	repository.ReadonlyQueryResult
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Guidance string `json:"guidance,omitempty"`
}

// NewExecuteReadonlyQueryTool 只暴露经过 QueryGuard、Catalog 和资源限制的窄执行接口。
// Agent 不接触 *sql.DB，也不能通过 Tool 参数传入连接、凭证或 SQL 变量。
func NewExecuteReadonlyQueryTool(executor repository.ReadonlyQueryExecutor) (tool.InvokableTool, error) {
	if executor == nil {
		return nil, errors.New("readonly query executor is required")
	}
	return toolutils.InferTool(
		ToolExecuteReadonlyQuery,
		"在已授权 SQL Server 数据源上执行模型根据用户请求生成的单条只读 T-SQL；执行器应用 QueryGuard、已发布 Schema Catalog、超时、行数、字节数和并发限制。用于读取记录、核对值或聚合统计，不要用 Schema Catalog 检索代替实际数据查询",
		func(ctx context.Context, input readonlyQueryInput) (readonlyQueryToolOutput, error) {
			if strings.TrimSpace(input.Query) == "" {
				return readonlyQueryToolOutput{}, errors.New("query must be non-empty")
			}
			dataSourceID, err := resolveGrantedSQLDataSource(ctx, input.DataSourceID)
			if err != nil {
				return readonlyQueryToolOutput{}, resilience.StrictFailure(err)
			}
			result, err := executor.Execute(ctx, dataSourceID, input.Query)
			if errors.Is(err, repository.ErrReadonlyQueryRejected) ||
				errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied) {
				// The guard still rejects the query before any data access. Returning
				// a structured result lets the Agent correct its next query instead
				// of causing the whole diagnosis task to be retried.
				return readonlyQueryToolOutput{
					OK:       false,
					Error:    "query_rejected",
					Guidance: "先使用 search_schema_catalog 确认已发布的 schema.object，再生成一条符合只读策略的查询；不能确认时直接输出带限制的 JSON 报告。",
				}, nil
			}
			if err != nil {
				return readonlyQueryToolOutput{}, err
			}
			return readonlyQueryToolOutput{ReadonlyQueryResult: result, OK: true}, nil
		},
	)
}
