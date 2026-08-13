package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

func TestModelTracingHandlerRecordsIdentityAndUsageWithoutPayload(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	handler := newModelTracingHandler("stepfun", "step-3.5-flash")
	info := &callbacks.RunInfo{Name: "main_chat", Component: components.ComponentOfChatModel}
	ctx := handler.OnStart(context.Background(), info, schema.UserMessage("sensitive prompt"))
	handler.OnEnd(ctx, info, usageCallbackOutput(10, 2, 12, 3, 1))

	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "model.main_chat" {
		t.Fatalf("unexpected spans: %#v", spans)
	}
	attributes := make(map[string]string, len(spans[0].Attributes))
	for _, item := range spans[0].Attributes {
		attributes[string(item.Key)] = item.Value.Emit()
	}
	for key, want := range map[string]string{
		"gen_ai.provider.name":       "stepfun",
		"gen_ai.request.model":       "step-3.5-flash",
		"gen_ai.usage.input_tokens":  "10",
		"gen_ai.usage.output_tokens": "2",
	} {
		if got := attributes[key]; got != want {
			t.Fatalf("attribute %s = %q, want %q", key, got, want)
		}
	}
	for _, item := range spans[0].Attributes {
		if item.Value.Emit() == "sensitive prompt" {
			t.Fatal("model span captured raw prompt")
		}
	}
}

func usageCallbackOutput(prompt, completion, total, cached, reasoning int) *model.CallbackOutput {
	return &model.CallbackOutput{TokenUsage: &model.TokenUsage{
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total,
		PromptTokenDetails:      model.PromptTokenDetails{CachedTokens: cached},
		CompletionTokensDetails: model.CompletionTokensDetails{ReasoningTokens: reasoning},
	}}
}
