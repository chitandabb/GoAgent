package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
)

func mustFullyConfiguredDefaultToolCatalog(t *testing.T) *ToolCatalog {
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
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases:        runnerTestCaseGetter{},
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
		t.Fatalf("NewDefaultToolCatalog: %v", err)
	}
	return catalog
}

func TestDefaultToolCatalogDiagnosisExcludesTaskCreationAndStatus(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	scope := mustTaskScope(t, auth.RoleAnalyst, TaskTypeDiagnosis, []ScopedDataSource{{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}}, ToolDependencyExternalCase, ToolDependencySQLServer, ToolDependencyKnowledge, ToolDependencyGitHubMCP)
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	names := toolNamesForTest(t, tools)
	for _, forbidden := range []string{ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus} {
		if slices.Contains(names, forbidden) {
			t.Fatalf("diagnosis received conversation command %q: %v", forbidden, names)
		}
	}
	for _, required := range []string{
		ToolReadExternalCase, ToolSearchKnowledge,
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
	} {
		if !slices.Contains(names, required) {
			t.Fatalf("diagnosis missing %q: %v", required, names)
		}
	}
}

func TestDefaultToolCatalogConversationGetsTaskCreationButNeverSQL(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeConversation, nil,
		[]ToolCapability{
			ToolCapabilityCase, ToolCapabilityKnowledge, ToolCapabilityTask,
			ToolCapabilityAttachment, ToolCapabilityWebSearch, ToolCapabilityMemory,
		},
		ToolDependencyExternalCase, ToolDependencyKnowledge,
		ToolDependencyWebSearch, ToolDependencyAttachment)
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	names := toolNamesForTest(t, tools)
	if !slices.Contains(names, ToolCreateDiagnosisTask) {
		t.Fatalf("conversation missing create_diagnosis_task: %v", names)
	}
	for _, forbidden := range []string{
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
	} {
		if slices.Contains(names, forbidden) {
			t.Fatalf("conversation received SQL Tool %q: %v", forbidden, names)
		}
	}
}

func TestDefaultToolCatalogConversationWithoutCaseHasNoTaskCreation(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeConversation, nil,
		[]ToolCapability{ToolCapabilityKnowledge}, ToolDependencyKnowledge)
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	if names := toolNamesForTest(t, tools); slices.Contains(names, ToolCreateDiagnosisTask) {
		t.Fatalf("conversation without case received task creation: %v", names)
	}
}

func TestDefaultToolCatalogMemoryToolPassesGuardWithMemoryPermission(t *testing.T) {
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	userID := uuid.New()
	scope := sourceRecoveryConversationScope(t, userID)
	tools, err := catalog.ToolsFor(context.Background(), scope)
	if err != nil {
		t.Fatalf("ToolsFor: %v", err)
	}
	var memoryTool tool.InvokableTool
	for _, current := range tools {
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
		t.Fatalf("memory Tool missing for memory-capable scope: %v", toolNamesForTest(t, tools))
	}
	// 执行期 Guard 必须放行 memory.read（scope 已授权 memory 能力），
	// 错误应来自 Tool 内部缺少 CommandContext，而不是 Guard 拒绝。
	_, err = memoryTool.InvokableRun(WithTaskScope(context.Background(), scope), `{"entryId":"fact-1"}`)
	if !errors.Is(err, conversation.ErrCommandContextRequired) {
		t.Fatalf("memory Tool error = %v, want CommandContextRequired (guard must pass memory.read)", err)
	}
}
