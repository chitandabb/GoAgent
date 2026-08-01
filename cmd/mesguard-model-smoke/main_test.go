package main

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type probeModel struct {
	tools []*schema.ToolInfo
	calls int
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
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 4},
		CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1},
	}}
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
	if result.Usage.TotalTokens != 24 || result.Usage.CachedTokens != 8 || result.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Calls[0].Reasoning == "" || result.Calls[1].Content == "" {
		t.Fatalf("calls = %+v", result.Calls)
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
}
