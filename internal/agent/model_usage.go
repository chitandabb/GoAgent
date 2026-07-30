package agent

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
)

// ModelUsage 汇总一次 Skill 执行中的全部 ChatModel 调用。
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
	mu    sync.Mutex
	usage ModelUsage
}

func (t *modelUsageTrace) append(output callbacks.CallbackOutput) {
	if t == nil {
		return
	}
	modelOutput := model.ConvCallbackOutput(output)
	if modelOutput == nil || modelOutput.Message == nil || modelOutput.Message.ResponseMeta == nil ||
		modelOutput.Message.ResponseMeta.Usage == nil {
		return
	}
	usage := modelOutput.Message.ResponseMeta.Usage
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.ModelCalls++
	t.usage.PromptTokens += usage.PromptTokens
	t.usage.CompletionTokens += usage.CompletionTokens
	t.usage.TotalTokens += usage.TotalTokens
	t.usage.CachedTokens += usage.PromptTokenDetails.CachedTokens
	t.usage.ReasoningTokens += usage.CompletionTokensDetails.ReasoningTokens
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
		Build()
}
