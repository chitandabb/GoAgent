package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type stubAgentChatModel struct{}

func (stubAgentChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (stubAgentChatModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (
	*schema.StreamReader[*schema.Message], error,
) {
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
		context.Background(), testAgentConfig(), stubAgentExternalCases{}, zap.NewNop(), agentRuntimeBuilders{
			loadSkills: func(string) ([]mesagent.SkillDefinition, error) { return testAgentSkills(), nil },
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

func TestBuildAgentRuntimeRejectsInvalidSkillPackage(t *testing.T) {
	want := errors.New("invalid skill package")
	_, err := buildAgentRuntime(
		context.Background(), testAgentConfig(), stubAgentExternalCases{}, zap.NewNop(), agentRuntimeBuilders{
			loadSkills: func(string) ([]mesagent.SkillDefinition, error) { return nil, want },
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildAgentRuntimeDegradesWhenGitHubMCPIsUnavailable(t *testing.T) {
	cfg := testAgentConfig()
	cfg.GitHubMCP.Enabled = true
	runtime, err := buildAgentRuntime(
		context.Background(), cfg, stubAgentExternalCases{}, zap.NewNop(), agentRuntimeBuilders{
			loadSkills: func(string) ([]mesagent.SkillDefinition, error) { return testAgentSkills(), nil },
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
	if runtime.runner == nil {
		t.Fatal("ticket diagnosis runner was not initialized")
	}
	_, err = runtime.runner.Invoke(context.Background(), mesagent.RunRequest{UserQuery: "搜索代码提交"})
	if !errors.Is(err, mesagent.ErrSkillUnavailable) {
		t.Fatalf("code investigation error = %v", err)
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

func testAgentConfig() config.Config {
	return config.Config{
		Agent:  config.AgentConfig{SkillsDirectory: "skills"},
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{Enabled: true}},
	}
}

func testAgentSkills() []mesagent.SkillDefinition {
	budget := mesagent.ContextBudget{
		MaxContextTokens: 1000, ReservedOutputTokens: 100,
		MaxEvidenceTokens: 300, MaxToolResultTokens: 200, MaxToolResultBytes: 1024,
	}
	return []mesagent.SkillDefinition{
		{
			ID: mesagent.SkillTicketDiagnosis, Version: "test-v1", Description: "ticket",
			SystemPrompt: "diagnose", AllowedTools: []string{mesagent.ToolReadExternalCase},
			Budget: budget, MaxSteps: 4, Timeout: time.Second,
		},
		{
			ID: mesagent.SkillCodeInvestigation, Version: "test-v1", Description: "code",
			SystemPrompt: "investigate", AllowedTools: append([]string(nil), mesagent.GitHubReadOnlyTools...),
			Budget: budget, MaxSteps: 4, Timeout: time.Second,
		},
	}
}
