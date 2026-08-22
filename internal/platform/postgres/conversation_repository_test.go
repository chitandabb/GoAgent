package postgres

import (
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/google/uuid"
)

func TestConversationTitleFromMessage(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "collapse whitespace", input: "  设备   报警\n如何处理？ ", want: "设备 报警 如何处理？"},
		{name: "empty", input: " \n\t", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversationTitleFromMessage(tt.input); got != tt.want {
				t.Fatalf("conversationTitleFromMessage() = %q, want %q", got, tt.want)
			}
		})
	}
	longInput := strings.Repeat("设备告警 ", 30)
	got := conversationTitleFromMessage(longInput)
	if len([]rune(got)) != maxConversationTitleRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("conversationTitleFromMessage() length/suffix = %q, want %d runes with ellipsis", got, maxConversationTitleRunes)
	}
}

func TestApplyResponseProvenance(t *testing.T) {
	turnID := uuid.New()
	message := conversation.Message{ID: uuid.New(), Role: conversation.MessageRoleAssistant}
	observation := &conversation.AgentRunObservation{
		Outcome: conversation.AgentRunAnswered, ToolCalls: 2, DurationMillis: 321,
		RetrievedSources: []conversation.AgentRunSource{
			{SourceType: conversation.CitationSourceKnowledgeChunk},
			{SourceType: conversation.CitationSourceKnowledgeChunk},
			{SourceType: conversation.CitationSourceWeb},
		},
	}
	applyResponseProvenance(&message, turnID, observation)
	if message.TurnID == nil || *message.TurnID != turnID || message.Provenance == nil {
		t.Fatalf("message provenance = %+v", message)
	}
	if message.Provenance.ExecutionPath != conversation.AgentRunExecutionAgent ||
		message.Provenance.ToolCalls != 2 || len(message.Provenance.Sources) != 2 ||
		message.Provenance.Sources[0].Count != 2 || message.Provenance.Sources[1].Count != 1 {
		t.Fatalf("provenance = %+v", message.Provenance)
	}
}
