package agent

import "testing"

func TestSummarizeEvaluation(t *testing.T) {
	registry, err := NewRegistry(testSkills()...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	summary := SummarizeEvaluation([]EvaluationObservation{
		{
			CaseID: "1", ExpectedSkill: SkillTicketDiagnosis, SelectedSkill: SkillTicketDiagnosis,
			ExpectedFirstTool: ToolReadExternalCase, ActualToolCalls: []string{ToolReadExternalCase},
			BaselineInputTokens: 1000, DynamicInputTokens: 500,
		},
		{
			CaseID: "2", ExpectedSkill: SkillCodeInvestigation, SelectedSkill: SkillTicketDiagnosis,
			ExpectedFirstTool: "search_code", ActualToolCalls: []string{"search_code"},
			BaselineInputTokens: 1000, DynamicInputTokens: 700,
		},
	}, registry)
	if summary.SkillRoutingAccuracy != 0.5 || summary.ToolSelectionAccuracy != 0.5 {
		t.Fatalf("unexpected accuracy: %+v", summary)
	}
	if summary.OutOfWhitelistCallRate != 0.5 {
		t.Fatalf("out of whitelist rate = %v", summary.OutOfWhitelistCallRate)
	}
	if summary.InputTokenReductionRate != 0.4 {
		t.Fatalf("token reduction = %v", summary.InputTokenReductionRate)
	}
}
