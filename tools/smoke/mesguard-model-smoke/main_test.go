package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	provideropenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type probeModel struct {
	tools            []*schema.ToolInfo
	calls            int
	withoutUsage     bool
	missingUsageCall int
}

func (m *probeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.calls++
	var message *schema.Message
	if m.calls == 1 {
		message = schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-1", Function: schema.FunctionCall{Name: m.tools[0].Name, Arguments: `{"value":"ok"}`},
		}})
		message.ReasoningContent = "需要先调用探针工具。"
	} else {
		message = schema.AssistantMessage("模型连接正常。", nil)
		message.ReasoningContent = "工具返回 ok。"
	}
	if !m.withoutUsage && m.calls != m.missingUsageCall {
		message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 4},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1},
		}}
	}
	return message, nil
}

func (m *probeModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (
	*schema.StreamReader[*schema.Message], error,
) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *probeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = append([]*schema.ToolInfo(nil), tools...)
	return m, nil
}

func TestProbeRequiresToolCallAndUsage(t *testing.T) {
	result, err := probe(context.Background(), &probeModel{}, "step-3.7-flash")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Tool != probeToolName || result.Answer != "模型连接正常。" || len(result.Calls) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !result.Usage.Provided || result.Usage.TotalTokens != 24 || result.Usage.CachedTokens != 8 || result.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Calls[0].Reasoning == "" || result.Calls[1].Content == "" {
		t.Fatalf("calls = %+v", result.Calls)
	}
}

// TestProbeReportsMissingUsageAsNotProvided 证明 Provider 流未返回 usage 对象时，
// Smoke 必须把 usage 标记为"未提供"而不是包装成零消耗。
func TestProbeReportsMissingUsageAsNotProvided(t *testing.T) {
	result, err := probe(context.Background(), &probeModel{withoutUsage: true}, "step-3.7-flash")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Usage.Provided {
		t.Fatalf("usage must be marked not-provided, got %+v", result.Usage)
	}
}

func TestProbeReportsPartialUsageWithoutPresentingItAsComplete(t *testing.T) {
	result, err := probe(context.Background(), &probeModel{missingUsageCall: 1}, "step-3.7-flash")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !result.Usage.Provided || result.Usage.Complete {
		t.Fatalf("usage = %+v, want partial", result.Usage)
	}
	if result.Usage.ProvidedCalls != 1 || result.Usage.TotalCalls != 2 {
		t.Fatalf("usage call coverage = %d/%d, want 1/2", result.Usage.ProvidedCalls, result.Usage.TotalCalls)
	}
}

func TestParseRunOptions(t *testing.T) {
	opts, err := parseRunOptions([]string{"-reasoning-effort", "LOW", "-show-reasoning"}, "medium")
	if err != nil {
		t.Fatalf("parseRunOptions: %v", err)
	}
	if opts.ReasoningEffort != "low" || !opts.ShowReasoning {
		t.Fatalf("options = %+v", opts)
	}
	if _, err = parseRunOptions([]string{"-reasoning-effort", "off"}, "medium"); err == nil {
		t.Fatal("parseRunOptions accepted unsupported reasoning effort")
	}
	if opts, err = parseRunOptions(nil, ""); err != nil || opts.ReasoningEffort != "" {
		t.Fatalf("parseRunOptions empty effort = %+v, %v", opts, err)
	}
	if opts, err = parseRunOptions([]string{"-reasoning-effort", "max"}, ""); err != nil || opts.ReasoningEffort != "max" {
		t.Fatalf("parseRunOptions DeepSeek effort = %+v, %v", opts, err)
	}
	opts, err = parseRunOptions([]string{"-profile", "opencode-deepseek-main", "-allow-provider-calls"}, "")
	if err != nil {
		t.Fatalf("parseRunOptions profile: %v", err)
	}
	if opts.Profile != "opencode-deepseek-main" || !opts.AllowProviderCalls {
		t.Fatalf("options = %+v", opts)
	}
	opts, err = parseRunOptions(nil, "")
	if err != nil {
		t.Fatalf("parseRunOptions defaults: %v", err)
	}
	if opts.Profile != "" || opts.AllowProviderCalls {
		t.Fatalf("default options = %+v", opts)
	}
}

func smokeTestChatConfig() config.ChatModelConfig {
	stepfun := config.ChatModelProfileConfig{
		Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 120_000,
		ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
		PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
	opencode := config.ChatModelProfileConfig{
		Provider: "opencode-go", BaseURL: "https://opencode.ai/zen/go/v1",
		APIKeyEnv: "MESGUARD_OPENCODE_GO_API_KEY", Model: "deepseek-v4-flash",
		ResponseFormat: "text", TimeoutMillis: 120_000,
		ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
		PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
	return config.ChatModelConfig{
		Enabled: true, ActiveProfileName: "stepfun-main", ConversationMemoryProfileName: "stepfun-memory",
		Profiles: map[string]config.ChatModelProfileConfig{
			"stepfun-main": stepfun, "stepfun-memory": stepfun, "opencode-deepseek-main": opencode,
		},
	}
}

type recordingModelFactory struct {
	calls   int
	names   []string
	efforts []string
}

type failingProbeModel struct{ err error }

func (m *failingProbeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, m.err
}

func (m *failingProbeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, m.err
}

func (m *failingProbeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (f *recordingModelFactory) build(
	ctx context.Context,
	profileName string,
	profile config.ChatModelProfileConfig,
) (*chatmodel.Instance, error) {
	f.calls++
	f.names = append(f.names, profileName)
	f.efforts = append(f.efforts, profile.ReasoningEffort)
	return &chatmodel.Instance{
		Model:    &probeModel{},
		Identity: chatmodel.Identity{Profile: profileName, Provider: profile.Provider, ModelID: profile.Model},
	}, nil
}

func TestRunAppliesReasoningEffortOverrideToSelectedProfile(t *testing.T) {
	cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
	factory := &recordingModelFactory{}
	if err := runWith(context.Background(), []string{
		"-profile", "stepfun-main",
		"-reasoning-effort", "high",
		"-allow-provider-calls",
	}, cfg, factory.build); err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if len(factory.efforts) != 1 || factory.efforts[0] != "high" {
		t.Fatalf("factory efforts = %v, want high", factory.efforts)
	}
}

// TestRunRefusesProviderCallsWithoutAllowFlag 证明成本护栏先于 Provider 创建：
// 未传 -allow-provider-calls 时 Factory 一次都不允许被调用。
func TestRunRefusesProviderCallsWithoutAllowFlag(t *testing.T) {
	cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
	factory := &recordingModelFactory{}
	err := runWith(context.Background(), []string{"-profile", "opencode-deepseek-main"}, cfg, factory.build)
	if err == nil || !strings.Contains(err.Error(), "-allow-provider-calls") {
		t.Fatalf("error = %v, want -allow-provider-calls rejection", err)
	}
	if factory.calls != 0 {
		t.Fatalf("factory must not be called before the guard, calls = %d", factory.calls)
	}
}

func TestRunUsesSelectedProfile(t *testing.T) {
	cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
	factory := &recordingModelFactory{}
	if err := runWith(context.Background(), []string{"-profile", "opencode-deepseek-main", "-allow-provider-calls"}, cfg, factory.build); err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if len(factory.names) != 1 || factory.names[0] != "opencode-deepseek-main" {
		t.Fatalf("factory names = %v, want opencode-deepseek-main", factory.names)
	}
}

func TestRunDefaultsToActiveProfile(t *testing.T) {
	cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
	factory := &recordingModelFactory{}
	if err := runWith(context.Background(), []string{"-allow-provider-calls"}, cfg, factory.build); err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if len(factory.names) != 1 || factory.names[0] != "stepfun-main" {
		t.Fatalf("factory names = %v, want active stepfun-main", factory.names)
	}
}

func TestRunRejectsUnknownProfileBeforeFactory(t *testing.T) {
	cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
	factory := &recordingModelFactory{}
	err := runWith(context.Background(), []string{"-profile", "missing-profile", "-allow-provider-calls"}, cfg, factory.build)
	if err == nil {
		t.Fatal("runWith accepted an unknown profile")
	}
	if factory.calls != 0 {
		t.Fatalf("factory must not run for an unknown profile, calls = %d", factory.calls)
	}
}

func TestRunClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind string
	}{
		{name: "rate limited", err: &provideropenai.APIError{HTTPStatusCode: 429}, kind: "rate_limited"},
		{name: "timeout", err: context.DeadlineExceeded, kind: "timeout"},
		{name: "protocol", err: io.ErrUnexpectedEOF, kind: "protocol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Models: config.ModelsConfig{Chat: smokeTestChatConfig()}}
			factory := func(
				context.Context,
				string,
				config.ChatModelProfileConfig,
			) (*chatmodel.Instance, error) {
				return &chatmodel.Instance{
					Model: &failingProbeModel{err: tt.err},
					Identity: chatmodel.Identity{
						Profile: "opencode-deepseek-main", Provider: "opencode-go", ModelID: "deepseek-v4-flash",
					},
				}, nil
			}
			err := runWith(context.Background(), []string{
				"-profile", "opencode-deepseek-main", "-allow-provider-calls",
			}, cfg, factory)
			if err == nil || !strings.Contains(err.Error(), "provider_error_type="+tt.kind) {
				t.Fatalf("error = %v, want provider_error_type=%s", err, tt.kind)
			}
		})
	}
}
