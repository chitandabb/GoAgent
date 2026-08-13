package agent

import (
	"math"
	"strings"
	"testing"
)

func TestEvaluateEvidenceGateEarlyExitAllowsBenefitsOnlyAfterQualityGate(t *testing.T) {
	cases := evidenceGateCasesForTest()
	observations := evidenceGateObservationsForTest()

	summary, err := EvaluateEvidenceGateEarlyExit(cases, observations)
	if err != nil {
		t.Fatalf("EvaluateEvidenceGateEarlyExit: %v", err)
	}
	if summary.PairedCases != 3 || summary.ReviewedCaseGap != 27 || !summary.QualityGatePassed ||
		!summary.PerformanceClaimsAllowed {
		t.Fatalf("summary gate = %+v", summary)
	}
	if summary.Baseline.CompletionRate != 1 || summary.Experiment.ConclusionCorrectness != 1 ||
		summary.Experiment.CitationCorrectness != 1 {
		t.Fatalf("quality summary = %+v", summary)
	}
	if summary.Baseline.ModelCalls != 6 || summary.Experiment.ModelCalls != 5 ||
		summary.Baseline.ToolCalls != 6 || summary.Experiment.ToolCalls != 5 {
		t.Fatalf("call summary = %+v", summary)
	}
	if math.Abs(summary.PairedModelCallReduction-1.0/6.0) > 1e-9 ||
		math.Abs(summary.PairedTokenReduction-200.0/1200.0) > 1e-9 {
		t.Fatalf("paired reductions = %+v", summary)
	}
	if summary.Baseline.P50DurationMillis != 200 || summary.Baseline.P95DurationMillis != 300 ||
		summary.Experiment.P50DurationMillis != 200 || summary.Experiment.P95DurationMillis != 200 {
		t.Fatalf("latency percentiles = %+v", summary)
	}
	if len(summary.CaseIssues) != 1 || summary.CaseIssues[0].CaseID != "never-sufficient" {
		t.Fatalf("case issues = %+v", summary.CaseIssues)
	}
}

func TestEvaluateEvidenceGateEarlyExitSuppressesBenefitsOnRegression(t *testing.T) {
	observations := evidenceGateObservationsForTest()
	for index := range observations {
		if observations[index].CaseID == "first-run-sufficient" &&
			observations[index].Variant == EvaluationExperiment {
			observations[index].ConclusionCorrect = false
			observations[index].HighSeverityWrongConclusion = true
		}
	}

	summary, err := EvaluateEvidenceGateEarlyExit(evidenceGateCasesForTest(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.QualityGatePassed || summary.PerformanceClaimsAllowed || len(summary.RegressedCases) != 1 {
		t.Fatalf("regression was hidden: %+v", summary)
	}
	if summary.PairedModelCallReduction != 0 || summary.PairedTokenReduction != 0 {
		t.Fatalf("performance claims were emitted after regression: %+v", summary)
	}
}

func TestEvaluateEvidenceGateEarlyExitRejectsNonIdenticalPairControls(t *testing.T) {
	observations := evidenceGateObservationsForTest()
	observations[1].PairingFingerprint = "sha256:different"

	_, err := EvaluateEvidenceGateEarlyExit(evidenceGateCasesForTest(), observations)
	if err == nil || !strings.Contains(err.Error(), "pairing controls") {
		t.Fatalf("error = %v", err)
	}
}

func TestEvaluateEvidenceGateEarlyExitRequiresHumanReviewBeforeClaims(t *testing.T) {
	observations := evidenceGateObservationsForTest()
	observations[1].QualityReviewed = false
	observations[1].ConclusionCorrect = false
	observations[1].CitationCorrect = false

	summary, err := EvaluateEvidenceGateEarlyExit(evidenceGateCasesForTest(), observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.QualityGatePassed || summary.PerformanceClaimsAllowed {
		t.Fatalf("unreviewed observations passed the quality gate: %+v", summary)
	}
}

func evidenceGateCasesForTest() []EvidenceGateEvaluationCase {
	return []EvidenceGateEvaluationCase{
		{DatasetVersion: "evidence-gate-v1", CaseID: "first-run-sufficient", EvidenceSufficientAtRun: 1},
		{DatasetVersion: "evidence-gate-v1", CaseID: "second-run-sufficient", EvidenceSufficientAtRun: 2},
		{DatasetVersion: "evidence-gate-v1", CaseID: "never-sufficient", EvidenceSufficientAtRun: 0},
	}
}

func evidenceGateObservationsForTest() []EvidenceGateEvaluationObservation {
	makeObservation := func(caseID string, variant EvaluationVariant, agentRuns, modelCalls, toolCalls, tokens int, duration int64) EvidenceGateEvaluationObservation {
		return EvidenceGateEvaluationObservation{
			DatasetVersion: "evidence-gate-v1", CaseID: caseID, Variant: variant,
			RunID: caseID + "-" + string(variant), EarlyExitEnabled: variant == EvaluationExperiment,
			PairingFingerprint: "sha256:pair", ModelProvider: "fixture", ModelID: "scripted-v1",
			ModelProfile: "fixture", PromptVersion: "diagnosis-v1", ReasoningEffort: "none",
			AgentRuns: agentRuns, Completed: true, QualityReviewed: true,
			ConclusionCorrect: true, CitationCorrect: true,
			Usage:     ModelUsage{ModelCalls: modelCalls, PromptTokens: tokens - 50, CompletionTokens: 50, TotalTokens: tokens},
			ToolCalls: toolCalls, DurationMillis: duration,
		}
	}
	result := []EvidenceGateEvaluationObservation{
		makeObservation("first-run-sufficient", EvaluationBaseline, 2, 2, 2, 400, 200),
		makeObservation("first-run-sufficient", EvaluationExperiment, 1, 1, 1, 200, 100),
		makeObservation("second-run-sufficient", EvaluationBaseline, 2, 2, 2, 400, 200),
		makeObservation("second-run-sufficient", EvaluationExperiment, 2, 2, 2, 400, 200),
		makeObservation("never-sufficient", EvaluationBaseline, 2, 2, 2, 400, 300),
		makeObservation("never-sufficient", EvaluationExperiment, 2, 2, 2, 400, 200),
	}
	result[4].DegradationReasons = []string{"evidence_insufficient_after_budget"}
	result[5].DegradationReasons = []string{"evidence_insufficient_after_budget"}
	return result
}
