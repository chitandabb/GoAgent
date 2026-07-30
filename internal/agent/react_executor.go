package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	flowagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type ArgumentRewriter func(ctx context.Context, toolName, arguments string) (string, error)

type executionTrace struct {
	mu      sync.Mutex
	entries []ToolExecution
}

type traceContextKey struct{}

func withExecutionTrace(ctx context.Context, trace *executionTrace) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func traceFromContext(ctx context.Context) *executionTrace {
	trace, _ := ctx.Value(traceContextKey{}).(*executionTrace)
	return trace
}

func (t *executionTrace) append(entry ToolExecution) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = append(t.entries, entry)
}

func (t *executionTrace) snapshot() []ToolExecution {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ToolExecution(nil), t.entries...)
}

// ReActExecutor 是一个已经按 Skill 预编译好的执行器。
// 构造时只把该 Skill 的 AllowedTools 绑定给模型，从根源上减少无关 Tool Schema。
type ReActExecutor struct {
	definition SkillDefinition
	agent      *react.Agent
	log        *zap.Logger
}

func NewReActExecutor(
	ctx context.Context,
	definition SkillDefinition,
	chatModel model.ToolCallingChatModel,
	tools *ToolRegistry,
	rewrite ArgumentRewriter,
	log *zap.Logger,
) (*ReActExecutor, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	if chatModel == nil || tools == nil || log == nil {
		return nil, errors.New("react executor model, tool registry, and logger are required")
	}
	allowedTools, err := tools.Resolve(definition.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("resolve skill %q tools: %w", definition.ID, err)
	}

	toolMiddleware := compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				startedAt := time.Now()
				output, callErr := next(ctx, input)
				if output != nil && len(output.Result) > definition.Budget.MaxToolResultBytes {
					output.Result = strings.ToValidUTF8(
						output.Result[:definition.Budget.MaxToolResultBytes], "�",
					) + "\n[tool result truncated by MESGuard]"
				}
				entry := ToolExecution{
					Name: input.Name, DurationMS: time.Since(startedAt).Milliseconds(), Succeeded: callErr == nil,
				}
				if callErr != nil {
					// 对外轨迹只保留稳定错误类别，原始错误由上层 Zap 统一记录。
					entry.Error = "tool execution failed"
				}
				traceFromContext(ctx).append(entry)
				return output, callErr
			}
		},
	}

	agentInstance, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: allowedTools,
			UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
				return "", fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
			},
			ToolArgumentsHandler: rewrite,
			ToolCallMiddlewares:  []compose.ToolMiddleware{toolMiddleware},
		},
		MaxStep:   definition.MaxSteps,
		GraphName: "mesguard_" + string(definition.ID),
	})
	if err != nil {
		return nil, fmt.Errorf("build skill %q react agent: %w", definition.ID, err)
	}
	return &ReActExecutor{definition: definition, agent: agentInstance, log: log.Named(string(definition.ID))}, nil
}

func (e *ReActExecutor) Execute(ctx context.Context, request RunRequest, _ SkillDefinition) (RunResult, error) {
	if e == nil || e.agent == nil {
		return RunResult{}, errors.New("react executor is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, e.definition.Timeout)
	defer cancel()
	trace := &executionTrace{}
	ctx = withExecutionTrace(ctx, trace)
	handoff := &handoffTrace{}
	ctx = withHandoffTrace(ctx, handoff)
	usageTrace := &modelUsageTrace{}
	usageHandler := newModelUsageHandler(usageTrace)

	userPrompt, err := BuildUserPrompt(request)
	if err != nil {
		return RunResult{}, fmt.Errorf("build user prompt: %w", err)
	}
	startedAt := time.Now()
	message, err := e.agent.Generate(ctx, []*schema.Message{
		schema.SystemMessage(e.definition.SystemPrompt),
		schema.UserMessage(userPrompt),
	}, flowagent.WithComposeOptions(compose.WithCallbacks(usageHandler)))
	if err != nil {
		usage := usageTrace.snapshot()
		e.log.Warn("skill execution failed",
			zap.String("skill_version", e.definition.Version),
			zap.Duration("duration", time.Since(startedAt)),
			zap.Int("model_calls", usage.ModelCalls),
			zap.Int("total_tokens", usage.TotalTokens),
			zap.Error(err),
		)
		return RunResult{}, fmt.Errorf("execute skill %q: %w", e.definition.ID, err)
	}
	usage := usageTrace.snapshot()
	e.log.Info("skill execution completed",
		zap.String("skill_version", e.definition.Version),
		zap.Duration("duration", time.Since(startedAt)),
		zap.Int("tool_calls", len(trace.snapshot())),
		zap.Int("model_calls", usage.ModelCalls),
		zap.Int("total_tokens", usage.TotalTokens),
	)
	return RunResult{
		Answer: message.Content, ToolExecutions: trace.snapshot(), Usage: usage, Handoff: handoff.snapshot(),
	}, nil
}
