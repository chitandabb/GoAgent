package memorycompactor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/platform/memorycompactor"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type generatorFunc func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)

func (f generatorFunc) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return f(ctx, messages, options...)
}

func TestModelCompactorReturnsStrictStructuredPayloadAndUsage(t *testing.T) {
	conversationID := uuid.New()
	encodedPayload := `{"conversationGoal":null,"facts":[],"decisions":[],"corrections":[],"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[]}`
	generator := generatorFunc(func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		if len(messages) != 2 || messages[0].Role != schema.System || messages[0].Content != "Return the fixed memory JSON schema." ||
			messages[1].Role != schema.User {
			t.Fatalf("model messages = %+v", messages)
		}
		var input struct {
			Mode           string `json:"mode"`
			ConversationID string `json:"conversationId"`
			Coverage       struct {
				FromSeq    int64 `json:"fromSeq"`
				ThroughSeq int64 `json:"throughSeq"`
			} `json:"coverage"`
			Messages []struct {
				Seq     int64  `json:"seq"`
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"newMessages"`
			Attempt               int    `json:"attempt"`
			RepairCode            string `json:"repairCode"`
			KnownReportReferences []struct {
				ReferenceID       string  `json:"referenceId"`
				SourceMessageSeqs []int64 `json:"sourceMessageSeqs"`
			} `json:"knownReportReferences"`
		}
		if err := json.Unmarshal([]byte(messages[1].Content), &input); err != nil {
			t.Fatalf("decode model input: %v", err)
		}
		if input.Mode != "initial" || input.ConversationID != conversationID.String() || input.Coverage.FromSeq != 1 ||
			input.Coverage.ThroughSeq != 1 || len(input.Messages) != 1 || input.Messages[0].Role != "user" ||
			input.Attempt != 1 || input.RepairCode != "" || len(input.KnownReportReferences) != 1 ||
			input.KnownReportReferences[0].ReferenceID != "report-1" ||
			len(input.KnownReportReferences[0].SourceMessageSeqs) != 1 || input.KnownReportReferences[0].SourceMessageSeqs[0] != 1 {
			t.Fatalf("model input = %+v", input)
		}
		return &schema.Message{
			Role: schema.Assistant, Content: "```json\n" + encodedPayload + "\n```",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 90, CompletionTokens: 20, TotalTokens: 110,
				PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 15},
			}},
		}, nil
	})
	compactor, err := memorycompactor.New(memorycompactor.Config{
		Generator: generator, Prompt: "Return the fixed memory JSON schema.", PromptVersion: "memory-v1",
		Timeout: time.Second, MaxOutputBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := compactor.Compact(context.Background(), conversationmemory.CompactionInput{
		ConversationID: conversationID, FromSeq: 1, ThroughSeq: 1, Attempt: 1,
		KnownReportReferences: map[string][]int64{"report-1": {1}},
		NewMessages: []conversation.Message{
			{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "记住这个目标"},
		},
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if output.Payload.Facts == nil || output.Payload.ReportReferences == nil || output.Usage.PromptTokens != 90 ||
		output.Usage.CompletionTokens != 20 || output.Usage.TotalTokens != 110 || output.Usage.CachedTokens != 15 {
		t.Fatalf("output = %+v", output)
	}
}

func TestModelCompactorRejectsMalformedOrOversizedOutput(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    error
	}{
		{name: "unknown schema field", content: `{"conversationGoal":null,"facts":[],"decisions":[],"corrections":[],"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[],"guess":"x"}`, want: conversationmemory.ErrInvalidPayloadSchema},
		{name: "oversized", content: `{"value":"` + string(make([]byte, 2048)) + `"}`, want: memorycompactor.ErrOutputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compactor, err := memorycompactor.New(memorycompactor.Config{
				Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
					return &schema.Message{Role: schema.Assistant, Content: tt.content}, nil
				}),
				Prompt: "Return JSON.", PromptVersion: "memory-v1", Timeout: time.Second, MaxOutputBytes: 1024,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = compactor.Compact(context.Background(), conversationmemory.CompactionInput{
				ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 1, Attempt: 1,
				NewMessages: []conversation.Message{{ID: uuid.New(), ConversationID: uuid.New(), Seq: 1, Role: conversation.MessageRoleUser, Content: "x"}},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Compact() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestModelCompactorRejectsLengthTruncatedOutputBeforeJSONDecode(t *testing.T) {
	for _, content := range []string{`{"conversationGoal":`, ""} {
		compactor, err := memorycompactor.New(memorycompactor.Config{
			Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return &schema.Message{
					Role: schema.Assistant, Content: content,
					ResponseMeta: &schema.ResponseMeta{FinishReason: "length"},
				}, nil
			}),
			Prompt: "Return JSON.", PromptVersion: "memory-v1", Timeout: time.Second, MaxOutputBytes: 1024,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = compactor.Compact(context.Background(), conversationmemory.CompactionInput{
			ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 1, Attempt: 1,
			NewMessages: []conversation.Message{{ID: uuid.New(), ConversationID: uuid.New(), Seq: 1, Role: conversation.MessageRoleUser, Content: "x"}},
		})
		if !errors.Is(err, memorycompactor.ErrOutputTruncated) {
			t.Fatalf("Compact(content=%q) error = %v, want ErrOutputTruncated", content, err)
		}
	}
}

func TestModelCompactorPrefersLengthTruncationWhenProviderReturnsResponseAndError(t *testing.T) {
	providerErr := errors.New("provider rejected incomplete strict JSON")
	compactor, err := memorycompactor.New(memorycompactor.Config{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return &schema.Message{
				Role: schema.Assistant, Content: `{"conversationGoal":`,
				ResponseMeta: &schema.ResponseMeta{FinishReason: "length"},
			}, providerErr
		}),
		Prompt: "Return JSON.", PromptVersion: "memory-v1", Timeout: time.Second, MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compactor.Compact(context.Background(), conversationmemory.CompactionInput{
		ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 1, Attempt: 1,
		NewMessages: []conversation.Message{{ID: uuid.New(), ConversationID: uuid.New(), Seq: 1, Role: conversation.MessageRoleUser, Content: "x"}},
	})
	if !errors.Is(err, memorycompactor.ErrOutputTruncated) || errors.Is(err, memorycompactor.ErrProviderRequest) {
		t.Fatalf("Compact() error = %v, want only ErrOutputTruncated", err)
	}
}

func TestModelCompactorPreservesHTTPProviderErrorsBeforeLengthTruncation(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			apiErr := &modelopenai.APIError{HTTPStatusCode: status, Message: "fixture provider error"}
			compactor, err := memorycompactor.New(memorycompactor.Config{
				Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
					return &schema.Message{
						Role: schema.Assistant, Content: `{"conversationGoal":`,
						ResponseMeta: &schema.ResponseMeta{FinishReason: "length"},
					}, apiErr
				}),
				Prompt: "Return JSON.", PromptVersion: "memory-v1", Timeout: time.Second, MaxOutputBytes: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = compactor.Compact(context.Background(), conversationmemory.CompactionInput{
				ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 1, Attempt: 1,
				NewMessages: []conversation.Message{{ID: uuid.New(), ConversationID: uuid.New(), Seq: 1, Role: conversation.MessageRoleUser, Content: "x"}},
			})
			var gotAPIError *modelopenai.APIError
			if !errors.Is(err, memorycompactor.ErrProviderRequest) || errors.Is(err, memorycompactor.ErrOutputTruncated) ||
				!errors.As(err, &gotAPIError) || gotAPIError.HTTPStatusCode != status {
				t.Fatalf("Compact() error = %v, want preserved HTTP %d Provider error", err, status)
			}
			var nonRetryable conversationmemory.NonRetryableCompactionError
			if !errors.As(err, &nonRetryable) || nonRetryable.NonRetryableCompaction() != (status == 401) {
				t.Fatalf("NonRetryableCompaction(status=%d) = %v", status, nonRetryable != nil && nonRetryable.NonRetryableCompaction())
			}
		})
	}
}
