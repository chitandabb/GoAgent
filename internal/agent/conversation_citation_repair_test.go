package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type citationRepairModelStub struct {
	content string
	options *model.Options
}

func (m *citationRepairModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *citationRepairModelStub) Generate(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.options = model.GetCommonOptions(nil, opts...)
	message := schema.AssistantMessage(m.content, nil)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25,
	}}
	return message, nil
}

func (m *citationRepairModelStub) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestModelConversationCitationRepairerReturnsOnlyValidatedCitations(t *testing.T) {
	source := conversation.MessageCitation{
		SourceType:    conversation.CitationSourceWeb,
		SourceRef:     "https://docs.example.com/runbook",
		ContentSHA256: strings.Repeat("a", 64),
	}
	modelStub := &citationRepairModelStub{
		content: `{"answer":"Use the runbook. [source:https://docs.example.com/runbook]"}`,
	}
	repairer, err := NewModelConversationCitationRepairer(ModelConversationCitationRepairerConfig{
		ChatModel: modelStub, Instruction: "strict citation repair", PromptVersion: "repair-v1",
		Timeout: time.Second, MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repairer.Repair(context.Background(), ConversationCitationRepairRequest{
		UserQuery: "What should I do?", Draft: "Use the runbook.",
		Evidence: []string{`{"content":"Use the runbook."}`}, Sources: []conversation.MessageCitation{source},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Use the runbook. [source:https://docs.example.com/runbook]" ||
		result.Usage.TotalTokens != 25 || result.Usage.ModelCalls != 1 {
		t.Fatalf("result = %+v", result)
	}
	if modelStub.options == nil || modelStub.options.ToolChoice == nil || *modelStub.options.ToolChoice != schema.ToolChoiceForbidden ||
		modelStub.options.MaxTokens == nil || *modelStub.options.MaxTokens != 256 || len(modelStub.options.Tools) != 0 {
		t.Fatalf("model options = %+v", modelStub.options)
	}
}

func TestModelConversationCitationRepairerRejectsUnknownMarkerAndKeepsUsage(t *testing.T) {
	source := conversation.MessageCitation{
		SourceType:    conversation.CitationSourceWeb,
		SourceRef:     "https://docs.example.com/runbook",
		ContentSHA256: strings.Repeat("a", 64),
	}
	modelStub := &citationRepairModelStub{
		content: `{"answer":"Invented. [source:https://docs.example.com/other]"}`,
	}
	repairer, err := NewModelConversationCitationRepairer(ModelConversationCitationRepairerConfig{
		ChatModel: modelStub, Instruction: "strict citation repair", PromptVersion: "repair-v1",
		Timeout: time.Second, MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repairer.Repair(context.Background(), ConversationCitationRepairRequest{
		UserQuery: "What should I do?", Draft: "Invented.",
		Evidence: []string{`{"content":"Use the runbook."}`}, Sources: []conversation.MessageCitation{source},
	})
	if err == nil || result.Usage.TotalTokens != 25 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestConversationCitationRepairUsageCountsResponseWithoutProviderUsage(t *testing.T) {
	if got := conversationCitationRepairUsage(schema.AssistantMessage("answer", nil)); got.ModelCalls != 1 || got.TotalTokens != 0 {
		t.Fatalf("usage = %+v", got)
	}
	if got := conversationCitationRepairUsage(nil); got != (ModelUsage{}) {
		t.Fatalf("nil response usage = %+v", got)
	}
}
