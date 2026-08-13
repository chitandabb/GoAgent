package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/chitandabb/GoAgent/internal/observability"

	"github.com/cloudwego/eino/compose"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const toolResultTruncationPrefix = "[tool result truncated by MESGuard"

func newToolObservabilityMiddleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			name := "unknown"
			if input != nil {
				name = input.Name
			}
			ctx, span := observability.StartToolCall(ctx, name)
			if input != nil && strings.TrimSpace(input.CallID) != "" {
				span.SetAttributes(attribute.String("mesguard.tool_call.id", strings.TrimSpace(input.CallID)))
			}
			output, err := next(ctx, input)
			observation := toolResultObservation{Status: "success"}
			if output != nil {
				observation = inspectToolResult(output.Result)
				span.SetAttributes(
					attribute.Int("mesguard.tool.result_bytes", len(output.Result)),
					attribute.Bool("mesguard.tool.result_truncated", observation.Truncated),
					attribute.Bool("mesguard.tool.degraded", observation.Degraded),
					attribute.String("mesguard.tool.result_status", observation.Status),
					attribute.String("mesguard.tool.reason_code", observation.ReasonCode),
				)
			}
			if observation.Degraded && err == nil {
				span.SetStatus(codes.Error, "tool degraded")
				span.End()
			} else {
				observability.End(span, err)
			}
			return output, err
		}
	}}
}

type toolResultObservation struct {
	Status     string
	ReasonCode string
	Degraded   bool
	Truncated  bool
}

func inspectToolResult(result string) toolResultObservation {
	observation := toolResultObservation{Status: "success"}
	if strings.Contains(result, toolResultTruncationPrefix) {
		observation.Status = "degraded"
		observation.Degraded = true
		observation.Truncated = true
	}
	var envelope struct {
		Degraded     bool              `json:"degraded"`
		Error        string            `json:"error"`
		ReasonCode   string            `json:"reasonCode"`
		Truncated    bool              `json:"truncated"`
		Degradations []json.RawMessage `json:"degradations"`
	}
	if json.Unmarshal([]byte(result), &envelope) != nil {
		return observation
	}
	observation.Truncated = observation.Truncated || envelope.Truncated
	observation.Degraded = observation.Degraded || envelope.Degraded || envelope.Truncated || len(envelope.Degradations) > 0
	observation.ReasonCode = strings.TrimSpace(envelope.ReasonCode)
	if strings.TrimSpace(envelope.Error) != "" {
		observation.Degraded = true
		if envelope.Error == "tool_call_rejected" {
			observation.Status = "rejected"
		} else {
			observation.Status = "degraded"
		}
	} else if observation.Degraded {
		observation.Status = "degraded"
	}
	return observation
}
