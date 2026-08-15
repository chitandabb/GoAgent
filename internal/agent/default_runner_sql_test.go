package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

// TestDefaultRunnerFixedProfileContainsConstructedSQLTools 证明 SQL Tool 的
// 模型可见性只由启动装配（固定 diagnosis-default Profile）决定：成功构造的
// SQL 三件套进入 Profile，未构造的不进入；任何 per-run 状态都不改变名单。
func TestDefaultRunnerFixedProfileContainsConstructedSQLTools(t *testing.T) {
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
	resolved, err := runner.toolCatalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	names := resolved.ModelVisibleNames
	for _, want := range []string{ToolDatabaseObjectDefinition, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery} {
		if !slices.Contains(names, want) {
			t.Fatalf("fixed Profile = %v, missing %q", names, want)
		}
	}

	// 未构造 SQL Tool 的部署：Profile 中不存在 SQL Tool，且重复解析名单不变。
	withoutSQL, err := NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: &runnerTestModel{state: &runnerModelState{}}, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:           filepath.Join("..", "..", "config", "skills"),
		SystemInstruction:   runnerTestSystemInstruction,
		BaselineInstruction: runnerTestBaselineInstruction,
		Logger:              zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewDefaultRunner(no SQL): %v", err)
	}
	withoutNames := withoutSQL.ProfileToolNames()
	if slices.Contains(withoutNames, ToolDatabaseObjectDefinition) ||
		slices.Contains(withoutNames, ToolExecuteReadonlyQuery) {
		t.Fatalf("unassembled SQL Tools leaked into the fixed Profile: %v", withoutNames)
	}
	again := withoutSQL.ProfileToolNames()
	if !slices.Equal(withoutNames, again) {
		t.Fatal("profile names changed between snapshots")
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
