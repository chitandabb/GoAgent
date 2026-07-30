package main

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type probeModel struct {
	tools []*schema.ToolInfo
}

func (m *probeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	message := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-1", Function: schema.FunctionCall{Name: m.tools[0].Name, Arguments: `{"value":"ok"}`},
	}})
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 4},
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
	return &probeModel{tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func TestProbeRequiresToolCallAndUsage(t *testing.T) {
	result, err := probe(context.Background(), &probeModel{}, "step-3.7-flash")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Tool != probeToolName || result.TotalTokens != 12 || result.CachedTokens != 4 {
		t.Fatalf("result = %+v", result)
	}
}
