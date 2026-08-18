package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/BurntSushi/toml"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type stubAgentChatModel struct{}

type stubConversationMemory struct{}

func (stubConversationMemory) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	return nil, conversationmemory.ErrSnapshotNotFound
}

func (stubConversationMemory) PrepareActive(
	context.Context,
	conversationmemory.PrepareActiveRequest,
) (conversationmemory.Snapshot, error) {
	return conversationmemory.Snapshot{}, conversationmemory.ErrCompactionFailed
}

func (stubAgentChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (stubAgentChatModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := (stubAgentChatModel{}).Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (stubAgentChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return stubAgentChatModel{}, nil
}

type stubAgentExternalCases struct{}

func (stubAgentExternalCases) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("not used")
}

func TestBuildAgentRuntimeDegradesWhenChatModelIsUnavailable(t *testing.T) {
	want := errors.New("missing model key")
	runtime, err := buildAgentRuntime(
		context.Background(), testAgentConfig(), stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return nil, want
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if runtime.runner != nil || !errors.Is(runtime.unavailable, want) {
		t.Fatalf("unexpected degraded runtime: %+v", runtime)
	}
}

func TestBuildAgentRuntimeWiresConversationShadowPreflight(t *testing.T) {
	cfg := testAgentConfig()
	profile := cfg.Models.Chat.Profiles["test"]
	profile.ContextWindowTokens = 4096
	profile.MaxOutputTokens = 512
	profile.PromptSafetyMarginTokens = 256
	profile.PromptSafetyMarginRatio = 0.05
	profile.TokenizerStrategy = config.TokenizerStrategyLocalCalibrated
	cfg.Models.Chat.Profiles["test"] = profile
	cfg.Agent.ContextMemory = config.ContextMemoryConfig{
		ShadowPreflightEnabled: true,
		ContinuousTailEnabled:  true, SummaryTailEnabled: true,
		MemoryMaxRatio: 0.20, SummaryMaxRatio: 0.05, TailMaxRatio: 0.15,
		PreflightTimeoutMillis: 250,
		SoftThresholdRatio:     0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 256, SyncCompactionTimeoutMillis: 45_000,
	}
	memoryBuilt := false
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			conversationMemory: func(context.Context, *gorm.DB, config.Config) (agent.ConversationMemory, error) {
				memoryBuilt = true
				return stubConversationMemory{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime(): %v", err)
	}
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	message := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "检查上下文预算",
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	response, err := runtime.conversation.Respond(ctx, conversation.AgentRequest{
		Conversation: conversation.Conversation{ID: conversationID, UserID: userID, Status: conversation.StatusActive},
		UserMessage:  message, History: []conversation.Message{message},
	})
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	if !memoryBuilt || response.RunObservation == nil || response.RunObservation.PromptManifest == nil {
		t.Fatalf("shadow manifest was not wired: %+v", response.RunObservation)
	}
}

func TestBuildAgentRuntimeSharesActiveMemoryWithSourceRecovery(t *testing.T) {
	cfg := testAgentConfig()
	profile := cfg.Models.Chat.Profiles["test"]
	profile.ContextWindowTokens = 4096
	profile.MaxOutputTokens = 512
	profile.PromptSafetyMarginTokens = 256
	profile.TokenizerStrategy = config.TokenizerStrategyLocalCalibrated
	cfg.Models.Chat.Profiles["test"] = profile
	cfg.Agent.ContextMemory = config.ContextMemoryConfig{
		ShadowPreflightEnabled: true, ContinuousTailEnabled: true, SummaryTailEnabled: true,
		MemoryMaxRatio: 0.20, SummaryMaxRatio: 0.05, TailMaxRatio: 0.15,
		PreflightTimeoutMillis: 250, SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 256, SyncCompactionTimeoutMillis: 45_000,
		SourceRecoveryEnabled: true, SourceRecoveryMaxMessages: 20,
		SourceRecoveryMaxTokens: 8192, SourceRecoveryMaxCalls: 2,
	}
	memoryBuildCount := 0
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, &gorm.DB{}, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			knowledgeSearch: func(
				context.Context, *gorm.DB, config.Config, model.ToolCallingChatModel, *zap.Logger,
			) (tool.BaseTool, error) {
				return nil, nil
			},
			conversationMemory: func(context.Context, *gorm.DB, config.Config) (agent.ConversationMemory, error) {
				memoryBuildCount++
				return stubConversationMemory{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime(): %v", err)
	}
	defer runtime.close()
	if memoryBuildCount != 1 || runtime.conversation == nil {
		t.Fatalf("memory build count/runtime = %d/%+v", memoryBuildCount, runtime.conversation)
	}
}

func TestBuildDiagnosisAgentRuntimeDoesNotRequireConversationMemory(t *testing.T) {
	cfg := testAgentConfig()
	profile := cfg.Models.Chat.Profiles["test"]
	profile.ContextWindowTokens = 4096
	profile.MaxOutputTokens = 512
	profile.PromptSafetyMarginTokens = 256
	profile.PromptSafetyMarginRatio = 0.05
	profile.TokenizerStrategy = config.TokenizerStrategyLocalCalibrated
	cfg.Models.Chat.Profiles["test"] = profile
	cfg.Agent.ContextMemory = config.ContextMemoryConfig{
		ShadowPreflightEnabled: true, ContinuousTailEnabled: true, SummaryTailEnabled: true,
		MemoryMaxRatio: 0.20, SummaryMaxRatio: 0.05, TailMaxRatio: 0.15,
		PreflightTimeoutMillis: 250, SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 256, SyncCompactionTimeoutMillis: 45_000,
	}
	memoryBuilt := false

	runtime, err := buildAgentRuntimeForRole(
		context.Background(), agentRuntimeRoleDiagnosis, cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(),
		agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			conversationMemory: func(context.Context, *gorm.DB, config.Config) (agent.ConversationMemory, error) {
				memoryBuilt = true
				return nil, errors.New("conversation-memory credential is unavailable")
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntimeForRole(diagnosis): %v", err)
	}
	defer runtime.close()
	if memoryBuilt || runtime.orchestrator == nil {
		t.Fatalf("diagnosis runtime memory/orchestrator = %t/%+v", memoryBuilt, runtime.orchestrator)
	}
}

func TestBuildAgentRuntimeRejectsInvalidSkillPackage(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agent.SkillsDirectory = filepath.Join(t.TempDir(), "missing")
	_, err := buildAgentRuntime(context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
		chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
			return stubAgentChatModel{}, nil
		},
	})
	if err == nil {
		t.Fatal("buildAgentRuntime accepted missing Skill package")
	}
}

func TestBuildAgentRuntimeRejectsMissingPromptFile(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agent.SystemPromptFile = filepath.Join(t.TempDir(), "missing.md")
	_, err := buildAgentRuntime(context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{})
	if err == nil {
		t.Fatal("buildAgentRuntime accepted missing Prompt file")
	}
}

func TestBuildAgentRuntimeDegradesWhenGitHubMCPIsUnavailable(t *testing.T) {
	cfg := testAgentConfig()
	cfg.GitHubMCP.Enabled = true
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			githubMCP: func(context.Context, config.GitHubMCPConfig, *zap.Logger) ([]tool.BaseTool, func() error, error) {
				return nil, nil, errors.New("github unavailable")
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if runtime.runner == nil || runtime.orchestrator == nil {
		t.Fatal("ticket diagnosis runtime was not initialized")
	}
}

func TestBuildAgentRuntimeSkipsSQLToolWhenSQLServerIsUnavailable(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	called := false
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			sqlObjectDefinitions: func(*sql.DB, config.SQLServerConfig, *zap.Logger) (tool.BaseTool, error) {
				called = true
				return nil, errors.New("must not be called")
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if called || runtime.runner == nil || runtime.orchestrator == nil {
		t.Fatalf("unexpected degraded SQL runtime: called=%t runtime=%+v", called, runtime)
	}
}

func TestBuildAgentRuntimeRegistersOrDegradesWebResearch(t *testing.T) {
	cfg := testAgentConfig()
	cfg.WebSearch = config.WebSearchConfig{
		Enabled: true, Provider: "firecrawl", BaseURL: "https://api.firecrawl.dev",
		APIKeyEnv: "FIRECRAWL_API_KEY_TEST", TimeoutMillis: 5000,
		MaxResults: 5, MaxFetchedPages: 3, MaxPageChars: 20000, MaxRounds: 2,
		MaxResponseBytes: 64 * 1024,
		Redaction:        config.WebSearchRedactionConfig{MaxInputRunes: 1024, MaxOutputRunes: 384, MinOutputRunes: 8},
	}
	t.Setenv("FIRECRAWL_API_KEY_TEST", "test-key")
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	defer runtime.close()
	if runtime.webResearch == nil {
		t.Fatal("web research service was not registered")
	}

	cfg.WebSearch.APIKeyEnv = "MISSING_FIRECRAWL_KEY_TEST"
	t.Setenv("MISSING_FIRECRAWL_KEY_TEST", "")
	degraded, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime degraded: %v", err)
	}
	defer degraded.close()
	if degraded.webResearch != nil {
		t.Fatal("unavailable web research service was exposed")
	}
}

func TestBuildAgentRuntimeRegistersSQLToolWhenSQLServerIsAvailable(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.ID = "8d5c67dc-4c09-4ee5-9e80-4d822303dc35"
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	called := false
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, new(sql.DB), nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
			sqlObjectDefinitions: func(*sql.DB, config.SQLServerConfig, *zap.Logger) (tool.BaseTool, error) {
				called = true
				return toolutils.InferTool(
					"get_database_object_definition", "test SQL Tool",
					func(context.Context, struct{}) (string, error) { return "ok", nil },
				)
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if !called || runtime.runner == nil || runtime.orchestrator == nil {
		t.Fatalf("SQL Tool was not registered: called=%t runtime=%+v", called, runtime)
	}
}

func TestAgentRuntimeCloseReleasesMCP(t *testing.T) {
	closed := false
	runtime := &agentRuntime{closeMCP: func() error { closed = true; return nil }}
	if err := runtime.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !closed {
		t.Fatal("MCP close was not called")
	}
}

func TestBuildQueryRewriterPreservesProviderFailedSemanticsWhenProfileKeyIsMissing(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "query-rewrite.md")
	if err := os.WriteFile(promptPath, []byte("Return strict JSON."), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISSING_REWRITE_KEY", "")
	cfg := testAgentConfig()
	cfg.Models.Chat.Profiles = map[string]config.ChatModelProfileConfig{
		"rewrite": {
			Provider: "dashscope", BaseURL: "https://example.com/v1", APIKeyEnv: "MISSING_REWRITE_KEY",
			Model: "qwen3.6-flash", ThinkingMode: "disabled", TimeoutMillis: 3000, MaxOutputTokens: 256,
		},
	}
	cfg.Knowledge.Retrieval.QueryRewrite = config.KnowledgeQueryRewriteConfig{
		Enabled: true, ModelProfile: "rewrite", PromptFile: promptPath, PromptVersion: "query-rewrite-v1",
		TimeoutMillis: 3000, MaxSubqueries: 1, MaxOutputRunes: 1024,
	}
	rewriter := buildQueryRewriter(context.Background(), cfg, nil, zap.NewNop())
	if rewriter == nil {
		t.Fatal("missing profile key disabled query rewrite instead of preserving failure semantics")
	}
	if _, err := rewriter.Rewrite(context.Background(), "SQL error 258"); err == nil {
		t.Fatal("unavailable query rewriter returned no error")
	}
}

func testAgentConfig() config.Config {
	configRoot := filepath.Join("..", "..", "config")
	return config.Config{
		Agent: config.AgentConfig{
			SkillsDirectory:           filepath.Join(configRoot, "skills"),
			PromptVersion:             "diagnosis-test-v1",
			SystemPromptFile:          filepath.Join(configRoot, "prompts", "diagnosis-system.md"),
			BaselinePromptFile:        filepath.Join(configRoot, "prompts", "evaluation-baseline.md"),
			ReportContractFile:        filepath.Join(configRoot, "prompts", "report-contract.md"),
			ConversationPromptVersion: "conversation-test-v1",
			ConversationPromptFile:    filepath.Join(configRoot, "prompts", "conversation-system.md"),
		},
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			Enabled: true, ActiveProfileName: "test",
			Profiles: map[string]config.ChatModelProfileConfig{
				"test": {Provider: "dashscope", Model: "fixture-v1"},
			},
		}},
	}
}

// TestBuildAgentRuntimeCarriesProductionActiveProfileIdentity 证明仓库演示
// 配置的 [models.chat] activeProfile 身份（provider/model）会原样进入
// Agent Runtime。模型工厂使用 stub，不创建任何真实 Provider。
func TestBuildAgentRuntimeCarriesProductionActiveProfileIdentity(t *testing.T) {
	var decoded config.Config
	path := filepath.Join("..", "..", "config", "mesguard.toml")
	if _, err := toml.DecodeFile(path, &decoded); err != nil {
		t.Fatalf("DecodeFile(%q): %v", path, err)
	}
	cfg := testAgentConfig()
	cfg.Models.Chat = decoded.Models.Chat
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, nil, nil, zap.NewNop(), agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return stubAgentChatModel{}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime(): %v", err)
	}
	if runtime.modelProvider != "stepfun" || runtime.modelID != "step-3.7-flash" {
		t.Fatalf("agent runtime identity = %q/%q, want stepfun/step-3.7-flash",
			runtime.modelProvider, runtime.modelID)
	}
}

func TestBuildAgentRuntimeFailsClosedOnInvalidSQLDataSourceID(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.ID = "not-a-uuid"
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	builders := agentRuntimeBuilders{
		chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
			return stubAgentChatModel{}, nil
		},
	}
	// 无效 UUID 必须在任何 SQL Adapter 构造前 fail-closed：builder 被调用即说明接线错误。
	builders.sqlObjectDefinitions = func(*sql.DB, config.SQLServerConfig, *zap.Logger) (tool.BaseTool, error) {
		t.Fatal("sqlObjectDefinitions builder ran despite invalid data source id")
		return nil, errors.New("must not run")
	}
	_, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, new(sql.DB), nil, zap.NewNop(), builders,
	)
	if err == nil || !strings.Contains(err.Error(), "data source id") {
		t.Fatalf("buildAgentRuntime() error = %v, want fail-closed data source id rejection", err)
	}
}

// conversationSQLExecutorStub 记录执行期 Context 的权威 RunAccess 与数据源。
type conversationSQLExecutorStub struct {
	mu           sync.Mutex
	calls        int
	dataSourceID uuid.UUID
	sqlRead      bool
	grants       []uuid.UUID
}

func (s *conversationSQLExecutorStub) Execute(
	ctx context.Context, dataSourceID uuid.UUID, _ string,
) (repository.ReadonlyQueryResult, error) {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.dataSourceID = dataSourceID
	if ok {
		s.sqlRead = access.Allows(agentruntime.PermissionSQLRead)
		s.grants = access.Grants().DataSourceIDs()
	}
	return repository.ReadonlyQueryResult{
		PolicyVersion: "tsql-readonly-v1", Columns: []string{"Status"},
		Rows: [][]any{{"处理中"}}, ReturnedRows: 1,
	}, nil
}

func (s *conversationSQLExecutorStub) snapshot() (int, uuid.UUID, bool, []uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.dataSourceID, s.sqlRead, append([]uuid.UUID(nil), s.grants...)
}

type conversationSQLCatalogSearcherStub struct{}

func (conversationSQLCatalogSearcherStub) SearchPublished(
	context.Context, uuid.UUID, string, int,
) ([]repository.SchemaCatalogEntry, error) {
	return []repository.SchemaCatalogEntry{{
		CatalogVersion: 1, ObjectSchema: "dbo", ObjectName: "Tickets", ObjectType: "TABLE",
		ColumnName: "Status", SensitivityLevel: "internal",
	}}, nil
}

// scriptedSQLChatModel 在 Conversation 真实装配中先调用 execute_readonly_query。
type scriptedSQLChatModel struct {
	queryDone bool
}

func (m *scriptedSQLChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *scriptedSQLChatModel) Generate(
	_ context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	hasQueryTool := false
	for _, info := range common.Tools {
		if info.Name == agent.ToolExecuteReadonlyQuery {
			hasQueryTool = true
		}
	}
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == agent.ToolExecuteReadonlyQuery {
			m.queryDone = true
		}
	}
	if !m.queryDone && hasQueryTool {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-sql", Function: schema.FunctionCall{
				Name: agent.ToolExecuteReadonlyQuery, Arguments: `{"query":"SELECT Status FROM dbo.v_Tickets"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("工单当前状态为 处理中。", nil), nil
}

func (m *scriptedSQLChatModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestBuildAgentRuntimeWiresSQLToolsAndGrantIntoConversationRunner(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.ID = "8d5c67dc-4c09-4ee5-9e80-4d822303dc35"
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	sqlDataSourceID := uuid.MustParse(cfg.SQLServer.ID)
	executor := &conversationSQLExecutorStub{}
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, new(sql.DB), &gorm.DB{}, zap.NewNop(),
		agentRuntimeBuilders{
			chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
				return &scriptedSQLChatModel{}, nil
			},
			schemaCatalog: func(*gorm.DB, uuid.UUID, *zap.Logger) (tool.BaseTool, error) {
				return agent.NewSearchSchemaCatalogTool(conversationSQLCatalogSearcherStub{})
			},
			readonlyQuery: func(*sql.DB, config.SQLServerConfig, *gorm.DB, *zap.Logger) (tool.BaseTool, error) {
				return agent.NewExecuteReadonlyQueryTool(executor)
			},
		},
	)
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	defer runtime.close()
	if runtime.conversation == nil {
		t.Fatal("conversation runtime is nil")
	}

	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	message := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "查询工单实时状态",
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	response, err := runtime.conversation.Respond(ctx, conversation.AgentRequest{
		Conversation: conversation.Conversation{ID: conversationID, UserID: userID, Status: conversation.StatusActive},
		UserMessage:  message, History: []conversation.Message{message},
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if !strings.Contains(response.Content, "处理中") {
		t.Fatalf("answer must come from the executed query: %q", response.Content)
	}
	calls, gotID, sqlRead, grants := executor.snapshot()
	if calls != 1 || gotID != sqlDataSourceID || !sqlRead ||
		!slices.Contains(grants, sqlDataSourceID) {
		t.Fatalf("executor observed calls=%d id=%s sqlRead=%v grants=%v", calls, gotID, sqlRead, grants)
	}
}
