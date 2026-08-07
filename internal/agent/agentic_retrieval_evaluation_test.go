package agent

import "testing"

func TestEvaluateAgenticRetrievalMeasuresTriggerAndEvidenceOutcomes(t *testing.T) {
	cases := []AgenticRetrievalEvaluationCase{
		{
			DatasetVersion: "agentic-retrieval-v1", CaseID: "missing", Scenario: AgenticScenarioEvidenceGap,
			UserQuery: "补充企业知识证据", ExpectedAttempted: true, ExpectedAdded: true,
			ExpectedStop: "new_evidence_added",
		},
		{
			DatasetVersion: "agentic-retrieval-v1", CaseID: "format", Scenario: AgenticScenarioFormatOnly,
			UserQuery: "修正报告格式", ExpectedStop: "not_eligible",
		},
		{
			DatasetVersion: "agentic-retrieval-v1", CaseID: "valid", Scenario: AgenticScenarioValid,
			UserQuery: "已有证据足够", ExpectedStop: "not_needed",
		},
	}
	observations := []AgenticRetrievalEvaluationObservation{
		agenticObservationForTest(cases[0], true, true, "new_evidence_added"),
		agenticObservationForTest(cases[1], false, false, "not_eligible"),
		agenticObservationForTest(cases[2], false, false, "not_needed"),
	}
	summary, err := EvaluateAgenticRetrieval(cases, observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptExpectationAccuracy != 1 || summary.AttemptPrecision != 1 || summary.AttemptRecall != 1 ||
		summary.AddedEvidenceExpectationAccuracy != 1 || summary.AddedEvidenceRate != 1 ||
		summary.StopReasonAccuracy != 1 || summary.CompletedRuns != 3 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateAgenticRetrievalRejectsAddedEvidenceWithoutAttempt(t *testing.T) {
	definition := AgenticRetrievalEvaluationCase{
		DatasetVersion: "agentic-retrieval-v1", CaseID: "missing", Scenario: AgenticScenarioEvidenceGap,
		UserQuery: "补充企业知识证据", ExpectedAttempted: true, ExpectedAdded: true,
		ExpectedStop: "new_evidence_added",
	}
	observation := agenticObservationForTest(definition, false, true, "new_evidence_added")
	if _, err := EvaluateAgenticRetrieval([]AgenticRetrievalEvaluationCase{definition}, []AgenticRetrievalEvaluationObservation{observation}); err == nil {
		t.Fatal("evaluation accepted added evidence without an attempt")
	}
}

func agenticObservationForTest(
	definition AgenticRetrievalEvaluationCase,
	attempted, added bool,
	stopReason string,
) AgenticRetrievalEvaluationObservation {
	return AgenticRetrievalEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID, Scenario: definition.Scenario,
		RunID: "run-" + definition.CaseID, Model: "fixture", ModelVersion: "fixture-v1",
		ReasoningEffort: "low", PromptVersion: "test-v1", AgentRuns: 1,
		AgenticRetrievalAttempted: attempted, AgenticRetrievalAddedEvidence: added,
		AgenticRetrievalStopReason: stopReason, Usage: ModelUsage{}, DurationMillis: 1,
	}
}
