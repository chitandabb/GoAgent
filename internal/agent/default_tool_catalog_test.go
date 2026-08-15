package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
)

// mustFullyConfiguredDefaultToolCatalog 构造完整注册、绑定 conversation-default
// 的默认 Catalog（与会话 Runner 的生产构造一致）。
func mustFullyConfiguredDefaultToolCatalog(t *testing.T) *ToolCatalog {
	t.Helper()
	return mustDefaultToolCatalogForProfile(t, NewConversationDefaultToolCatalog)
}

// mustDiagnosisConfiguredDefaultCatalogForTest 构造完整注册、绑定
// diagnosis-default 的默认 Catalog（与诊断 Runner 的生产构造一致）。
func mustDiagnosisConfiguredDefaultCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	return mustDefaultToolCatalogForProfile(t, NewDiagnosisDefaultToolCatalog)
}

type defaultCatalogConstructor func(context.Context, DefaultToolCatalogDependencies) (*ToolCatalog, error)

func mustDefaultToolCatalogForProfile(
	t *testing.T,
	constructor defaultCatalogConstructor,
) *ToolCatalog {
	t.Helper()
	knowledgeTool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{}})
	if err != nil {
		t.Fatalf("NewSearchKnowledgeTool: %v", err)
	}
	webSearch, err := NewWebSearchTool(webResearcherStub{})
	if err != nil {
		t.Fatalf("NewWebSearchTool: %v", err)
	}
	fetchPage, err := NewFetchPublicPageTool(webResearcherStub{})
	if err != nil {
		t.Fatalf("NewFetchPublicPageTool: %v", err)
	}
	sqlDefinition, err := NewDatabaseObjectDefinitionTool(&stubDatabaseObjectDefinitionReader{
		definition: "CREATE VIEW dbo.v AS SELECT 1", objectType: "VIEW",
	})
	if err != nil {
		t.Fatalf("NewDatabaseObjectDefinitionTool: %v", err)
	}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(&stubReadonlyQueryExecutor{})
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	githubTools := make([]tool.BaseTool, 0, len(GitHubReadOnlyTools))
	for _, name := range GitHubReadOnlyTools {
		githubTools = append(githubTools, newNamedToolForTest(t, name))
	}
	catalog, err := constructor(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases:        runnerTestCaseGetter{},
		SkillReference:       newNamedToolForTest(t, ToolReadSkillReference),
		GitHubTools:          githubTools,
		SQLObjectDefinitions: sqlDefinition,
		SchemaCatalog:        mustSchemaCatalogToolForTest(t),
		ReadonlyQuery:        readonlyQuery,
		KnowledgeSearch:      knowledgeTool,
		WebSearch:            webSearch,
		FetchPublicPage:      fetchPage,
		CreateDiagnosisTask:  &diagnosisToolCreatorStub{},
		DiagnosisTaskStatus:  &diagnosisTaskStatusReaderStub{},
		AttachmentReader:     &attachmentReaderStub{},
		ConversationMemorySources: &sourceRecoveryReaderStub{},
	})
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	return catalog
}

// TestDefaultToolCatalogDiagnosisProfileExcludesTaskCommands 证明 Diagnosis
// 固定 Profile 不暴露任务创建/状态命令，但包含只读证据 Tool 与 SQL 三件套；
// 名单不随任何 per-run 状态变化。
func TestDefaultToolCatalogDiagnosisProfileExcludesTaskCommands(t *testing.T) {
	catalog := mustDiagnosisConfiguredDefaultCatalogForTest(t)
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	names := resolved.ModelVisibleNames
	for _, forbidden := range []string{ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus} {
		if slices.Contains(names, forbidden) {
			t.Fatalf("diagnosis profile received conversation command %q: %v", forbidden, names)
		}
	}
	for _, required := range []string{
		ToolReadExternalCase, ToolSearchKnowledge,
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
		ToolSkill,
	} {
		if !slices.Contains(names, required) {
			t.Fatalf("diagnosis profile missing %q: %v", required, names)
		}
	}
	// 重复解析名单不变。
	again, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.ModelVisibleNames, names) {
		t.Fatal("diagnosis profile names changed between resolutions")
	}
}

// TestDefaultToolCatalogConversationProfileContainsTextToSQLButNotObjectDDL
// 证明 Conversation 固定 Profile：任务创建/状态命令与 Text-to-SQL 两件套在
// 名单中（执行期由 RunAccess 收窄），对象定义 Tool 按最小 Tool 集原则仅供
// Diagnosis。
func TestDefaultToolCatalogConversationProfileContainsTextToSQLButNotObjectDDL(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	names := resolved.ModelVisibleNames
	for _, required := range []string{
		ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
		ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
	} {
		if !slices.Contains(names, required) {
			t.Fatalf("conversation profile missing %q: %v", required, names)
		}
	}
	if slices.Contains(names, ToolDatabaseObjectDefinition) {
		t.Fatalf("conversation profile must not contain object definition: %v", names)
	}
}

// TestDefaultToolCatalogConversationCommandGatedByRunAccess 证明 Schema 可见
// 不等于可执行：create_diagnosis_task 始终在 Conversation Profile，但没有
// diagnosis.create Permission 时执行期 Guard fail-closed、creator 零调用。
func TestDefaultToolCatalogConversationCommandGatedByRunAccess(t *testing.T) {
	creator := &diagnosisToolCreatorStub{}
	catalog := mustDefaultToolCatalogForProfile(t, func(ctx context.Context, deps DefaultToolCatalogDependencies) (*ToolCatalog, error) {
		deps.CreateDiagnosisTask = creator
		return NewConversationDefaultToolCatalog(ctx, deps)
	})
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	var createTool tool.InvokableTool
	for _, current := range resolved.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		if info.Name == ToolCreateDiagnosisTask {
			createTool = current.(tool.InvokableTool)
			break
		}
	}
	if createTool == nil {
		t.Fatal("create_diagnosis_task missing from the fixed conversation profile")
	}
	withoutPermission := mustConversationTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionKnowledgeRead},
		agentruntime.ResourceGrantsConfig{},
	)
	ctx := withTestRunAccess(context.Background(), withoutPermission)
	ctx = conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(),
		Actor: conversation.Actor{UserID: uuid.New()},
	})
	if _, err := createTool.InvokableRun(ctx, `{"externalCaseId":"`+runnerTestCaseID.String()+`","diagnosisGoal":"检查"}`); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("create without diagnosis.create error = %v, want ErrToolNotAllowed", err)
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0", creator.calls)
	}
}

// TestDefaultToolCatalogMemoryToolPassesGuardWithMemoryPermission 证明执行期
// Guard 只读 RunAccess：memory.read 存在时 Guard 放行，错误来自 Tool 内部
// 缺少 CommandContext。
func TestDefaultToolCatalogMemoryToolPassesGuardWithMemoryPermission(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	userID := uuid.New()
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	var memoryTool tool.InvokableTool
	for _, current := range resolved.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		if info.Name == ToolReadConversationMemorySources {
			memoryTool = current.(tool.InvokableTool)
			break
		}
	}
	if memoryTool == nil {
		t.Fatalf("memory Tool missing from fixed conversation profile: %v", resolved.ModelVisibleNames)
	}
	ctx := withTestRunAccess(context.Background(), sourceRecoveryConversationAccess(t, userID))
	_, err = memoryTool.InvokableRun(ctx, `{"entryId":"fact-1"}`)
	if !errors.Is(err, conversation.ErrCommandContextRequired) {
		t.Fatalf("memory Tool error = %v, want CommandContextRequired (guard must pass memory.read)", err)
	}
}
