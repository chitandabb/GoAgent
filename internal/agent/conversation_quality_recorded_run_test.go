package agent

import (
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/google/uuid"
)

func TestBuildRecordedConversationQualityObservationUsesPersistedRunFacts(t *testing.T) {
	definition := conversationQualityFixtureCase(t, "knowledge-answer")
	turnID := uuid.New()
	run := conversation.RecordedAgentRun{
		TurnID: turnID, UserQuery: definition.UserQuery,
		Answer: "失败后等待 30 秒再重试。[source:" + qualityKnowledgeRef + "]",
		Citations: []conversation.MessageCitation{{
			Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
			SourceRef: qualityKnowledgeRef, ContentSHA256: strings.Repeat("a", 64),
		}},
		Observation: conversation.AgentRunObservation{
			ModelProvider: "dashscope", ModelID: "qwen3.6-flash", PromptVersion: "conversation-v1",
			Outcome: conversation.AgentRunAnswered,
			RetrievedSources: []conversation.AgentRunSource{{
				SourceType: conversation.CitationSourceKnowledgeChunk,
				SourceRef:  qualityKnowledgeRef, ContentSHA256: strings.Repeat("a", 64),
			}},
			Usage: conversation.AgentRunUsage{
				ModelCalls: 2, PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
			},
			DurationMillis: 250,
		},
	}
	selection := ConversationQualityRecordedRunSelection{
		CaseID: definition.CaseID, TurnID: turnID.String(), EstimatedCostCNY: 0.001,
		PreviewContentSHA256ByRef: map[string]string{qualityKnowledgeRef: strings.Repeat("a", 64)},
	}
	observation, err := BuildRecordedConversationQualityObservation(definition, run, selection)
	if err != nil {
		t.Fatal(err)
	}
	if observation.ObservationKind != ConversationQualityRecordedRun || observation.RunID != turnID.String() ||
		observation.Model != "dashscope" || observation.ModelVersion != "qwen3.6-flash" ||
		len(observation.RetrievedSources) != 1 || !observation.RetrievedSources[0].PreviewRequired ||
		len(observation.Citations) != 1 || observation.Citations[0].PreviewContentSHA256 != strings.Repeat("a", 64) ||
		observation.Usage.TotalTokens != 120 || observation.EstimatedCostCNY != 0.001 {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestBuildRecordedConversationQualityObservationRejectsMismatchedQueryAndPreview(t *testing.T) {
	definition := conversationQualityFixtureCase(t, "knowledge-answer")
	turnID := uuid.New()
	run := conversation.RecordedAgentRun{
		TurnID: turnID, UserQuery: "另一个问题", Answer: "回答",
		Observation: conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-v1",
			Outcome: conversation.AgentRunAnswered,
		},
	}
	selection := ConversationQualityRecordedRunSelection{CaseID: definition.CaseID, TurnID: turnID.String()}
	if _, err := BuildRecordedConversationQualityObservation(definition, run, selection); err == nil {
		t.Fatal("mismatched user query was accepted")
	}
}

func TestBuildRecordedConversationQualityObservationSupportsTerminalFailureWithoutAssistantMessage(t *testing.T) {
	definition := ConversationQualityCase{
		DatasetVersion: "conversation-quality-v1", CaseID: "provider-timeout",
		UserQuery: "查询知识库超时后会怎样？", ExpectedOutcome: ConversationQualityFailed,
		Tags: []string{"failure"},
	}
	turnID := uuid.New()
	run := conversation.RecordedAgentRun{
		TurnID: turnID, UserQuery: definition.UserQuery, ErrorType: "agent_timeout",
		Observation: conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-v1",
			Outcome: conversation.AgentRunFailed, DurationMillis: 60_000,
		},
	}
	selection := ConversationQualityRecordedRunSelection{CaseID: definition.CaseID, TurnID: turnID.String()}
	observation, err := BuildRecordedConversationQualityObservation(definition, run, selection)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Outcome != ConversationQualityFailed || observation.ErrorType != "agent_timeout" ||
		observation.Answer != "" || observation.Usage.ModelCalls != 0 {
		t.Fatalf("observation = %+v", observation)
	}
}

func conversationQualityFixtureCase(t *testing.T, caseID string) ConversationQualityCase {
	t.Helper()
	cases, _ := conversationQualityFixture()
	for _, definition := range cases {
		if definition.CaseID == caseID {
			return definition
		}
	}
	t.Fatalf("fixture case %q not found", caseID)
	return ConversationQualityCase{}
}
