package agent

import (
	"math"
	"strings"
	"testing"
)

func TestEvaluateDatasetPairsComparableRuns(t *testing.T) {
	cases := evaluationCasesForTest()
	observations := []EvaluationObservation{
		evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000),
		evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700),
		evaluationObservationForTest("code", EvaluationBaseline, 1200, 1400),
		evaluationObservationForTest("code", EvaluationExperiment, 700, 900),
	}
	observations[2].SelectedSkill = SkillTicketDiagnosis
	observations[2].RootCauseMatched = false
	observations[2].Evidence = nil
	observations[3].SelectedSkill = SkillCodeInvestigation

	summary, err := EvaluateDataset(cases, observations)
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.DatasetVersion != "dev-v1" || summary.Cases != 2 || summary.Runs != 4 || summary.PairedCases != 2 {
		t.Fatalf("unexpected summary identity: %+v", summary)
	}
	if summary.Baseline.SkillRoutingAccuracy != 0.5 || summary.Experiment.SkillRoutingAccuracy != 1 {
		t.Fatalf("routing accuracy: %+v", summary)
	}
	if summary.Baseline.TaskCompletionRate != 0.5 || summary.Experiment.TaskCompletionRate != 1 {
		t.Fatalf("task completion: %+v", summary)
	}
	wantTokenReduction := 900.0 / 2200.0
	if math.Abs(summary.PairedInputTokenReduction-wantTokenReduction) > 1e-9 {
		t.Fatalf("token reduction = %v, want %v", summary.PairedInputTokenReduction, wantTokenReduction)
	}
}

func TestEvaluateDatasetDoesNotPairDifferentModelSettings(t *testing.T) {
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.ReasoningEffort = "high"

	summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 || summary.PairedInputTokenReduction != 0 {
		t.Fatalf("mismatched model settings were paired: %+v", summary)
	}
}

func TestEvaluationCaseRejectsContradictoryToolLabels(t *testing.T) {
	definition := evaluationCasesForTest()[0]
	definition.ForbiddenTools = []string{ToolReadExternalCase}
	if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "both expected and forbidden") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestEvaluationObservationRequiresHumanReviewForRootCauseMatch(t *testing.T) {
	observation := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	observation.HumanReviewed = false
	if err := observation.Validate(); err == nil || !strings.Contains(err.Error(), "humanReviewed") {
		t.Fatalf("Validate error = %v", err)
	}
}

func evaluationCasesForTest() []EvaluationCase {
	return []EvaluationCase{
		{
			DatasetVersion: "dev-v1", CaseID: "ticket", TaskType: TaskTypeDiagnosis,
			UserQuery: "读取并诊断工单", ExpectedSkill: SkillTicketDiagnosis,
			ExpectedFirstTool: ToolReadExternalCase, ExpectedTools: []string{ToolReadExternalCase},
			RequiredEvidence: []string{"ticket"}, ExpectedRootCause: "status-sync",
			AcceptableConclusionStatuses: []ConclusionStatus{ConclusionProbable},
		},
		{
			DatasetVersion: "dev-v1", CaseID: "code", TaskType: TaskTypeDiagnosis,
			UserQuery: "读取工单并追踪代码", ExpectedSkill: SkillCodeInvestigation,
			ExpectedFirstTool: ToolReadExternalCase,
			ExpectedTools:     []string{ToolReadExternalCase, "search_code"},
			RequiredEvidence:  []string{"ticket", "code"}, ExpectedRootCause: "missing-filter",
			AcceptableConclusionStatuses: []ConclusionStatus{ConclusionProbable},
		},
	}
}

func evaluationObservationForTest(
	caseID string,
	variant EvaluationVariant,
	promptTokens int,
	durationMillis int64,
) EvaluationObservation {
	actualTools := []string{ToolReadExternalCase}
	evidence := []string{"ticket"}
	if caseID == "code" {
		actualTools = append(actualTools, "search_code")
		evidence = append(evidence, "code")
	}
	return EvaluationObservation{
		DatasetVersion: "dev-v1", CaseID: caseID, Variant: variant,
		RunID: caseID + "-" + string(variant), Model: "stepfun", ModelVersion: "step-3.7-flash",
		ReasoningEffort: "medium", PromptVersion: "test-v1", SelectedSkill: SkillTicketDiagnosis,
		ActualToolCalls: actualTools,
		AllowedTools:    []string{ToolReadExternalCase, "search_code", ToolSkill},
		Evidence:        evidence, ConclusionStatus: ConclusionProbable,
		RootCauseMatched: true, HumanReviewed: true,
		Usage: ModelUsage{
			ModelCalls: 2, PromptTokens: promptTokens, CompletionTokens: 100,
			TotalTokens: promptTokens + 100,
		},
		TTFTMillis: 500, DurationMillis: durationMillis,
	}
}
