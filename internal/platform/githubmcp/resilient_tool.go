package githubmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	codeSearchToolName       = "search_code"
	codeSearchMaxAttempts    = 3
	codeSearchRetryDelay     = 250 * time.Millisecond
	codeSearchRetryDelayStep = 500 * time.Millisecond
)

// codeSearchIndexPendingResultStatus is intentionally a machine-readable
// status. An incomplete GitHub response is not equivalent to an empty result.
const codeSearchIndexPendingResultStatus = "index_pending"

type retryPolicy struct {
	maxAttempts int
	delays      []time.Duration
	sleep       func(context.Context, time.Duration) error
}

func defaultCodeSearchRetryPolicy() retryPolicy {
	return retryPolicy{
		maxAttempts: codeSearchMaxAttempts,
		delays: []time.Duration{
			codeSearchRetryDelay,
			codeSearchRetryDelay + codeSearchRetryDelayStep,
		},
		sleep: sleepWithContext,
	}
}

func (p retryPolicy) validate() error {
	if p.maxAttempts < 1 {
		return errors.New("github code search retry max attempts must be positive")
	}
	if len(p.delays) < p.maxAttempts-1 {
		return fmt.Errorf("github code search retry delays must contain at least %d entries", p.maxAttempts-1)
	}
	for index, delay := range p.delays {
		if delay < 0 {
			return fmt.Errorf("github code search retry delay %d must not be negative", index)
		}
	}
	if p.sleep == nil {
		return errors.New("github code search retry sleep function is nil")
	}
	return nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryingTool preserves the Eino tool contract and adds behavior only to
// GitHub Code Search. Other GitHub tools remain single-attempt read-only calls.
type retryingTool struct {
	inner  tool.InvokableTool
	policy retryPolicy
}

func (t *retryingTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	if t == nil || t.inner == nil {
		return nil, errors.New("github code search tool is nil")
	}
	return t.inner.Info(ctx)
}

func (t *retryingTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	if t == nil || t.inner == nil {
		return "", errors.New("github code search tool is nil")
	}
	if err := t.policy.validate(); err != nil {
		return "", err
	}

	var lastResult string
	for attempt := 1; attempt <= t.policy.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result, err := t.inner.InvokableRun(ctx, arguments, opts...)
		if err != nil {
			return result, err
		}
		lastResult = result
		incomplete, inspectErr := codeSearchIndexIncomplete(result)
		if inspectErr != nil {
			return result, inspectErr
		}
		if !incomplete {
			return result, nil
		}
		if attempt == t.policy.maxAttempts {
			pending, pendingErr := newCodeSearchIndexPendingResult(lastResult, attempt)
			if pendingErr != nil {
				return "", pendingErr
			}
			return pending, nil
		}
		if err := t.policy.sleep(ctx, retryDelay(t.policy, attempt)); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("github code search retry loop ended unexpectedly")
}

func retryDelay(policy retryPolicy, completedAttempt int) time.Duration {
	index := completedAttempt - 1
	if index < 0 || index >= len(policy.delays) {
		return 0
	}
	return policy.delays[index]
}

func codeSearchIndexIncomplete(raw string) (bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("response is not a JSON object")
		}
		return false, fmt.Errorf("github code search response is not a JSON object: %w", err)
	}
	var envelope struct {
		Status            string `json:"status"`
		IncompleteResults bool   `json:"incomplete_results"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return false, fmt.Errorf("github code search response is not valid JSON: %w", err)
	}
	if envelope.Status == codeSearchIndexPendingResultStatus || envelope.IncompleteResults {
		return true, nil
	}
	if envelope.Content == nil {
		return false, nil
	}
	parsedBlock := false
	for _, block := range envelope.Content {
		if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		var payload struct {
			IncompleteResults bool `json:"incomplete_results"`
		}
		if err := json.Unmarshal([]byte(block.Text), &payload); err != nil {
			return false, fmt.Errorf("github code search content block is not valid JSON: %w", err)
		}
		parsedBlock = true
		if payload.IncompleteResults {
			return true, nil
		}
	}
	if len(envelope.Content) > 0 && !parsedBlock {
		return false, errors.New("github code search response has no parseable text content")
	}
	return false, nil
}

func newCodeSearchIndexPendingResult(raw string, attempts int) (string, error) {
	response := json.RawMessage(raw)
	if !json.Valid(response) {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return "", fmt.Errorf("encode invalid github code search response: %w", err)
		}
		response = encoded
	}
	value := struct {
		Status            string          `json:"status"`
		Tool              string          `json:"tool"`
		Attempts          int             `json:"attempts"`
		IncompleteResults bool            `json:"incomplete_results"`
		Message           string          `json:"message"`
		GitHubResponse    json.RawMessage `json:"github_response"`
	}{
		Status:            codeSearchIndexPendingResultStatus,
		Tool:              codeSearchToolName,
		Attempts:          attempts,
		IncompleteResults: true,
		Message:           "GitHub Code Search returned incomplete results after bounded retries; do not treat this as no matches. Use known file or commit tools when possible, or report the limitation.",
		GitHubResponse:    response,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode github code search pending result: %w", err)
	}
	return string(encoded), nil
}

func wrapCodeSearchTool(ctx context.Context, current tool.BaseTool) (tool.BaseTool, error) {
	if current == nil {
		return nil, errors.New("github search_code tool is nil")
	}
	info, err := current.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("read github search_code tool info: %w", err)
	}
	if info == nil || info.Name != codeSearchToolName {
		return current, nil
	}
	invokable, ok := current.(tool.InvokableTool)
	if !ok {
		return nil, errors.New("github search_code tool is not invokable")
	}
	return &retryingTool{inner: invokable, policy: defaultCodeSearchRetryPolicy()}, nil
}

func wrapGitHubTool(ctx context.Context, current tool.BaseTool) (tool.BaseTool, error) {
	if current == nil {
		return nil, errors.New("github tool is nil")
	}
	info, err := current.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("read github tool info: %w", err)
	}
	if info == nil {
		return nil, errors.New("github tool info is nil")
	}
	if info.Name == codeSearchToolName {
		return wrapCodeSearchTool(ctx, current)
	}
	if info.Name == repositoryTreeToolName {
		return wrapRepositoryTreeTool(ctx, current)
	}
	return current, nil
}
