package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type stubAgentChatModel struct{}

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
		PreflightTimeoutMillis: 250,
		SoftThresholdRatio:     0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 256,
	}
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
	if response.RunObservation == nil || response.RunObservation.PromptManifest == nil {
		t.Fatalf("shadow manifest was not wired: %+v", response.RunObservation)
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

func TestBuildAgentRuntimeRegistersOrDegradesWebResearchAsOneDependency(t *testing.T) {
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
	if runtime.webResearch == nil || !slices.Contains(runtime.availableDependencies, agent.ToolDependencyWebSearch) {
		t.Fatalf("web research dependency was not registered: %+v", runtime.availableDependencies)
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
	if degraded.webResearch != nil || slices.Contains(degraded.availableDependencies, agent.ToolDependencyWebSearch) {
		t.Fatalf("unavailable web research was exposed: %+v", degraded.availableDependencies)
	}
}

func TestBuildAgentRuntimeRegistersSQLToolWhenSQLServerIsAvailable(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
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
