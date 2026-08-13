package observability

import (
	"context"
	"strings"

	"github.com/chitandabb/GoAgent/internal/resilience"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/chitandabb/GoAgent"

func StartAgentRun(ctx context.Context, operation string) (context.Context, trace.Span) {
	return start(ctx, "agent."+normalizedName(operation), "agent")
}

func StartModelCall(ctx context.Context, operation, provider, model string) (context.Context, trace.Span) {
	ctx, span := start(ctx, "model."+normalizedName(operation), "model")
	span.SetAttributes(
		attribute.String("gen_ai.provider.name", strings.TrimSpace(provider)),
		attribute.String("gen_ai.request.model", strings.TrimSpace(model)),
	)
	return ctx, span
}

func StartToolCall(ctx context.Context, toolName string) (context.Context, trace.Span) {
	ctx, span := start(ctx, "tool."+normalizedName(toolName), "tool")
	span.SetAttributes(attribute.String("mesguard.tool.name", strings.TrimSpace(toolName)))
	return ctx, span
}

func StartRetrieval(ctx context.Context, operation string) (context.Context, trace.Span) {
	return start(ctx, "retrieval."+normalizedName(operation), "retrieval")
}

func start(ctx context.Context, spanName, operationType string) (context.Context, trace.Span) {
	ctx, span := otel.Tracer(instrumentationName).Start(ctx, spanName)
	attributes := []attribute.KeyValue{attribute.String("mesguard.operation.type", operationType)}
	if identity, ok := resilience.RunIdentityFromContext(ctx); ok {
		attributes = append(attributes, identityAttributes(identity)...)
	}
	span.SetAttributes(attributes...)
	return ctx, span
}

func BindTraceIdentity(ctx context.Context) context.Context {
	identity, ok := resilience.RunIdentityFromContext(ctx)
	spanContext := trace.SpanContextFromContext(ctx)
	if !ok || !spanContext.IsValid() {
		return ctx
	}
	identity.TraceID = spanContext.TraceID().String()
	return resilience.WithRunIdentity(ctx, identity)
}

func TraceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func End(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.SetStatus(codes.Error, "operation failed")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func RecordDegradation(ctx context.Context, event resilience.DegradationEvent) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddEvent("mesguard.degradation", trace.WithAttributes(
		attribute.String("mesguard.degradation.operation", event.Operation),
		attribute.String("mesguard.degradation.policy", string(event.Policy)),
		attribute.String("mesguard.degradation.fallback", event.Fallback),
		attribute.String("mesguard.degradation.reason_code", event.ReasonCode),
		attribute.String("gen_ai.provider.name", event.Provider),
		attribute.String("gen_ai.request.model", event.Model),
	))
}

func identityAttributes(identity resilience.RunIdentity) []attribute.KeyValue {
	attributes := []attribute.KeyValue{attribute.String("mesguard.run.id", identity.RunID)}
	if identity.ConversationID != "" {
		attributes = append(attributes, attribute.String("mesguard.conversation.id", identity.ConversationID))
	}
	if identity.TaskID != "" {
		attributes = append(attributes, attribute.String("mesguard.task.id", identity.TaskID))
	}
	return attributes
}

func normalizedName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	return strings.ReplaceAll(value, " ", "_")
}
