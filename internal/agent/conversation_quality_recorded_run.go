package agent

import (
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/google/uuid"
)

// ConversationQualityRecordedRunSelection maps one immutable dataset case to
// one persisted Conversation turn. This small manifest is the only manual
// input required for export; model output, usage, sources, citations and timing
// are loaded from the run ledger.
type ConversationQualityRecordedRunSelection struct {
	CaseID                    string            `json:"caseId"`
	TurnID                    string            `json:"turnId"`
	EstimatedCostCNY          float64           `json:"estimatedCostCny"`
	PreviewContentSHA256ByRef map[string]string `json:"previewContentSha256ByRef,omitempty"`
}

func (s ConversationQualityRecordedRunSelection) Validate() error {
	if !conversationQualityLabelPattern.MatchString(s.CaseID) {
		return errors.New("recorded conversation quality caseId is invalid")
	}
	turnID, err := uuid.Parse(strings.TrimSpace(s.TurnID))
	if err != nil || turnID == uuid.Nil || turnID.String() != s.TurnID {
		return errors.New("recorded conversation quality turnId is invalid")
	}
	if math.IsNaN(s.EstimatedCostCNY) || math.IsInf(s.EstimatedCostCNY, 0) ||
		s.EstimatedCostCNY < 0 || s.EstimatedCostCNY > 1_000 {
		return errors.New("recorded conversation quality cost is invalid")
	}
	for sourceRef, digest := range s.PreviewContentSHA256ByRef {
		if strings.TrimSpace(sourceRef) == "" || sourceRef != strings.TrimSpace(sourceRef) ||
			!validConversationQualitySHA256(digest) {
			return errors.New("recorded conversation quality preview hash is invalid")
		}
	}
	return nil
}

func BuildRecordedConversationQualityObservation(
	definition ConversationQualityCase,
	run conversation.RecordedAgentRun,
	selection ConversationQualityRecordedRunSelection,
) (ConversationQualityObservation, error) {
	if err := definition.Validate(); err != nil {
		return ConversationQualityObservation{}, err
	}
	if err := selection.Validate(); err != nil {
		return ConversationQualityObservation{}, err
	}
	turnID, _ := uuid.Parse(selection.TurnID)
	isFailed := run.Observation.Outcome == conversation.AgentRunFailed
	if selection.CaseID != definition.CaseID || run.TurnID != turnID ||
		run.UserQuery != definition.UserQuery || (!isFailed && strings.TrimSpace(run.Answer) == "") ||
		run.Observation.Validate() != nil {
		return ConversationQualityObservation{}, errors.New("recorded conversation run does not match the selected case")
	}
	relevantByRef := make(map[string]ConversationQualitySource, len(definition.RelevantSources))
	for _, source := range definition.RelevantSources {
		relevantByRef[source.SourceRef] = source
	}
	observation := ConversationQualityObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID,
		RunID: run.TurnID.String(), ObservationKind: ConversationQualityRecordedRun,
		Model: run.Observation.ModelProvider, ModelVersion: run.Observation.ModelID,
		PromptVersion: run.Observation.PromptVersion, Answer: run.Answer,
		Outcome:          ConversationQualityOutcome(run.Observation.Outcome),
		DegradedChannels: append([]string(nil), run.Observation.DegradedChannels...),
		Usage: ModelUsage{
			ModelCalls:       run.Observation.Usage.ModelCalls,
			PromptTokens:     run.Observation.Usage.PromptTokens,
			CompletionTokens: run.Observation.Usage.CompletionTokens,
			TotalTokens:      run.Observation.Usage.TotalTokens,
			CachedTokens:     run.Observation.Usage.CachedTokens,
			ReasoningTokens:  run.Observation.Usage.ReasoningTokens,
		},
		DurationMillis: run.Observation.DurationMillis, EstimatedCostCNY: selection.EstimatedCostCNY,
		ErrorType: run.ErrorType,
	}
	if run.Observation.SourcesTruncated {
		if !slices.Contains(observation.DegradedChannels, "retrieved_sources_truncated") {
			observation.DegradedChannels = append(observation.DegradedChannels, "retrieved_sources_truncated")
		}
		if observation.Outcome != ConversationQualityFailed {
			observation.Outcome = ConversationQualityDegraded
		}
	}
	for _, source := range run.Observation.RetrievedSources {
		qualitySource := ConversationQualitySource{
			SourceType: EvidenceSourceType(source.SourceType), SourceRef: source.SourceRef,
			ContentSHA256: source.ContentSHA256,
		}
		if relevant, exists := relevantByRef[source.SourceRef]; exists {
			qualitySource.PreviewRequired = relevant.PreviewRequired
		}
		observation.RetrievedSources = append(observation.RetrievedSources, qualitySource)
	}
	citedRefs := make(map[string]struct{}, len(run.Citations))
	for _, citation := range run.Citations {
		qualityCitation := ConversationQualityCitation{
			SourceType: EvidenceSourceType(citation.SourceType), SourceRef: citation.SourceRef,
			ContentSHA256:        citation.ContentSHA256,
			PreviewContentSHA256: selection.PreviewContentSHA256ByRef[citation.SourceRef],
		}
		observation.Citations = append(observation.Citations, qualityCitation)
		citedRefs[citation.SourceRef] = struct{}{}
	}
	for sourceRef := range selection.PreviewContentSHA256ByRef {
		if _, exists := citedRefs[sourceRef]; !exists {
			return ConversationQualityObservation{}, errors.New("preview hash does not belong to a cited source")
		}
	}
	if err := observation.Validate(); err != nil {
		return ConversationQualityObservation{}, err
	}
	return observation, nil
}
