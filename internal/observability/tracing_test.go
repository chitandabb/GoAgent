package observability

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/resilience"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAgentRunCarriesRuntimeIdentityWithoutRawPayload(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	ctx := resilience.WithRunIdentity(context.Background(), resilience.RunIdentity{
		RunID: "run-1", ConversationID: "conversation-1", TaskID: "task-1",
	})
	ctx, span := StartAgentRun(ctx, "conversation")
	ctx = BindTraceIdentity(ctx)
	End(span, nil)

	identity, ok := resilience.RunIdentityFromContext(ctx)
	if !ok || identity.TraceID == "" {
		t.Fatal("expected trace identity to be bound to the run")
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "agent.conversation" {
		t.Fatalf("unexpected spans: %#v", spans)
	}
	attributes := spanAttributes(spans[0])
	for key, want := range map[string]string{
		"mesguard.operation.type":  "agent",
		"mesguard.run.id":          "run-1",
		"mesguard.conversation.id": "conversation-1",
		"mesguard.task.id":         "task-1",
	} {
		if got := attributes[key]; got != want {
			t.Fatalf("attribute %s = %q, want %q", key, got, want)
		}
	}
	if _, exists := attributes["mesguard.prompt"]; exists {
		t.Fatal("raw prompt must not be captured")
	}
}

func TestCanceledOperationIsNotReportedAsSuccessful(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	_, span := StartAgentRun(context.Background(), "canceled")
	End(span, context.Canceled)
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Error {
		t.Fatalf("canceled span status = %#v, want Error", spans)
	}
}

func TestChildSpansAndDegradationShareTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	ctx := resilience.WithRunIdentity(context.Background(), resilience.RunIdentity{RunID: "run-2"})
	ctx, root := StartAgentRun(ctx, "diagnosis")
	ctx = BindTraceIdentity(ctx)
	modelCtx, modelSpan := StartModelCall(ctx, "chat", "stepfun", "step-3.5-flash")
	End(modelSpan, nil)
	toolCtx, toolSpan := StartToolCall(modelCtx, "search_knowledge")
	RecordDegradation(toolCtx, resilience.DegradationEvent{
		Operation: "rerank", Policy: resilience.PolicyBestEffort, Fallback: "retrieval_order",
		ReasonCode: "provider_error", RunID: "run-2",
	})
	End(toolSpan, errors.New("tool failed"))
	End(root, nil)

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("span count = %d, want 3", len(spans))
	}
	if spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() ||
		spans[1].SpanContext.TraceID() != spans[2].SpanContext.TraceID() {
		t.Fatal("expected one trace for agent, model, and tool spans")
	}
	if !hasEvent(spans[1], "mesguard.degradation") {
		t.Fatalf("unexpected tool events: %#v", spans[1].Events)
	}
}

func hasEvent(span tracetest.SpanStub, name string) bool {
	for _, event := range span.Events {
		if event.Name == name {
			return true
		}
	}
	return false
}

func spanAttributes(span tracetest.SpanStub) map[string]string {
	values := make(map[string]string, len(span.Attributes))
	for _, item := range span.Attributes {
		values[string(item.Key)] = item.Value.AsString()
	}
	return values
}
