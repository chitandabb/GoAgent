package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	qualityFirstSearchNotice  = "本次质量观察只执行一次真实知识检索；请优先使用当前证据直接回答并标注完整来源，不要改写问题后重复检索。"
	qualityCachedSearchNotice = "本次质量观察已达到真实知识检索上限；以下为首次检索的缓存结果。请立即基于已有证据回答并标注完整来源，不要再次调用 search_knowledge。"
)

// boundedQualitySearchTool keeps the paid quality observation deterministic:
// each user message can execute one real knowledge search, while subsequent
// attempts receive the first valid result again. The production Conversation
// runner remains unchanged and can still perform multiple distinct searches.
type boundedQualitySearchTool struct {
	inner      tool.InvokableTool
	maxResults int

	mu      sync.Mutex
	results map[uuid.UUID]string
}

func newBoundedQualitySearchTool(inner tool.InvokableTool, maxResults int) (tool.InvokableTool, error) {
	if inner == nil || maxResults < 1 || maxResults > 20 {
		return nil, errors.New("quality search tool and bounded max results are required")
	}
	return &boundedQualitySearchTool{
		inner: inner, maxResults: maxResults, results: make(map[uuid.UUID]string, 8),
	}, nil
}

func (t *boundedQualitySearchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *boundedQualitySearchTool) InvokableRun(
	ctx context.Context,
	arguments string,
	opts ...tool.Option,
) (string, error) {
	commandContext, ok := conversation.CommandContextFromContext(ctx)
	if !ok || commandContext.UserMessageID == uuid.Nil {
		return "", errors.New("quality search requires a user message context")
	}

	t.mu.Lock()
	cached, exists := t.results[commandContext.UserMessageID]
	t.mu.Unlock()
	if exists {
		return decorateQualitySearchResult(cached, qualityCachedSearchNotice, true)
	}

	boundedArguments, err := boundQualitySearchArguments(arguments, t.maxResults)
	if err != nil {
		return "", err
	}
	result, err := t.inner.InvokableRun(ctx, boundedArguments, opts...)
	if err != nil {
		return "", err
	}
	decorated, err := decorateQualitySearchResult(result, qualityFirstSearchNotice, false)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.results[commandContext.UserMessageID] = result
	t.mu.Unlock()
	return decorated, nil
}

func boundQualitySearchArguments(arguments string, maxResults int) (string, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil || payload == nil {
		return "", errors.New("quality search arguments must be a JSON object")
	}
	payload["maxResults"] = json.RawMessage(strconv.Itoa(maxResults))
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", errors.New("encode bounded quality search arguments")
	}
	return string(encoded), nil
}

func decorateQualitySearchResult(result, notice string, cached bool) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil || payload == nil {
		return "", errors.New("quality search returned an invalid JSON object")
	}
	if cached {
		if err := compactCachedQualitySearchResult(payload); err != nil {
			return "", err
		}
	}
	payload["evaluationNotice"] = notice
	payload["evaluationCached"] = cached
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", errors.New("encode bounded quality search result")
	}
	return string(encoded), nil
}

func compactCachedQualitySearchResult(payload map[string]any) error {
	results, ok := payload["results"].([]any)
	if !ok || len(results) == 0 {
		return errors.New("quality search cache contains no result")
	}
	// The original Tool message remains in the ReAct context. Keep one complete,
	// evidence-valid result in the repeated response so citation validation still
	// succeeds without duplicating every retrieved Chunk and Parent window.
	payload["results"] = results[:1]
	payload["contextGroups"] = []any{}
	payload["contextExpanded"] = false
	payload["contextCompression"] = map[string]any{
		"enabled": false, "applied": false,
		"inputChunks": 0, "outputChunks": 0,
		"inputRunes": 0, "outputRunes": 0, "omittedChunks": 0,
	}
	payload["embeddingTotalTokens"] = 0
	payload["rerankTotalTokens"] = 0
	return nil
}
