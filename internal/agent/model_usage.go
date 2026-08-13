package agent

import (
	"context"
	"io"
	"sync"

	"github.com/chitandabb/GoAgent/internal/observability"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ModelUsage 汇总一次 Agent 执行中的全部 ChatModel 调用。
type ModelUsage struct {
	ModelCalls       int `json:"modelCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
}

func (u *ModelUsage) Add(other ModelUsage) {
	if u == nil {
		return
	}
	u.ModelCalls += other.ModelCalls
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CachedTokens += other.CachedTokens
	u.ReasoningTokens += other.ReasoningTokens
}

type modelUsageTrace struct {
	mu         sync.Mutex
	usage      ModelUsage
	initial    ModelUsage
	hasInitial bool
	onUsage    func(ModelUsage)
}

func (t *modelUsageTrace) append(output callbacks.CallbackOutput) {
	if t == nil {
		return
	}
	modelOutput := model.ConvCallbackOutput(output)
	if modelOutput == nil {
		return
	}
	if modelOutput.TokenUsage != nil {
		t.appendValues(
			modelOutput.TokenUsage.PromptTokens,
			modelOutput.TokenUsage.CompletionTokens,
			modelOutput.TokenUsage.TotalTokens,
			modelOutput.TokenUsage.PromptTokenDetails.CachedTokens,
			modelOutput.TokenUsage.CompletionTokensDetails.ReasoningTokens,
		)
		return
	}
	if modelOutput.Message == nil || modelOutput.Message.ResponseMeta == nil ||
		modelOutput.Message.ResponseMeta.Usage == nil {
		return
	}
	usage := modelOutput.Message.ResponseMeta.Usage
	t.appendValues(
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		usage.PromptTokenDetails.CachedTokens,
		usage.CompletionTokensDetails.ReasoningTokens,
	)
}

func (t *modelUsageTrace) appendValues(prompt, completion, total, cached, reasoning int) {
	if t == nil {
		return
	}
	delta := ModelUsage{
		ModelCalls: 1, PromptTokens: prompt, CompletionTokens: completion,
		TotalTokens: total, CachedTokens: cached, ReasoningTokens: reasoning,
	}
	t.addUsage(delta, true)
}

func (t *modelUsageTrace) appendUsage(delta ModelUsage) {
	t.addUsage(delta, false)
}

func (t *modelUsageTrace) addUsage(delta ModelUsage, initialUsageCandidate bool) {
	if t == nil || delta.ModelCalls == 0 && delta.TotalTokens == 0 {
		return
	}
	t.mu.Lock()
	t.usage.Add(delta)
	if initialUsageCandidate && !t.hasInitial {
		t.initial = delta
		t.hasInitial = true
	}
	onUsage := t.onUsage
	t.mu.Unlock()
	if onUsage != nil {
		onUsage(delta)
	}
}

func (t *modelUsageTrace) initialSnapshot() (ModelUsage, bool) {
	if t == nil {
		return ModelUsage{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.initial, t.hasInitial
}

func (t *modelUsageTrace) snapshot() ModelUsage {
	if t == nil {
		return ModelUsage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

func newModelUsageHandler(trace *modelUsageTrace) callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel {
				return ctx
			}
			trace.append(output)
			return ctx
		}).
		OnEndWithStreamOutputFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			output *schema.StreamReader[callbacks.CallbackOutput],
		) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel || output == nil {
				return ctx
			}
			defer output.Close()
			// 流式 usage 通常只出现在最后一个分片；只记最后一份，避免把同一次模型调用重复累计。
			var last callbacks.CallbackOutput
			for {
				item, err := output.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return ctx
				}
				modelOutput := model.ConvCallbackOutput(item)
				if modelOutput == nil {
					continue
				}
				if modelOutput.TokenUsage != nil ||
					(modelOutput.Message != nil && modelOutput.Message.ResponseMeta != nil &&
						modelOutput.Message.ResponseMeta.Usage != nil) {
					last = item
				}
			}
			if last != nil {
				trace.append(last)
			}
			return ctx
		}).
		Build()
}

type modelSpanContextKey struct{}

type modelSpanState struct {
	span trace.Span
	once sync.Once
}

func newModelTracingHandler(provider, modelID string) callbacks.Handler {
	finish := func(ctx context.Context, err error, output callbacks.CallbackOutput) context.Context {
		state, _ := ctx.Value(modelSpanContextKey{}).(*modelSpanState)
		if state == nil {
			return ctx
		}
		state.once.Do(func() {
			if usage := modelUsageFromOutput(output); usage != nil {
				state.span.SetAttributes(
					attribute.Int("gen_ai.usage.input_tokens", usage.PromptTokens),
					attribute.Int("gen_ai.usage.output_tokens", usage.CompletionTokens),
					attribute.Int("mesguard.model.cached_tokens", usage.CachedTokens),
					attribute.Int("mesguard.model.reasoning_tokens", usage.ReasoningTokens),
				)
			}
			observability.End(state.span, err)
		})
		return ctx
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel {
				return ctx
			}
			operation := info.Name
			if operation == "" {
				operation = "chat"
			}
			ctx, span := observability.StartModelCall(ctx, operation, provider, modelID)
			return context.WithValue(ctx, modelSpanContextKey{}, &modelSpanState{span: span})
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel {
				return ctx
			}
			return finish(ctx, nil, output)
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel {
				return ctx
			}
			return finish(ctx, err, nil)
		}).
		OnEndWithStreamOutputFn(func(
			ctx context.Context,
			info *callbacks.RunInfo,
			output *schema.StreamReader[callbacks.CallbackOutput],
		) context.Context {
			if info == nil || info.Component != components.ComponentOfChatModel || output == nil {
				return ctx
			}
			defer output.Close()
			var last callbacks.CallbackOutput
			for {
				item, err := output.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return finish(ctx, err, nil)
				}
				if modelUsageFromOutput(item) != nil {
					last = item
				}
			}
			return finish(ctx, nil, last)
		}).
		Build()
}

func modelUsageFromOutput(output callbacks.CallbackOutput) *ModelUsage {
	modelOutput := model.ConvCallbackOutput(output)
	if modelOutput == nil {
		return nil
	}
	if usage := modelOutput.TokenUsage; usage != nil {
		return &ModelUsage{
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
			ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
		}
	}
	if modelOutput.Message == nil || modelOutput.Message.ResponseMeta == nil ||
		modelOutput.Message.ResponseMeta.Usage == nil {
		return nil
	}
	usage := modelOutput.Message.ResponseMeta.Usage
	return &ModelUsage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
}
