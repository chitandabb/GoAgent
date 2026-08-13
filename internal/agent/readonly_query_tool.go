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

// NewExecuteReadonlyQueryTool 只暴露经过 QueryGuard、Catalog 和资源限制的窄执行接口。
// Agent 不接触 *sql.DB，也不能通过 Tool 参数传入连接、凭证或 SQL 变量。
func NewExecuteReadonlyQueryTool(executor repository.ReadonlyQueryExecutor) (tool.InvokableTool, error) {
	if executor == nil {
		return nil, errors.New("readonly query executor is required")
	}
	return toolutils.InferTool(
		ToolExecuteReadonlyQuery,
		"在已授权 SQL Server 数据源上执行模型根据用户请求生成的单条只读 T-SQL；执行器应用 QueryGuard、已发布 Schema Catalog、超时、行数、字节数和并发限制。用于读取记录、核对值或聚合统计，不要用 Schema Catalog 检索代替实际数据查询",
		func(ctx context.Context, input readonlyQueryInput) (repository.ReadonlyQueryResult, error) {
			scope, ok := TaskScopeFromContext(ctx)
			if !ok {
				return repository.ReadonlyQueryResult{}, ErrTaskScopeRequired
			}
			if !scope.DependencyAvailable(ToolDependencySQLServer) {
				return repository.ReadonlyQueryResult{}, resilience.RetryableFailure(
					errors.New("SQL Server dependency is unavailable"),
				)
			}
			if strings.TrimSpace(input.Query) == "" {
				return repository.ReadonlyQueryResult{}, errors.New("query must be non-empty")
			}
			dataSourceID, err := resolveCatalogDataSource(scope, input.DataSourceID)
			if err != nil {
				return repository.ReadonlyQueryResult{}, resilience.StrictFailure(err)
			}
			return executor.Execute(ctx, dataSourceID, input.Query)
		},
	)
}
