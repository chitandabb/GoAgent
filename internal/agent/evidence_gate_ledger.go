package agent

import (
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

// BuildEvidenceGateEarlyExitLedger keeps the Early Exit quality gate in the
// domain summary while projecting shared run identity and usage into the Ledger.
func BuildEvidenceGateEarlyExitLedger(
	asset evaluationledger.Asset,
	metadata evaluationledger.SourceMetadata,
	cases []EvidenceGateEvaluationCase,
	observations []EvidenceGateEvaluationObservation,
) (evaluationledger.Report, error) {
	if err := metadata.Validate(); err != nil {
		return evaluationledger.Report{}, fmt.Errorf("source metadata: %w", err)
	}
	summary, err := EvaluateEvidenceGateEarlyExit(cases, observations)
	if err != nil {
		return evaluationledger.Report{}, err
	}
	records := make([]evaluationledger.Record, 0, len(observations))
	for _, observation := range observations {
		outcome := evaluationledger.OutcomeSucceeded
		errorType := strings.TrimSpace(observation.ErrorType)
		if skipped := strings.TrimSpace(observation.SkippedReason); skipped != "" {
			outcome = evaluationledger.OutcomeFailed
			errorType = "skipped: " + skipped
		} else if errorType != "" {
			outcome = evaluationledger.OutcomeFailed
		}
		var usage *evaluationledger.Usage
		if observation.Usage.ModelCalls > 0 && observation.Usage.TotalTokens > 0 {
			usage = &evaluationledger.Usage{
				ModelCalls: observation.Usage.ModelCalls, PromptTokens: observation.Usage.PromptTokens,
				CompletionTokens: observation.Usage.CompletionTokens, TotalTokens: observation.Usage.TotalTokens,
				CachedTokens: observation.Usage.CachedTokens, ReasoningTokens: observation.Usage.ReasoningTokens,
			}
		}
		records = append(records, evaluationledger.Record{
			Domain: asset.Domain, DatasetVersion: observation.DatasetVersion,
			CaseID: observation.CaseID, Variant: string(observation.Variant), RunID: observation.RunID,
			Operation: "evidence_gate_early_exit", Outcome: outcome,
			ModelProvider: observation.ModelProvider, ModelID: observation.ModelID,
			ModelProfile: observation.ModelProfile, PromptVersion: observation.PromptVersion,
			ReasoningEffort: observation.ReasoningEffort, Usage: usage,
			DurationMillis: observation.DurationMillis, ErrorType: errorType,
			DegradationReasons: append([]string(nil), observation.DegradationReasons...),
			ConfigFingerprint:  metadata.ConfigFingerprint, ImplementationRevision: metadata.ImplementationRevision,
		})
	}
	return evaluationledger.BuildReport(asset, metadata, records, summary)
}
