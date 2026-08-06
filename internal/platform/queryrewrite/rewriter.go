package queryrewrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Generator interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

type Rewriter struct {
	generator      Generator
	prompt         string
	promptVersion  string
	timeout        time.Duration
	maxSubqueries  int
	maxOutputRunes int
}

func New(
	generator Generator, prompt, promptVersion string,
	timeout time.Duration, maxSubqueries, maxOutputRunes int,
) (*Rewriter, error) {
	if generator == nil || strings.TrimSpace(prompt) == "" || prompt != strings.TrimSpace(prompt) ||
		strings.TrimSpace(promptVersion) == "" || promptVersion != strings.TrimSpace(promptVersion) ||
		timeout < time.Second || timeout > 30*time.Second || maxSubqueries < 0 || maxSubqueries > knowledge.MaxQuerySubqueries ||
		maxOutputRunes < 128 || maxOutputRunes > 4096 {
		return nil, errors.New("query rewriter config is invalid")
	}
	return &Rewriter{
		generator: generator, prompt: prompt, promptVersion: promptVersion,
		timeout: timeout, maxSubqueries: maxSubqueries, maxOutputRunes: maxOutputRunes,
	}, nil
}

func (r *Rewriter) Rewrite(ctx context.Context, query string) (knowledge.QueryRewriteResult, error) {
	if r == nil || r.generator == nil {
		return knowledge.QueryRewriteResult{}, errors.New("query rewriter is unavailable")
	}
	if _, err := knowledge.OriginalQueryPlan(query); err != nil {
		return knowledge.QueryRewriteResult{}, err
	}
	payload, err := json.Marshal(struct {
		Query            string   `json:"query"`
		ProtectedSignals []string `json:"protectedSignals"`
	}{Query: query, ProtectedSignals: knowledge.ProtectedQuerySignals(query)})
	if err != nil {
		return knowledge.QueryRewriteResult{}, errors.New("encode query rewrite input")
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := r.generator.Generate(runCtx, []*schema.Message{
		{Role: schema.System, Content: r.prompt},
		schema.UserMessage(string(payload)),
	})
	if err != nil {
		return knowledge.QueryRewriteResult{}, fmt.Errorf("query rewrite model request: %w", err)
	}
	if response == nil || strings.TrimSpace(response.Content) == "" || len([]rune(response.Content)) > r.maxOutputRunes {
		return knowledge.QueryRewriteResult{}, errors.New("query rewrite model output is invalid")
	}
	decoded, err := decodeResponse(response.Content)
	if err != nil {
		return knowledge.QueryRewriteResult{}, err
	}
	if len(decoded.Subqueries) > r.maxSubqueries {
		return knowledge.QueryRewriteResult{}, errors.New("query rewrite returned too many subqueries")
	}
	result := knowledge.QueryRewriteResult{
		LexicalQuery: strings.TrimSpace(decoded.LexicalQuery), SemanticQuery: strings.TrimSpace(decoded.SemanticQuery),
		Subqueries: make([]string, 0, len(decoded.Subqueries)), PromptVersion: r.promptVersion,
	}
	for _, subquery := range decoded.Subqueries {
		result.Subqueries = append(result.Subqueries, strings.TrimSpace(subquery))
	}
	if response.ResponseMeta != nil && response.ResponseMeta.Usage != nil {
		usage := response.ResponseMeta.Usage
		result.Usage = knowledge.QueryRewriteUsage{
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		}
	}
	return result, nil
}

type responsePayload struct {
	LexicalQuery  string   `json:"lexicalQuery"`
	SemanticQuery string   `json:"semanticQuery"`
	Subqueries    []string `json:"subqueries"`
}

func decodeResponse(content string) (responsePayload, error) {
	normalized, err := normalizeJSON(content)
	if err != nil {
		return responsePayload{}, err
	}
	var encoded struct {
		LexicalQuery  string           `json:"lexicalQuery"`
		SemanticQuery string           `json:"semanticQuery"`
		Subqueries    *json.RawMessage `json:"subqueries"`
	}
	decoder := json.NewDecoder(strings.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return responsePayload{}, fmt.Errorf("decode query rewrite JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return responsePayload{}, errors.New("decode query rewrite JSON: trailing content")
	}
	if encoded.Subqueries == nil {
		return responsePayload{}, errors.New("decode query rewrite JSON: subqueries array is required")
	}
	result := responsePayload{LexicalQuery: encoded.LexicalQuery, SemanticQuery: encoded.SemanticQuery}
	if err := json.Unmarshal(*encoded.Subqueries, &result.Subqueries); err != nil || result.Subqueries == nil {
		return responsePayload{}, errors.New("decode query rewrite JSON: subqueries must be an array")
	}
	return result, nil
}

func normalizeJSON(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	lineEnd := strings.IndexByte(trimmed, '\n')
	if lineEnd < 0 || !strings.EqualFold(strings.TrimSpace(trimmed[:lineEnd]), "```json") {
		return "", errors.New("decode query rewrite JSON: unsupported Markdown fence")
	}
	remainder := strings.TrimSpace(trimmed[lineEnd+1:])
	closingLine := strings.LastIndexByte(remainder, '\n')
	if closingLine < 0 || strings.TrimSpace(remainder[closingLine+1:]) != "```" {
		return "", errors.New("decode query rewrite JSON: malformed Markdown fence")
	}
	payload := strings.TrimSpace(remainder[:closingLine])
	if payload == "" || strings.Contains(payload, "```") {
		return "", errors.New("decode query rewrite JSON: malformed Markdown fence")
	}
	return payload, nil
}
