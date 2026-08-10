package agent

import (
	"context"
	"io"
	"sync"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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
	mu      sync.Mutex
	usage   ModelUsage
	onUsage func(ModelUsage)
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
	t.appendUsage(delta)
}

func (t *modelUsageTrace) appendUsage(delta ModelUsage) {
	if t == nil || delta.ModelCalls == 0 && delta.TotalTokens == 0 {
		return
	}
	t.mu.Lock()
	t.usage.Add(delta)
	onUsage := t.onUsage
	t.mu.Unlock()
	if onUsage != nil {
		onUsage(delta)
	}
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
