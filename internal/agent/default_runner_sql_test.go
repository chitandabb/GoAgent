package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestDefaultRunnerAuthorizesObjectDefinitionToolByScope(t *testing.T) {
	sqlTool, err := NewDatabaseObjectDefinitionTool(&stubDatabaseObjectDefinitionReader{
		definition: "CREATE VIEW dbo.v_Test AS SELECT 1", objectType: "VIEW",
	})
	if err != nil {
		t.Fatalf("NewDatabaseObjectDefinitionTool: %v", err)
	}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(&stubReadonlyQueryExecutor{})
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	runner, err := NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: &runnerTestModel{state: &runnerModelState{}}, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:            filepath.Join("..", "..", "config", "skills"),
		SystemInstruction:    runnerTestSystemInstruction,
		BaselineInstruction:  runnerTestBaselineInstruction,
		SQLObjectDefinitions: sqlTool, SchemaCatalog: mustSchemaCatalogToolForTest(t),
		ReadonlyQuery: readonlyQuery, Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewDefaultRunner: %v", err)
	}

	readOnlySource := ScopedDataSource{
		ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
	}
	withDependency := mustTaskScope(
		t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{readOnlySource}, ToolDependencySQLServer,
	)
	tools, err := runner.toolCatalog.ToolsFor(context.Background(), withDependency)
	if err != nil {
		t.Fatalf("ToolsFor(read-only): %v", err)
	}
	if names := toolNamesForTest(t, tools); !slices.Contains(names, ToolDatabaseObjectDefinition) ||
		!slices.Contains(names, ToolSearchSchemaCatalog) || !slices.Contains(names, ToolExecuteReadonlyQuery) {
		t.Fatalf("read-only tools = %v, missing object definition Tool", names)
	}

	withoutDependency := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{readOnlySource})
	tools, err = runner.toolCatalog.ToolsFor(context.Background(), withoutDependency)
	if err != nil {
		t.Fatalf("ToolsFor(no SQL Server): %v", err)
	}
	if names := toolNamesForTest(t, tools); slices.Contains(names, ToolDatabaseObjectDefinition) ||
		slices.Contains(names, ToolExecuteReadonlyQuery) {
		t.Fatalf("tools without SQL Server dependency = %v", names)
	}

	labScope := mustTaskScope(t, auth.RoleAdmin, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab,
	}}, ToolDependencySQLServer)
	tools, err = runner.toolCatalog.ToolsFor(context.Background(), labScope)
	if err != nil {
		t.Fatalf("ToolsFor(bounded lab): %v", err)
	}
	if names := toolNamesForTest(t, tools); slices.Contains(names, ToolDatabaseObjectDefinition) ||
		slices.Contains(names, ToolExecuteReadonlyQuery) {
		t.Fatalf("bounded-lab scope received read-only object definition Tool: %v", names)
	}
}

func mustSchemaCatalogToolForTest(t *testing.T) tool.InvokableTool {
	t.Helper()
	current, err := NewSearchSchemaCatalogTool(&stubSchemaCatalogSearcher{})
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	return current
}

func TestAgentInstructionDisclosesUnavailableSQLDependency(t *testing.T) {
	scope := runnerTestScopeWithCapabilities(t, []ToolCapability{ToolCapabilityCase, ToolCapabilitySQL}, ToolDependencyExternalCase)
	instruction := buildAgentInstruction(runnerTestSystemInstruction, SkillSQLInvestigation, "test SQL Skill", scope)
	if !strings.Contains(instruction, sqlServerUnavailableMessage) {
		t.Fatalf("instruction did not disclose SQL Server degradation: %s", instruction)
	}
}
