package agent

import (
	"math"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
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

func TestEvaluateDatasetPairsArmsWithDifferentToolProfileContracts(t *testing.T) {
	// ToolProfileID/toolSchemaFingerprint 是实验臂特有合同：两臂按设计不同
	// （baseline=evaluation-wide-v2，experiment=diagnosis-default），禁止要求
	// 两臂相等；身份一致时仍必须正常配对。
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	if baseline.ToolProfileID == experiment.ToolProfileID ||
		baseline.ToolSchemaFingerprint == experiment.ToolSchemaFingerprint {
		t.Fatal("test arms must use distinct Tool Profile contracts")
	}
	summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.PairedCases != 1 || summary.UnpairedRuns != 0 {
		t.Fatalf("arms with distinct Tool Profile contracts were not paired: %+v", summary)
	}
}

func TestEvaluateDatasetRejectsHistoricalV2Observation(t *testing.T) {
	// active evaluator 不保留 v2 兼容分支：历史 v2 资产只能标记 historical，
	// 不得进入正式归约。
	observation := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	observation.ObservationSchemaVersion = "evaluation-observation-v2"
	_, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), "unsupported observationSchemaVersion") {
		t.Fatalf("EvaluateDataset error = %v, want explicit v2 rejection", err)
	}
}

func TestEvaluateDatasetRejectsHistoricalV1Observation(t *testing.T) {
	// active evaluator 不保留 v1 兼容分支：历史 v1 资产只能标记 historical，
	// 不得进入正式归约。
	observation := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	observation.ObservationSchemaVersion = ""
	_, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), "historical") {
		t.Fatalf("EvaluateDataset error = %v, want explicit historical rejection", err)
	}
}

func TestEvaluateDatasetRejectsUnknownObservationSchemaVersion(t *testing.T) {
	observation := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	observation.ObservationSchemaVersion = "evaluation-observation-v9"
	_, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{observation})
	if err == nil || !strings.Contains(err.Error(), "unsupported observationSchemaVersion") {
		t.Fatalf("EvaluateDataset error = %v, want unsupported schema version rejection", err)
	}
}

func TestEvaluationObservationV3RequiresComparisonIdentity(t *testing.T) {
	observation := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	observation.ComparisonFingerprint = ""

	if err := observation.Validate(); err == nil {
		t.Fatal("v3 observation without comparison identity must be rejected")
	}
}

func TestEvaluateDatasetRejectsVariantToolProfileContractViolation(t *testing.T) {
	// baseline 必须记录 evaluation-wide-v2；伪装成 diagnosis-default 拒绝。
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	baseline.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	_, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline})
	if err == nil || !strings.Contains(err.Error(), "toolProfileId") {
		t.Fatalf("EvaluateDataset error = %v, want Tool Profile contract rejection", err)
	}
	// experiment 必须记录 diagnosis-default；伪装成 evaluation-wide-v2 拒绝。
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.ToolProfileID = string(agentruntime.ToolProfileEvaluationWide)
	_, err = EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{experiment})
	if err == nil || !strings.Contains(err.Error(), "toolProfileId") {
		t.Fatalf("EvaluateDataset error = %v, want Tool Profile contract rejection", err)
	}
}

func TestEvaluateDatasetRejectsMixedComparisonContracts(t *testing.T) {
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.ComparisonFingerprint = "sha256:" + strings.Repeat("d", 64)

	_, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err == nil || !strings.Contains(err.Error(), "comparison contract") {
		t.Fatalf("EvaluateDataset error = %v, want mixed comparison contract rejection", err)
	}
}

func TestEvaluateDatasetFailClosedOnVariantSchemaDrift(t *testing.T) {
	// 同一 variant 跨样本必须保持同一 Schema 指纹，否则 fail-closed。
	first := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	second := evaluationObservationForTest("code", EvaluationBaseline, 1200, 1400)
	second.ToolSchemaFingerprint = strings.Repeat("f", 64)
	_, err := EvaluateDataset(evaluationCasesForTest(), []EvaluationObservation{first, second})
	if err == nil || !strings.Contains(err.Error(), "Schema fingerprint") {
		t.Fatalf("EvaluateDataset error = %v, want fail-closed variant schema drift", err)
	}
}

func TestEvaluateDatasetDoesNotPairDifferentPromptVersion(t *testing.T) {
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.PromptVersion = "test-v2"

	summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("mismatched promptVersion was paired: %+v", summary)
	}
}

func TestEvaluateDatasetDoesNotPairDifferentModelProfileFingerprint(t *testing.T) {
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.ModelProfileFingerprint = strings.Repeat("b", 64)

	summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("mismatched modelProfileFingerprint was paired: %+v", summary)
	}
}

func TestEvaluateDatasetDoesNotPairDifferentImplementationRevision(t *testing.T) {
	baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
	experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
	experiment.ImplementationRevision = "git:other-revision"

	summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
	if err != nil {
		t.Fatalf("EvaluateDataset: %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("mismatched implementationRevision was paired: %+v", summary)
	}
}

func TestEvaluateDatasetExcludesDirtyArmsFromPairing(t *testing.T) {
	// dirty 观测只保留单臂统计供本地 smoke，不得进入正式 paired 归约。
	for _, name := range []string{"baseline dirty", "experiment dirty", "both dirty"} {
		t.Run(name, func(t *testing.T) {
			baseline := evaluationObservationForTest("ticket", EvaluationBaseline, 1000, 1000)
			experiment := evaluationObservationForTest("ticket", EvaluationExperiment, 600, 700)
			switch name {
			case "baseline dirty":
				baseline.ImplementationDirty = true
			case "experiment dirty":
				experiment.ImplementationDirty = true
			default:
				baseline.ImplementationDirty = true
				experiment.ImplementationDirty = true
			}
			summary, err := EvaluateDataset(evaluationCasesForTest()[:1], []EvaluationObservation{baseline, experiment})
			if err != nil {
				t.Fatalf("EvaluateDataset: %v", err)
			}
			if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
				t.Fatalf("dirty arms were paired: %+v", summary)
			}
		})
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
			DatasetVersion: "dev-v1", CaseID: "ticket", TaskType: "diagnosis",
			UserQuery: "读取并诊断工单", ExpectedSkill: SkillTicketDiagnosis,
			ExpectedFirstTool: ToolReadExternalCase, ExpectedTools: []string{ToolReadExternalCase},
			RequiredEvidence: []string{"ticket"}, ExpectedRootCause: "status-sync",
			AcceptableConclusionStatuses: []ConclusionStatus{ConclusionProbable},
		},
		{
			DatasetVersion: "dev-v1", CaseID: "code", TaskType: "diagnosis",
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
		ReasoningEffort: "medium", PromptVersion: "test-v1",
		ObservationSchemaVersion: EvaluationObservationV3,
		ToolProfileID:            evaluationProfileIDForTest(variant),
		ToolSchemaFingerprint:    evaluationSchemaFingerprintForTest(variant),
		ModelProfileFingerprint:  strings.Repeat("a", 64),
		ImplementationRevision:   "git:test-revision",
		ComparisonFingerprint:    "sha256:" + strings.Repeat("c", 64),
		SharedToolNames:          []string{ToolReadExternalCase, ToolSkill},
		BaselineOnlyToolNames:    []string{"search_code"},
		SelectedSkill:            SkillTicketDiagnosis,
		ActualToolCalls:          actualTools,
		AllowedTools:             []string{ToolReadExternalCase, "search_code", ToolSkill},
		Evidence:                 evidence,
		ConclusionStatus:         ConclusionProbable,
		RootCauseMatched:         true,
		HumanReviewed:            true,
		Usage: ModelUsage{
			ModelCalls: 2, PromptTokens: promptTokens, CompletionTokens: 100,
			TotalTokens: promptTokens + 100,
		},
		TTFTMillis: 500, DurationMillis: durationMillis,
	}
}

func evaluationProfileIDForTest(variant EvaluationVariant) string {
	if variant == EvaluationBaseline {
		return string(agentruntime.ToolProfileEvaluationWide)
	}
	return string(agentruntime.ToolProfileDiagnosis)
}

func evaluationSchemaFingerprintForTest(variant EvaluationVariant) string {
	if variant == EvaluationBaseline {
		return strings.Repeat("1", 64)
	}
	return strings.Repeat("2", 64)
}
