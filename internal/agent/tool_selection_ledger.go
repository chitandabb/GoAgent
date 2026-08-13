package agent

import (
	"fmt"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

// BuildToolSelectionLedger validates and scores the existing Tool Selection domain observations,
// then projects only their shared run fields into the Evaluation Ledger.
func BuildToolSelectionLedger(
	asset evaluationledger.Asset,
	metadata evaluationledger.SourceMetadata,
	cases []ToolSelectionCase,
	observations []ToolSelectionObservation,
) (evaluationledger.Report, error) {
	if err := metadata.Validate(); err != nil {
		return evaluationledger.Report{}, fmt.Errorf("source metadata: %w", err)
	}
	summary, err := EvaluateToolSelection(cases, observations)
	if err != nil {
		return evaluationledger.Report{}, err
	}
	records := make([]evaluationledger.Record, 0, len(observations))
	for _, observation := range observations {
		outcome := evaluationledger.OutcomeSucceeded
		if observation.ErrorType != "" {
			outcome = evaluationledger.OutcomeFailed
		}
		var usage *evaluationledger.Usage
		if observation.Usage.ModelCalls > 0 && observation.Usage.TotalTokens > 0 {
			usage = &evaluationledger.Usage{
				ModelCalls:       observation.Usage.ModelCalls,
				PromptTokens:     observation.Usage.PromptTokens,
				CompletionTokens: observation.Usage.CompletionTokens,
				TotalTokens:      observation.Usage.TotalTokens,
				CachedTokens:     observation.Usage.CachedTokens,
				ReasoningTokens:  observation.Usage.ReasoningTokens,
			}
		}
		records = append(records, evaluationledger.Record{
			Domain: asset.Domain, DatasetVersion: observation.DatasetVersion,
			CaseID: observation.CaseID, Variant: string(observation.Variant), RunID: observation.RunID,
			Operation: "tool_selection", Outcome: outcome,
			ModelProvider: observation.ModelProvider, ModelID: observation.ModelID,
			ModelProfile: metadata.ModelProfile, PromptVersion: observation.PromptVersion,
			ReasoningEffort: observation.ReasoningEffort, Usage: usage,
			DurationMillis: observation.DurationMillis, ErrorType: observation.ErrorType,
			ConfigFingerprint: metadata.ConfigFingerprint, ImplementationRevision: metadata.ImplementationRevision,
		})
	}
	return evaluationledger.BuildReport(asset, metadata, records, summary)
}
