package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestModelUsageHandlerAggregatesNonStreamingAndStreamingUsage(t *testing.T) {
	trace := &modelUsageTrace{}
	handler := newModelUsageHandler(trace)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	handler.OnEnd(context.Background(), info, usageCallbackOutput(10, 2, 12, 3, 1))

	stream := schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&model.CallbackOutput{Message: schema.AssistantMessage("partial", nil)},
		usageCallbackOutput(20, 4, 24, 5, 2),
	})
	handler.OnEndWithStreamOutput(context.Background(), info, stream)

	if got := trace.snapshot(); got != (ModelUsage{
		ModelCalls: 2, PromptTokens: 30, CompletionTokens: 6,
		TotalTokens: 36, CachedTokens: 8, ReasoningTokens: 3,
	}) {
		t.Fatalf("usage = %+v", got)
	}
	trace.appendUsage(ModelUsage{ModelCalls: 1, PromptTokens: 6, CompletionTokens: 2, TotalTokens: 8})
	if initial, available := trace.initialSnapshot(); !available || initial != (ModelUsage{
		ModelCalls: 1, PromptTokens: 10, CompletionTokens: 2,
		TotalTokens: 12, CachedTokens: 3, ReasoningTokens: 1,
	}) {
		t.Fatalf("initial provider usage = %+v, available=%v", initial, available)
	}
}

func usageCallbackOutput(prompt, completion, total, cached, reasoning int) *model.CallbackOutput {
	return &model.CallbackOutput{TokenUsage: &model.TokenUsage{
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total,
		PromptTokenDetails:      model.PromptTokenDetails{CachedTokens: cached},
		CompletionTokensDetails: model.CompletionTokensDetails{ReasoningTokens: reasoning},
	}}
}
