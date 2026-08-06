package queryrewrite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type generatorStub struct {
	messages []*schema.Message
	response *schema.Message
	err      error
}

func (s *generatorStub) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	s.messages = messages
	return s.response, s.err
}

func TestRewriterReturnsStrictStructuredCandidate(t *testing.T) {
	response := schema.AssistantMessage(`{"lexicalQuery":"ERP-504 timeout","semanticQuery":"ERP-504 gateway timeout","subqueries":[]}`, nil)
	response.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
	generator := &generatorStub{response: response}
	rewriter, err := New(generator, "system prompt", "query-rewrite-v1", 5*time.Second, 2, 2048)
	if err != nil {
		t.Fatal(err)
	}
	result, err := rewriter.Rewrite(context.Background(), "ERP-504 timeout")
	if err != nil {
		t.Fatal(err)
	}
	if result.LexicalQuery != "ERP-504 timeout" || result.SemanticQuery != "ERP-504 gateway timeout" ||
		result.PromptVersion != "query-rewrite-v1" || result.Usage.TotalTokens != 15 || len(generator.messages) != 2 {
		t.Fatalf("result=%+v messages=%+v", result, generator.messages)
	}
	if generator.messages[0].Role != schema.System || !strings.Contains(generator.messages[1].Content, `"query":"ERP-504 timeout"`) ||
		!strings.Contains(generator.messages[1].Content, `"protectedSignals":["erp-504"]`) {
		t.Fatalf("messages=%+v", generator.messages)
	}
}

func TestRewriterRejectsUnknownFieldsAndProviderErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		err      error
	}{
		{name: "unknown field", response: `{"lexicalQuery":"q","semanticQuery":"q","subqueries":[],"answer":"x"}`},
		{name: "trailing JSON", response: `{"lexicalQuery":"q","semanticQuery":"q","subqueries":[]} {}`},
		{name: "missing subqueries", response: `{"lexicalQuery":"q","semanticQuery":"q"}`},
		{name: "null subqueries", response: `{"lexicalQuery":"q","semanticQuery":"q","subqueries":null}`},
		{name: "provider failure", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{err: test.err}
			if test.response != "" {
				generator.response = schema.AssistantMessage(test.response, nil)
			}
			rewriter, err := New(generator, "system prompt", "v1", time.Second, 2, 2048)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rewriter.Rewrite(context.Background(), "query")
			if err == nil {
				t.Fatal("Rewrite accepted invalid output")
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("Rewrite error = %v", err)
			}
		})
	}
}
