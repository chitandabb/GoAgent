package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/google/uuid"
)

func TestBoundedQualitySearchToolExecutesOncePerUserMessage(t *testing.T) {
	calls := 0
	inner, err := utils.InferTool("search_knowledge", "test knowledge search", func(context.Context, struct {
		Query      string `json:"query"`
		MaxResults int    `json:"maxResults"`
	}) (map[string]any, error) {
		calls++
		return map[string]any{"query": "first", "results": []any{map[string]any{"chunkId": "chunk-1"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := newBoundedQualitySearchTool(inner, 3)
	if err != nil {
		t.Fatal(err)
	}
	messageID := uuid.New()
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: messageID,
		Actor: conversation.Actor{UserID: uuid.New()},
	})

	first := invokeQualitySearch(t, current, ctx, `{"query":"first"}`)
	second := invokeQualitySearch(t, current, ctx, `{"query":"different"}`)
	if calls != 1 {
		t.Fatalf("underlying calls = %d, want 1", calls)
	}
	if first["evaluationCached"] != false || second["evaluationCached"] != true {
		t.Fatalf("first=%v second=%v", first, second)
	}
	if second["query"] != "first" || second["evaluationNotice"] != qualityCachedSearchNotice {
		t.Fatalf("cached result = %v", second)
	}
	if results, ok := second["results"].([]any); !ok || len(results) != 1 || second["contextExpanded"] != false {
		t.Fatalf("cached result was not compacted: %v", second)
	}

	otherContext := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(),
		Actor: conversation.Actor{UserID: uuid.New()},
	})
	_ = invokeQualitySearch(t, current, otherContext, `{"query":"other"}`)
	if calls != 2 {
		t.Fatalf("underlying calls across messages = %d, want 2", calls)
	}
}

func TestBoundedQualitySearchToolRequiresMessageContext(t *testing.T) {
	inner, err := utils.InferTool("search_knowledge", "test knowledge search", func(context.Context, struct{}) (string, error) {
		return `{}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := newBoundedQualitySearchTool(inner, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = current.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("bounded quality search accepted a missing message context")
	}
}

func TestBoundedQualitySearchToolPinsCaseRetrievalLimit(t *testing.T) {
	gotLimit := 0
	inner, err := utils.InferTool("search_knowledge", "test knowledge search", func(_ context.Context, input struct {
		Query      string `json:"query"`
		MaxResults int    `json:"maxResults"`
	}) (map[string]any, error) {
		gotLimit = input.MaxResults
		return map[string]any{"query": input.Query, "results": []any{map[string]any{"chunkId": "chunk-1"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := newBoundedQualitySearchTool(inner, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(),
		Actor: conversation.Actor{UserID: uuid.New()},
	})
	_ = invokeQualitySearch(t, current, ctx, `{"query":"pool","maxResults":20}`)
	if gotLimit != 3 {
		t.Fatalf("underlying maxResults = %d, want fixed case K=3", gotLimit)
	}
}

func invokeQualitySearch(t *testing.T, current tool.InvokableTool, ctx context.Context, arguments string) map[string]any {
	t.Helper()
	result, err := current.InvokableRun(ctx, arguments)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return payload
}
