package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const ToolDatabaseObjectDefinition = "get_database_object_definition"

var ErrDatabaseObjectDefinitionUnavailable = errors.New("database object definition is unavailable")

// DatabaseObjectDefinitionReader 是 SQL 调查 Tool 需要的最小能力。
// Agent 层只依赖这个接口，不直接持有 SQL Server 连接池，也无法拼接任意 SQL。
type DatabaseObjectDefinitionReader interface {
	GetObjectDefinition(ctx context.Context, schemaName, objectName string) (
		definition string, objectType string, truncated bool, err error,
	)
}

type databaseObjectDefinitionInput struct {
	Schema     string `json:"schema" jsonschema:"required,description=允许的数据源 schema，例如 dbo"`
	ObjectName string `json:"objectName" jsonschema:"required,description=存储过程、视图或函数名称"`
}

type DatabaseObjectDefinitionResult struct {
	Schema     string `json:"schema"`
	ObjectName string `json:"objectName"`
	ObjectType string `json:"objectType"`
	Definition string `json:"definition"`
	Truncated  bool   `json:"truncated"`
}

var databaseObjectIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewDatabaseObjectDefinitionTool(reader DatabaseObjectDefinitionReader) (tool.InvokableTool, error) {
	if reader == nil {
		return nil, errors.New("database object definition reader is required")
	}
	return toolutils.InferTool(
		ToolDatabaseObjectDefinition,
		"读取管理员允许的 SQL Server 存储过程、视图或函数的 SQL 定义；用于检查对象内部实现，不要用 Schema Catalog 元数据检索代替定义读取；仅只读，不接受任意 SQL",
		func(ctx context.Context, input databaseObjectDefinitionInput) (DatabaseObjectDefinitionResult, error) {
			schemaName := strings.TrimSpace(input.Schema)
			objectName := strings.TrimSpace(input.ObjectName)
			if schemaName != input.Schema || objectName != input.ObjectName ||
				!databaseObjectIdentifierPattern.MatchString(schemaName) ||
				!databaseObjectIdentifierPattern.MatchString(objectName) {
				return DatabaseObjectDefinitionResult{}, errors.New("schema and objectName must be simple SQL identifiers")
			}
			definition, objectType, truncated, err := reader.GetObjectDefinition(ctx, schemaName, objectName)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return DatabaseObjectDefinitionResult{}, err
				}
				// 数据库驱动错误可能包含主机、实例或连接参数；模型侧只接收稳定的安全错误。
				return DatabaseObjectDefinitionResult{}, resilience.RetryableFailure(
					ErrDatabaseObjectDefinitionUnavailable,
				)
			}
			return DatabaseObjectDefinitionResult{
				Schema: schemaName, ObjectName: objectName, ObjectType: objectType,
				Definition: definition, Truncated: truncated,
			}, nil
		},
	)
}
