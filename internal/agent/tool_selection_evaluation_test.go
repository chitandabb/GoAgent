package agent

import (
	"math"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
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

// validV2ToolSelectionObservation 构造通过 v2 Validate 的完整观测。两个
// 实验臂使用各自正确的 Profile 合同（wide=评测合同，filtered=生产
// diagnosis-default），fingerprint/revision 相同以便默认可以配对。
func validV2ToolSelectionObservation(
	variant ToolSelectionVariant,
	runID string,
	promptTokens, schemaBytes int,
	selected string,
) ToolSelectionObservation {
	observation := validToolSelectionObservation(variant, runID, promptTokens, schemaBytes, selected)
	observation.ObservationSchemaVersion = ToolSelectionObservationV2
	observation.ToolProfileID = ToolSelectionEvaluationWideProfile
	if variant == ToolSelectionFiltered {
		observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	}
	observation.ModelVisibleNames = []string{"search_code"}
	observation.ModelProfileFingerprint = strings.Repeat("ab", 32)
	observation.ImplementationRevision = "abc123"
	observation.ImplementationDirty = false
	return observation
}

func TestToolSelectionObservationV2FilteredRequiresDiagnosisProfile(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	observation.ToolProfileID = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("v2 filtered observation without toolProfileId must fail")
	}
	observation.ToolProfileID = ToolSelectionEvaluationWideProfile
	if err := observation.Validate(); err == nil {
		t.Fatal("v2 filtered observation with the wide contract id must fail")
	}
}

func TestToolSelectionObservationV2WideRejectsDiagnosisProfile(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	if err := observation.Validate(); err == nil {
		t.Fatal("v2 wide observation must not masquerade as diagnosis-default")
	}
	observation.ToolProfileID = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("v2 wide observation without toolProfileId must fail")
	}
}

func TestToolSelectionObservationV2RequiresFullIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ToolSelectionObservation)
	}{
		{name: "missing fingerprint", mutate: func(o *ToolSelectionObservation) { o.ModelProfileFingerprint = "" }},
		{name: "invalid fingerprint", mutate: func(o *ToolSelectionObservation) { o.ModelProfileFingerprint = "not-a-sha256" }},
		{name: "missing revision", mutate: func(o *ToolSelectionObservation) { o.ImplementationRevision = "" }},
		{name: "unknown revision", mutate: func(o *ToolSelectionObservation) { o.ImplementationRevision = "unknown" }},
		{name: "missing model visible names", mutate: func(o *ToolSelectionObservation) { o.ModelVisibleNames = nil }},
		{name: "names differ from available", mutate: func(o *ToolSelectionObservation) {
			o.ModelVisibleNames = []string{"search_code", "extra"}
		}},
		{name: "invalid visible name", mutate: func(o *ToolSelectionObservation) {
			o.ModelVisibleNames = []string{"Invalid Name"}
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
			current.mutate(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatalf("v2 observation with %s must fail", current.name)
			}
		})
	}
	if err := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code").Validate(); err != nil {
		t.Fatalf("complete v2 observation must pass: %v", err)
	}
}

func TestToolSelectionObservationUnsupportedSchemaVersionRejected(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	observation.ObservationSchemaVersion = "tool-selection-observation-v3"
	if err := observation.Validate(); err == nil {
		t.Fatal("unknown observationSchemaVersion must be rejected")
	}
}

// TestToolSelectionObservationV2AllowsEmptyReasoningEffort：基础校验不再强制
// ReasoningEffort 非空，完整 v2 Observation 可以 Validate。
func TestToolSelectionObservationV2AllowsEmptyReasoningEffort(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	observation.ReasoningEffort = ""
	if err := observation.Validate(); err != nil {
		t.Fatalf("v2 observation with empty reasoningEffort must validate: %v", err)
	}
}

// TestToolSelectionObservationV2UnknownDirtyValidatesButNeverPairs：
// unknown+dirty 允许 Validate（本地 smoke），但禁止进入正式 paired
// reduction。
func TestToolSelectionObservationV2UnknownDirtyValidatesButNeverPairs(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	observation.ImplementationRevision = "unknown"
	observation.ImplementationDirty = true
	if err := observation.Validate(); err != nil {
		t.Fatalf("v2 unknown+dirty observation must validate for local smoke: %v", err)
	}
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	wide.ImplementationRevision = "unknown"
	wide.ImplementationDirty = true
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	filtered.ImplementationRevision = "unknown"
	filtered.ImplementationDirty = true
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 || summary.PairedPromptTokenReduction != 0 {
		t.Fatalf("unknown+dirty arms must not pair or reduce: %+v", summary)
	}
}

// TestToolSelectionObservationV2UnknownCleanRejected：revision=unknown 且
// ImplementationDirty=false 必须拒绝。
func TestToolSelectionObservationV2UnknownCleanRejected(t *testing.T) {
	observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	observation.ImplementationRevision = "unknown"
	observation.ImplementationDirty = false
	if err := observation.Validate(); err == nil {
		t.Fatal("v2 unknown+clean observation must be rejected")
	}
}

// TestToolSelectionObservationV2EmptyRevisionRejected：空 revision 必须拒绝。
func TestToolSelectionObservationV2EmptyRevisionRejected(t *testing.T) {
	for _, revision := range []string{"", "   "} {
		observation := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
		observation.ImplementationRevision = revision
		if err := observation.Validate(); err == nil {
			t.Fatalf("v2 observation with revision %q must be rejected", revision)
		}
	}
}

func TestToolSelectionObservationV1RemainsReplayableWithoutNewFields(t *testing.T) {
	observation := validToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	if err := observation.Validate(); err != nil {
		t.Fatalf("v1 observation without schema version must remain valid: %v", err)
	}
	observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	if err := observation.Validate(); err != nil {
		t.Fatalf("v1 observation with legacy toolProfileId must remain valid: %v", err)
	}
}

func TestEvaluateToolSelectionPairsRejectMismatchedImplementationRevision(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	filtered.ImplementationRevision = "other-revision"
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("revision mismatch must not pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if summary.PairedPromptTokenReduction != 0 || summary.PairedToolSchemaTokenReduction != 0 ||
		summary.PairedSchemaByteReduction != 0 {
		t.Fatalf("revision mismatch must not contribute paired reductions: %+v", summary)
	}
}

func TestEvaluateToolSelectionPairsRejectMismatchedModelProfileFingerprint(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	filtered.ModelProfileFingerprint = strings.Repeat("cd", 32)
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 || summary.PairedPromptTokenReduction != 0 {
		t.Fatalf("fingerprint mismatch must not pair or reduce: %+v", summary)
	}
}

func TestEvaluateToolSelectionDirtyObservationsNeverEnterPairedReduction(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	wide.ImplementationDirty = true
	filtered.ImplementationDirty = true
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("dirty observations must not pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if summary.PairedPromptTokenReduction != 0 || summary.PairedToolSchemaTokenReduction != 0 ||
		summary.PairedSchemaByteReduction != 0 {
		t.Fatalf("dirty observations must not contribute paired reductions: %+v", summary)
	}
	// 单臂统计仍然生成，供本地 smoke 使用。
	if summary.Wide.Runs != 1 || summary.Filtered.Runs != 1 || summary.Wide.Accuracy != 1 {
		t.Fatalf("dirty observations must still produce single-arm stats: %+v", summary)
	}
}

func TestEvaluateToolSelectionPairsMismatchedDirtyStates(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	wide.ImplementationDirty = true
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("mixed dirty states must not pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
}

func TestEvaluateToolSelectionPairsIdenticalCleanV2Observations(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV2ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV2ToolSelectionObservation(ToolSelectionFiltered, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.UnpairedRuns != 0 {
		t.Fatalf("identical clean v2 arms must pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 ||
		math.Abs(summary.PairedToolSchemaTokenReduction-(500.0/900.0)) > 1e-9 ||
		math.Abs(summary.PairedSchemaByteReduction-0.6) > 1e-9 {
		t.Fatalf("clean v2 pair reductions = %v/%v/%v",
			summary.PairedPromptTokenReduction, summary.PairedToolSchemaTokenReduction, summary.PairedSchemaByteReduction)
	}
}
