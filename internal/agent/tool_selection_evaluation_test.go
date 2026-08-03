package agent

import (
	"math"
	"testing"
)

func TestEvaluateToolSelectionUsesProviderPromptTokensForPairedReduction(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.Wide.Accuracy != 1 || summary.Filtered.Accuracy != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 ||
		math.Abs(summary.PairedToolSchemaTokenReduction-(500.0/900.0)) > 1e-9 ||
		math.Abs(summary.PairedSchemaByteReduction-0.6) > 1e-9 {
		t.Fatalf("reductions = %v/%v/%v", summary.PairedPromptTokenReduction, summary.PairedToolSchemaTokenReduction, summary.PairedSchemaByteReduction)
	}
}

func TestEvaluateToolSelectionPairsInvalidSelectionsWithProviderUsage(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "")
	filtered.ToolCallCount = 0
	filtered.ErrorType = "invalid_tool_call_count"
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.UnpairedRuns != 0 {
		t.Fatalf("pair counts = %d/%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 {
		t.Fatalf("prompt reduction = %v", summary.PairedPromptTokenReduction)
	}
}

func validToolSelectionObservation(
	variant ToolSelectionVariant,
	runID string,
	promptTokens, schemaBytes int,
	selected string,
) ToolSelectionObservation {
	return ToolSelectionObservation{
		DatasetVersion: "tools-v1", CaseID: "case-1", Variant: variant, RunID: runID,
		ModelProvider: "stepfun", ModelID: "model", ReasoningEffort: "low", PromptVersion: "selection-v1",
		MaxOutputTokens: 512,
		AvailableTools:  []string{"search_code"}, SelectedTool: selected, ToolCallCount: 1,
		ToolSchemaHash: "sha256:test", ToolSchemaBytes: schemaBytes,
		BasePromptTokens: 100, ToolSchemaPromptTokens: promptTokens - 100,
		Usage: ModelUsage{ModelCalls: 1, PromptTokens: promptTokens, CompletionTokens: 10, TotalTokens: promptTokens + 10},
	}
}
