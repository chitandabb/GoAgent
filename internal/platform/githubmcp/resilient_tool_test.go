package githubmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type retryTestTool struct {
	results []string
	calls   int
}

func (t *retryTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: codeSearchToolName}, nil
}

func (t *retryTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls++
	index := t.calls - 1
	if index >= len(t.results) {
		index = len(t.results) - 1
	}
	return t.results[index], nil
}

func incompleteSearchResponse() string {
	return `{"content":[{"type":"text","text":"{\"incomplete_results\":true,\"items\":[],\"total_count\":0}"}]}`
}

func completeSearchResponse() string {
	return `{"content":[{"type":"text","text":"{\"incomplete_results\":false,\"items\":[{\"path\":\"src/TicketQuery.cs\"}],\"total_count\":1}"}]}`
}

func TestRetryingToolRetriesIncompleteCodeSearch(t *testing.T) {
	inner := &retryTestTool{results: []string{incompleteSearchResponse(), incompleteSearchResponse(), completeSearchResponse()}}
	wrapped := &retryingTool{
		inner:  inner,
		policy: retryPolicy{maxAttempts: 3, delays: []time.Duration{0, 0}, sleep: func(context.Context, time.Duration) error { return nil }},
	}

	result, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if inner.calls != 3 {
		t.Fatalf("calls = %d, want 3", inner.calls)
	}
	if result != completeSearchResponse() {
		t.Fatalf("result = %s, want final complete response", result)
	}
}

func TestRetryingToolReturnsIndexPendingAfterBoundedRetries(t *testing.T) {
	inner := &retryTestTool{results: []string{incompleteSearchResponse()}}
	wrapped := &retryingTool{
		inner:  inner,
		policy: retryPolicy{maxAttempts: 3, delays: []time.Duration{0, 0}, sleep: func(context.Context, time.Duration) error { return nil }},
	}

	result, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if inner.calls != 3 {
		t.Fatalf("calls = %d, want 3", inner.calls)
	}
	var status struct {
		Status            string `json:"status"`
		Attempts          int    `json:"attempts"`
		IncompleteResults bool   `json:"incomplete_results"`
		Message           string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &status); err != nil {
		t.Fatalf("decode status result: %v; result=%s", err, result)
	}
	if status.Status != codeSearchIndexPendingResultStatus || status.Attempts != 3 || !status.IncompleteResults {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Message, "not treat this as no matches") {
		t.Fatalf("message does not preserve no-match warning: %q", status.Message)
	}
}

func TestWrapCodeSearchToolLeavesOtherToolsUnchanged(t *testing.T) {
	inner := &retryTestTool{results: []string{completeSearchResponse()}}
	wrapped, err := wrapCodeSearchTool(context.Background(), inner)
	if err != nil {
		t.Fatalf("wrap search_code: %v", err)
	}
	if wrapped == inner {
		t.Fatal("search_code was not wrapped")
	}

	other := &namedRetryTestTool{}
	unchanged, err := wrapCodeSearchTool(context.Background(), other)
	if err != nil {
		t.Fatalf("wrap other tool: %v", err)
	}
	if unchanged != other {
		t.Fatal("non-search tool was unexpectedly wrapped")
	}
}

func TestRetryingToolRejectsMalformedSearchResponse(t *testing.T) {
	inner := &retryTestTool{results: []string{"not-json"}}
	wrapped := &retryingTool{
		inner:  inner,
		policy: retryPolicy{maxAttempts: 1, sleep: func(context.Context, time.Duration) error { return nil }},
	}

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("error = %v, want malformed response error", err)
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1", inner.calls)
	}
}

func TestRetryPolicyRejectsInsufficientOrNegativeDelays(t *testing.T) {
	for _, policy := range []retryPolicy{
		{maxAttempts: 3, delays: []time.Duration{time.Second}, sleep: func(context.Context, time.Duration) error { return nil }},
		{maxAttempts: 2, delays: []time.Duration{-time.Second}, sleep: func(context.Context, time.Duration) error { return nil }},
	} {
		if err := policy.validate(); err == nil {
			t.Fatalf("policy was accepted: %+v", policy)
		}
	}
}

func TestRetryingToolInfoGuardsNilReceiver(t *testing.T) {
	var wrapped *retryingTool
	if _, err := wrapped.Info(context.Background()); err == nil || !strings.Contains(err.Error(), "tool is nil") {
		t.Fatalf("Info error = %v", err)
	}
}

type namedRetryTestTool struct{}

func (*namedRetryTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "get_file_contents"}, nil
}
