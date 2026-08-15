package agent

import (
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestBuildToolSelectionLedgerPreservesDomainMetricsAndMissingUsage(t *testing.T) {
	t.Parallel()

	asset := evaluationledger.Asset{
		ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
		Status: evaluationledger.AssetRetestNeeded, Reason: "Current tool contracts require a selective retest.",
		EntryPoint: "mesguard-tool-selection-eval", DatasetArtifact: "dataset.jsonl",
		ObservationArtifact: "observations.jsonl", ReportArtifact: "summary.json",
	}
	metadata := evaluationledger.SourceMetadata{
		ModelProfile: "stepfun-main", ConfigFingerprint: "sha256:config", ImplementationRevision: "git:revision-1",
		DatasetSHA256: "sha256:dataset", ObservationSHA256: "sha256:observations",
	}
	cases := []ToolSelectionCase{{
		DatasetVersion: "tool-selection-v1", CaseID: "case-1", Scope: ToolSelectionTicket,
		UserQuery: "读取工单", ExpectedTool: ToolReadExternalCase,
	}}
	observations := []ToolSelectionObservation{
		toolSelectionLedgerObservation("run-wide", ToolSelectionWide, "", ModelUsage{
			ModelCalls: 1, PromptTokens: 120, CompletionTokens: 5, TotalTokens: 125,
		}),
		toolSelectionLedgerObservation("run-production", ToolSelectionProduction, "missing_provider_usage", ModelUsage{}),
	}

	report, err := BuildToolSelectionLedger(asset, metadata, cases, observations)
	if err != nil {
		t.Fatalf("BuildToolSelectionLedger() error = %v", err)
	}
	if report.Summary.Runs != 2 || report.Summary.UsageAvailableRuns != 1 || report.Summary.UsageUnavailableRuns != 1 {
		t.Fatalf("ledger summary = %+v", report.Summary)
	}
	if report.Records[0].ModelProvider != "stepfun" || report.Records[0].ModelID != "step-3.7-flash" ||
		report.Records[0].ModelProfile != "stepfun-main" || report.Records[0].Usage == nil {
		t.Fatalf("wide ledger record = %+v", report.Records[0])
	}
	if report.Records[1].Outcome != evaluationledger.OutcomeFailed || report.Records[1].Usage != nil {
		t.Fatalf("production ledger record = %+v", report.Records[1])
	}
	var summary ToolSelectionSummary
	if err := report.DecodeDomainSummary(&summary); err != nil {
		t.Fatalf("DecodeDomainSummary() error = %v", err)
	}
	if summary.Wide.Accuracy != 1 || summary.Production.FailedRuns != 1 || summary.PairedCases != 0 {
		t.Fatalf("domain summary = %+v", summary)
	}
}

func toolSelectionLedgerObservation(
	runID string,
	variant ToolSelectionVariant,
	errorType string,
	usage ModelUsage,
) ToolSelectionObservation {
	observation := ToolSelectionObservation{
		DatasetVersion: "tool-selection-v1", CaseID: "case-1", Variant: variant, RunID: runID,
		ModelProvider: "stepfun", ModelID: "step-3.7-flash", ReasoningEffort: "low",
		PromptVersion: "tool-selection-v1", MaxOutputTokens: 64,
		AvailableTools: []string{ToolReadExternalCase}, SelectedTool: ToolReadExternalCase,
		ToolCallCount: 1, ToolSchemaHash: "sha256:schema", ToolSchemaBytes: 100,
		BasePromptTokens: 80, ToolSchemaPromptTokens: 40, Usage: usage,
		DurationMillis: 200, ErrorType: errorType,
		ObservationSchemaVersion: ToolSelectionObservationV3,
		ModelVisibleNames:        []string{ToolReadExternalCase},
		ModelProfileFingerprint:  strings.Repeat("ab", 32),
		ImplementationRevision:   "git:revision-1",
		ComparisonFingerprint:    "sha256:" + strings.Repeat("cd", 32),
		SharedToolNames:          []string{ToolReadExternalCase},
		BaselineOnlyToolNames:    []string{"read_conversation_tool_result"},
	}
	if variant == ToolSelectionWide {
		observation.ToolProfileID = ToolSelectionEvaluationWideProfile
		observation.AvailableTools = []string{ToolReadExternalCase, "read_conversation_tool_result"}
		observation.ModelVisibleNames = append([]string(nil), observation.AvailableTools...)
	} else {
		observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	}
	return observation
}
