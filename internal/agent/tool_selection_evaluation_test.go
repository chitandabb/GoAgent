package agent

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestEvaluateToolSelectionUsesProviderPromptTokensForPairedReduction(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.Wide.Accuracy != 1 || summary.Production.Accuracy != 1 {
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
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "")
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

// validV4ToolSelectionObservation 构造通过 v4 Validate 的完整观测。两个
// 实验臂使用各自正确的 Profile 合同（wide=评测合同 evaluation-wide-v2，
// production=生产 diagnosis-default），fingerprint/revision 相同以便默认
// 可以配对。
func validV4ToolSelectionObservation(
	variant ToolSelectionVariant,
	runID string,
	promptTokens, schemaBytes int,
	selected string,
) ToolSelectionObservation {
	observation := validToolSelectionObservation(variant, runID, promptTokens, schemaBytes, selected)
	observation.ObservationSchemaVersion = ToolSelectionObservationV4
	observation.ToolChoiceMode = ToolSelectionToolChoiceRequired
	observation.ToolProfileID = ToolSelectionEvaluationWideProfile
	if variant == ToolSelectionProduction {
		observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	}
	observation.ModelVisibleNames = []string{"search_code"}
	observation.ModelProfileFingerprint = strings.Repeat("ab", 32)
	observation.ImplementationRevision = "abc123"
	observation.ImplementationDirty = false
	observation.ComparisonFingerprint = "sha256:" + strings.Repeat("cd", 32)
	observation.SharedToolNames = []string{"search_code"}
	observation.BaselineOnlyToolNames = []string{"read_external_case"}
	if variant == ToolSelectionWide {
		observation.AvailableTools = []string{"search_code", "read_external_case"}
		observation.ModelVisibleNames = append([]string(nil), observation.AvailableTools...)
	}
	return observation
}

func TestToolSelectionObservationV4FilteredRequiresDiagnosisProfile(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	observation.ToolProfileID = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("v4 production observation without toolProfileId must fail")
	}
	observation.ToolProfileID = ToolSelectionEvaluationWideProfile
	if err := observation.Validate(); err == nil {
		t.Fatal("v4 production observation with the wide contract id must fail")
	}
}

func TestToolSelectionObservationV4WideRejectsDiagnosisProfile(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	if err := observation.Validate(); err == nil {
		t.Fatal("v4 wide observation must not masquerade as diagnosis-default")
	}
	observation.ToolProfileID = ""
	if err := observation.Validate(); err == nil {
		t.Fatal("v4 wide observation without toolProfileId must fail")
	}
}

func TestToolSelectionObservationV4RequiresFullIdentity(t *testing.T) {
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
			observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
			current.mutate(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatalf("v4 observation with %s must fail", current.name)
			}
		})
	}
	if err := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code").Validate(); err != nil {
		t.Fatalf("complete v4 observation must pass: %v", err)
	}
}

// TestToolSelectionObservationV4RejectsHistoricalContracts proves the v4 hard
// cut: historical v1 (no observationSchemaVersion), v2 and v3 observations are
// rejected; no compatibility branch is kept.
func TestToolSelectionObservationV4RejectsHistoricalContracts(t *testing.T) {
	v1 := validToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	if err := v1.Validate(); err == nil {
		t.Fatal("historical v1 observation (no schema version) must be rejected")
	}
	if !strings.Contains(v1.Validate().Error(), ToolSelectionObservationV4) {
		t.Fatalf("v1 rejection must name the active v4 contract: %v", v1.Validate())
	}

	v2 := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	v2.ObservationSchemaVersion = "tool-selection-observation-v2"
	if err := v2.Validate(); err == nil {
		t.Fatal("historical v2 observation must be rejected")
	}
	if !strings.Contains(v2.Validate().Error(), "historical") {
		t.Fatalf("v2 rejection must mark the contract historical: %v", v2.Validate())
	}

	v3 := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	v3.ObservationSchemaVersion = "tool-selection-observation-v3"
	if err := v3.Validate(); err == nil {
		t.Fatal("historical v3 observation must be rejected by the v4 contract")
	}
	if !strings.Contains(v3.Validate().Error(), "historical") {
		t.Fatalf("v3 rejection must mark the contract historical: %v", v3.Validate())
	}

	unknown := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	unknown.ObservationSchemaVersion = "tool-selection-observation-v9"
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown observationSchemaVersion must be rejected")
	}
}

// TestToolSelectionObservationV4ToolChoiceModeContract proves toolChoiceMode is
// a mandatory v4 identity field: only required and absent are legal; missing,
// empty or arbitrary values (including provider-style "auto"/"forced") are
// rejected.
func TestToolSelectionObservationV4ToolChoiceModeContract(t *testing.T) {
	for _, mode := range []ToolSelectionToolChoiceMode{
		ToolSelectionToolChoiceRequired, ToolSelectionToolChoiceAbsent,
	} {
		observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
		observation.ToolChoiceMode = mode
		if err := observation.Validate(); err != nil {
			t.Fatalf("v4 observation with toolChoiceMode %q must validate: %v", mode, err)
		}
	}
	for _, mode := range []ToolSelectionToolChoiceMode{"", "auto", "forced", "none", "Required"} {
		observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
		observation.ToolChoiceMode = mode
		if err := observation.Validate(); err == nil {
			t.Fatalf("v4 observation with toolChoiceMode %q must be rejected", mode)
		}
	}
}

// TestParseToolSelectionToolChoiceMode proves the CLI seam: only the exact
// strings required/absent parse; anything else fails closed with both legal
// values named.
func TestParseToolSelectionToolChoiceMode(t *testing.T) {
	for value, want := range map[string]ToolSelectionToolChoiceMode{
		"required": ToolSelectionToolChoiceRequired, "absent": ToolSelectionToolChoiceAbsent,
	} {
		mode, err := ParseToolSelectionToolChoiceMode(value)
		if err != nil || mode != want {
			t.Fatalf("ParseToolSelectionToolChoiceMode(%q) = %q, %v; want %q", value, mode, err, want)
		}
	}
	for _, value := range []string{"", "auto", "forced", "REQUIRED", " absent"} {
		if _, err := ParseToolSelectionToolChoiceMode(value); err == nil {
			t.Fatalf("ParseToolSelectionToolChoiceMode(%q) must fail closed", value)
		}
	}
}

// TestEvaluateToolSelectionPairsRejectMismatchedToolChoiceMode proves arms
// recorded under different tool-choice request modes never pair and never
// contribute paired reductions.
func TestEvaluateToolSelectionPairsRejectMismatchedToolChoiceMode(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	wide.ToolChoiceMode = ToolSelectionToolChoiceAbsent
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("tool choice mode mismatch must not pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if summary.PairedPromptTokenReduction != 0 || summary.PairedToolSchemaTokenReduction != 0 ||
		summary.PairedSchemaByteReduction != 0 {
		t.Fatalf("tool choice mode mismatch must not contribute paired reductions: %+v", summary)
	}
}

// TestEvaluateToolSelectionPairsIdenticalAbsentMode proves two arms recorded
// under the same absent mode still pair normally.
func TestEvaluateToolSelectionPairsIdenticalAbsentMode(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	wide.ToolChoiceMode = ToolSelectionToolChoiceAbsent
	filtered.ToolChoiceMode = ToolSelectionToolChoiceAbsent
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.UnpairedRuns != 0 || math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 {
		t.Fatalf("identical absent-mode arms must pair: %+v", summary)
	}
}

// TestToolSelectionObservationV4RejectsFilteredVariant proves the filtered
// naming is retired: the variant contract only accepts wide/production.
func TestToolSelectionObservationV4RejectsFilteredVariant(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	observation.Variant = ToolSelectionVariant("filtered")
	if err := observation.Validate(); err == nil {
		t.Fatal("legacy filtered variant must be rejected")
	}
	observation.Variant = ToolSelectionVariant("production")
	if err := observation.Validate(); err != nil {
		t.Fatalf("production variant must validate: %v", err)
	}
}

// TestToolSelectionObservationV4RequiresComparisonIdentity proves the v4
// comparison identity fields are mandatory: comparisonFingerprint,
// sharedToolNames and baselineOnlyToolNames.
func TestToolSelectionObservationV4RequiresComparisonIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ToolSelectionObservation)
	}{
		{name: "missing comparison fingerprint", mutate: func(o *ToolSelectionObservation) { o.ComparisonFingerprint = "" }},
		{name: "malformed comparison fingerprint", mutate: func(o *ToolSelectionObservation) { o.ComparisonFingerprint = "not-a-sha256" }},
		{name: "missing shared tool names", mutate: func(o *ToolSelectionObservation) { o.SharedToolNames = nil }},
		{name: "empty shared tool names", mutate: func(o *ToolSelectionObservation) { o.SharedToolNames = []string{} }},
		{name: "invalid shared tool name", mutate: func(o *ToolSelectionObservation) {
			o.SharedToolNames = []string{"Invalid Name"}
		}},
		{name: "duplicate shared tool name", mutate: func(o *ToolSelectionObservation) {
			o.SharedToolNames = []string{"search_code", "search_code"}
		}},
		{name: "missing baseline-only tool names", mutate: func(o *ToolSelectionObservation) { o.BaselineOnlyToolNames = nil }},
		{name: "invalid baseline-only tool name", mutate: func(o *ToolSelectionObservation) {
			o.BaselineOnlyToolNames = []string{"Invalid Name"}
		}},
		{name: "baseline-only overlaps shared", mutate: func(o *ToolSelectionObservation) {
			o.BaselineOnlyToolNames = []string{"search_code"}
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
			current.mutate(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatalf("v4 observation with %s must fail", current.name)
			}
		})
	}
	if err := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code").Validate(); err != nil {
		t.Fatalf("complete v4 observation must pass: %v", err)
	}
}

// TestToolSelectionObservationV4AllowsEmptyReasoningEffort：基础校验不再强制
// ReasoningEffort 非空，完整 v2 Observation 可以 Validate。
func TestToolSelectionObservationV4AllowsEmptyReasoningEffort(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	observation.ReasoningEffort = ""
	if err := observation.Validate(); err != nil {
		t.Fatalf("v4 observation with empty reasoningEffort must validate: %v", err)
	}
}

// TestToolSelectionObservationV4UnknownDirtyValidatesButNeverPairs：
// unknown+dirty 允许 Validate（本地 smoke），但禁止进入正式 paired
// reduction。
func TestToolSelectionObservationV4UnknownDirtyValidatesButNeverPairs(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	observation.ImplementationRevision = "unknown"
	observation.ImplementationDirty = true
	if err := observation.Validate(); err != nil {
		t.Fatalf("v4 unknown+dirty observation must validate for local smoke: %v", err)
	}
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	wide.ImplementationRevision = "unknown"
	wide.ImplementationDirty = true
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
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

// TestToolSelectionObservationV4UnknownCleanRejected：revision=unknown 且
// ImplementationDirty=false 必须拒绝。
func TestToolSelectionObservationV4UnknownCleanRejected(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	observation.ImplementationRevision = "unknown"
	observation.ImplementationDirty = false
	if err := observation.Validate(); err == nil {
		t.Fatal("v4 unknown+clean observation must be rejected")
	}
}

// TestToolSelectionObservationV4EmptyRevisionRejected：空 revision 必须拒绝。
func TestToolSelectionObservationV4EmptyRevisionRejected(t *testing.T) {
	for _, revision := range []string{"", "   "} {
		observation := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
		observation.ImplementationRevision = revision
		if err := observation.Validate(); err == nil {
			t.Fatalf("v4 observation with revision %q must be rejected", revision)
		}
	}
}

// TestToolSelectionObservationV4RejectsProductionComparisonThatDoesNotMatchActualTools
// locks the observation seam: comparison metadata cannot describe a stale or
// different production schema than the tools actually sent to the model.
func TestToolSelectionObservationV4RejectsProductionComparisonThatDoesNotMatchActualTools(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionProduction, "production", 500, 400, "search_code")
	observation.AvailableTools = []string{"search_code", "unexpected_tool"}
	observation.ModelVisibleNames = append([]string(nil), observation.AvailableTools...)

	if err := observation.Validate(); err == nil {
		t.Fatal("production observation whose actual tools differ from sharedToolNames must be rejected")
	}
}

// TestToolSelectionObservationV4RejectsWideComparisonThatDoesNotMatchActualTools
// requires the wide arm to contain exactly the shared plus baseline-only Tool
// names recorded by the comparison contract.
func TestToolSelectionObservationV4RejectsWideComparisonThatDoesNotMatchActualTools(t *testing.T) {
	observation := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	observation.AvailableTools = []string{"search_code", "unexpected_tool"}
	observation.ModelVisibleNames = append([]string(nil), observation.AvailableTools...)

	if err := observation.Validate(); err == nil {
		t.Fatal("wide observation whose actual tools differ from the comparison union must be rejected")
	}
}

func TestToolSelectionObservationV1RemainsReplayableWithoutNewFields(t *testing.T) {
	observation := validToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	if err := observation.Validate(); err == nil {
		t.Fatalf("v1 observation without schema version must be rejected by the v4 contract: %v", err)
	}
	observation.ToolProfileID = string(agentruntime.ToolProfileDiagnosis)
	if err := observation.Validate(); err == nil {
		t.Fatal("v1 observation with legacy toolProfileId must still be rejected")
	}
}

func TestEvaluateToolSelectionPairsRejectMismatchedImplementationRevision(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
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
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
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
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
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
	if summary.Wide.Runs != 1 || summary.Production.Runs != 1 || summary.Wide.Accuracy != 1 {
		t.Fatalf("dirty observations must still produce single-arm stats: %+v", summary)
	}
}

func TestEvaluateToolSelectionPairsMismatchedDirtyStates(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	wide.ImplementationDirty = true
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 {
		t.Fatalf("mixed dirty states must not pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
}

func TestEvaluateToolSelectionPairsIdenticalCleanV4Observations(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 1 || summary.UnpairedRuns != 0 {
		t.Fatalf("identical clean v4 arms must pair: paired=%d unpaired=%d", summary.PairedCases, summary.UnpairedRuns)
	}
	if math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 ||
		math.Abs(summary.PairedToolSchemaTokenReduction-(500.0/900.0)) > 1e-9 ||
		math.Abs(summary.PairedSchemaByteReduction-0.6) > 1e-9 {
		t.Fatalf("clean v4 pair reductions = %v/%v/%v",
			summary.PairedPromptTokenReduction, summary.PairedToolSchemaTokenReduction, summary.PairedSchemaByteReduction)
	}
}

// TestEvaluateToolSelectionPairsRejectComparisonFingerprintDrift proves that
// arms with a drifted comparisonFingerprint never enter paired reduction.
func TestEvaluateToolSelectionPairsRejectComparisonFingerprintDrift(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	filtered.ComparisonFingerprint = "sha256:" + strings.Repeat("ff", 32)
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 ||
		summary.PairedPromptTokenReduction != 0 || summary.PairedToolSchemaTokenReduction != 0 {
		t.Fatalf("comparison fingerprint drift must not pair or reduce: %+v", summary)
	}
}

// TestEvaluateToolSelectionPairsRejectBaselineOnlyDrift proves that arms with
// a drifted baseline-only Tool list never enter paired reduction.
func TestEvaluateToolSelectionPairsRejectBaselineOnlyDrift(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	filtered := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	filtered.BaselineOnlyToolNames = []string{"another_tool"}
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, filtered})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 || summary.PairedPromptTokenReduction != 0 {
		t.Fatalf("baseline-only drift must not pair or reduce: %+v", summary)
	}
}

// TestEvaluateToolSelectionPairsRejectSharedToolNamesDrift proves that arms
// with a drifted shared Tool list never enter paired reduction.
func TestEvaluateToolSelectionPairsRejectSharedToolNamesDrift(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	production := validV4ToolSelectionObservation(ToolSelectionProduction, "production", 500, 400, "search_code")
	production.SharedToolNames = []string{"search_code", "web_search"}
	production.AvailableTools = append([]string(nil), production.SharedToolNames...)
	production.ModelVisibleNames = append([]string(nil), production.AvailableTools...)
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, production})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.PairedCases != 0 || summary.UnpairedRuns != 2 || summary.PairedPromptTokenReduction != 0 {
		t.Fatalf("shared tool names drift must not pair or reduce: %+v", summary)
	}
}

// comparabilitySkillRuntimeForTest builds the real Eino Skill Middleware host
// (same Skill root as the production Diagnosis Runner).
func comparabilitySkillRuntimeForTest(t *testing.T) *NativeSkillRuntime {
	t.Helper()
	runtime, err := NewNativeSkillRuntime(context.Background(), filepath.Join("..", "..", "config", "skills"))
	if err != nil {
		t.Fatalf("NewNativeSkillRuntime: %v", err)
	}
	return runtime
}

// assembleComparabilityArm turns a Catalog into the final model-visible
// ToolInfo list through the real assembly chain: ToolCatalog ->
// ToolAuthorizationMiddleware -> (optional) Eino Skill Middleware. It never
// fabricates a Schema.
func assembleComparabilityArm(
	t *testing.T,
	catalog *ToolCatalog,
	skillRuntime *NativeSkillRuntime,
	withSkill bool,
) []*schema.ToolInfo {
	t.Helper()
	authorization, err := NewToolAuthorizationMiddleware(catalog, catalog.BoundProfileID())
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware(%s): %v", catalog.BoundProfileID(), err)
	}
	accessCtx := agentruntime.WithRunAccess(context.Background(), runnerTestAccess(t,
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead,
		agentruntime.PermissionAttachmentRead, agentruntime.PermissionWebRead,
	))
	_, authorizedCtx, authErr := authorization.BeforeAgent(accessCtx, &adk.ChatModelAgentContext{Tools: nil})
	if authErr != nil {
		t.Fatalf("BeforeAgent(authorization): %v", authErr)
	}
	finalCtx := authorizedCtx
	if withSkill {
		_, withSkillCtx, skillErr := skillRuntime.Middleware.BeforeAgent(accessCtx, authorizedCtx)
		if skillErr != nil {
			t.Fatalf("BeforeAgent(skill): %v", skillErr)
		}
		finalCtx = withSkillCtx
	}
	infos := make([]*schema.ToolInfo, 0, len(finalCtx.Tools))
	for _, current := range finalCtx.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		infos = append(infos, info)
	}
	return infos
}

// comparabilityWideCatalogForTest builds the wide Catalog bound to
// evaluation-wide-v2 through the single wide assembly entry point.
func comparabilityWideCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	return mustDefaultToolCatalogForProfile(t, NewEvaluationWideDefaultToolCatalog)
}

func comparabilityArmNames(t *testing.T, infos []*schema.ToolInfo) []string {
	t.Helper()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	slices.Sort(names)
	return names
}

func countComparabilitySkill(infos []*schema.ToolInfo) int {
	count := 0
	for _, info := range infos {
		if info.Name == ToolSkill {
			count++
		}
	}
	return count
}

// TestVerifyToolSelectionComparabilityAcceptsRealAssembly proves through real
// assembly that the evaluation-wide-v2 arm is a strict Schema superset of the
// production (diagnosis-default) arm: production subset of wide, wide has at
// least one extra Tool, shared Tool Schemas are identical, skill appears
// exactly once per arm, no duplicates, and the fingerprint is stable.
func TestVerifyToolSelectionComparabilityAcceptsRealAssembly(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := assembleComparabilityArm(t, comparabilityWideCatalogForTest(t), skillRuntime, true)

	comparability, err := VerifyToolSelectionComparability(productionInfos, wideInfos)
	if err != nil {
		t.Fatalf("VerifyToolSelectionComparability(real assembly): %v", err)
	}
	productionNames := comparabilityArmNames(t, productionInfos)
	wideNames := comparabilityArmNames(t, wideInfos)

	for _, name := range productionNames {
		if !slices.Contains(wideNames, name) {
			t.Fatalf("production Tool %q is missing from the wide arm: %v", name, wideNames)
		}
	}
	if len(wideNames) <= len(productionNames) {
		t.Fatalf("wide arm must be a strict superset: production=%d wide=%d", len(productionNames), len(wideNames))
	}

	if count := countComparabilitySkill(productionInfos); count != 1 {
		t.Fatalf("production arm contains skill %d times, want exactly 1", count)
	}
	if count := countComparabilitySkill(wideInfos); count != 1 {
		t.Fatalf("wide arm contains skill %d times, want exactly 1", count)
	}
	if !slices.Contains(comparability.SharedToolNames, ToolSkill) {
		t.Fatalf("sharedToolNames must include skill: %v", comparability.SharedToolNames)
	}
	if !slices.Equal(comparability.SharedToolNames, productionNames) {
		t.Fatalf("sharedToolNames = %v, want production names %v", comparability.SharedToolNames, productionNames)
	}

	var wantBaselineOnly []string
	for _, name := range wideNames {
		if !slices.Contains(productionNames, name) {
			wantBaselineOnly = append(wantBaselineOnly, name)
		}
	}
	if !slices.Equal(comparability.BaselineOnlyToolNames, wantBaselineOnly) {
		t.Fatalf("baselineOnlyToolNames = %v, want %v", comparability.BaselineOnlyToolNames, wantBaselineOnly)
	}

	again, err := VerifyToolSelectionComparability(productionInfos, wideInfos)
	if err != nil {
		t.Fatalf("VerifyToolSelectionComparability(second): %v", err)
	}
	if again.ComparisonFingerprint != comparability.ComparisonFingerprint {
		t.Fatalf("comparison fingerprint is not stable: %q vs %q",
			again.ComparisonFingerprint, comparability.ComparisonFingerprint)
	}
	if !strings.HasPrefix(comparability.ComparisonFingerprint, "sha256:") {
		t.Fatalf("comparison fingerprint must use the sha256: prefix, got %q", comparability.ComparisonFingerprint)
	}
}

// TestVerifyToolSelectionComparabilityRejectsWideWithoutSkillMiddleware
// proves that a wide arm that did not pass the same Skill Middleware as the
// production arm (missing skill) is rejected.
func TestVerifyToolSelectionComparabilityRejectsWideWithoutSkillMiddleware(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := assembleComparabilityArm(t, comparabilityWideCatalogForTest(t), skillRuntime, false)
	_, err := VerifyToolSelectionComparability(productionInfos, wideInfos)
	if err == nil {
		t.Fatal("wide arm without the Skill Middleware must be rejected")
	}
	if !strings.Contains(err.Error(), ToolSkill) {
		t.Fatalf("rejection must mention the missing skill Tool: %v", err)
	}
}

// TestVerifyToolSelectionComparabilityRejectsSharedSchemaDrift proves that
// identical names with drifted canonical Schemas are rejected: comparing only
// Tool counts cannot catch Schema drift.
func TestVerifyToolSelectionComparabilityRejectsSharedSchemaDrift(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := assembleComparabilityArm(t, comparabilityWideCatalogForTest(t), skillRuntime, true)

	drifted := make([]*schema.ToolInfo, 0, len(productionInfos))
	for _, info := range productionInfos {
		clone := *info
		if info.Name == ToolReadExternalCase {
			clone.Desc = "drifted shared schema description"
		}
		drifted = append(drifted, &clone)
	}
	_, err := VerifyToolSelectionComparability(drifted, wideInfos)
	if err == nil {
		t.Fatal("shared Tool schema drift must be rejected")
	}
	if !strings.Contains(err.Error(), ToolReadExternalCase) {
		t.Fatalf("rejection must name the drifted Tool: %v", err)
	}
}

// TestVerifyToolSelectionComparabilityRejectsEqualArms proves that a wide arm
// equal to production (not a strict superset) is rejected.
func TestVerifyToolSelectionComparabilityRejectsEqualArms(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := append([]*schema.ToolInfo(nil), productionInfos...)
	if _, err := VerifyToolSelectionComparability(productionInfos, wideInfos); err == nil {
		t.Fatal("wide arm equal to production must be rejected (strict superset required)")
	}
}

// TestVerifyToolSelectionComparabilityRejectsWideMissingProductionTool proves
// that a wide arm missing any production Tool is rejected.
func TestVerifyToolSelectionComparabilityRejectsWideMissingProductionTool(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := assembleComparabilityArm(t, comparabilityWideCatalogForTest(t), skillRuntime, true)

	missing := make([]*schema.ToolInfo, 0, len(wideInfos))
	removed := ""
	for _, info := range wideInfos {
		if removed == "" && info.Name == ToolReadExternalCase {
			removed = info.Name
			continue
		}
		missing = append(missing, info)
	}
	_, err := VerifyToolSelectionComparability(productionInfos, missing)
	if err == nil {
		t.Fatal("wide arm missing a production Tool must be rejected")
	}
	if !strings.Contains(err.Error(), removed) {
		t.Fatalf("rejection must name the missing Tool %q: %v", removed, err)
	}
}

// TestVerifyToolSelectionComparabilityRejectsDuplicateSkill proves that a
// duplicated skill Tool is rejected (Middleware must append exactly once).
func TestVerifyToolSelectionComparabilityRejectsDuplicateSkill(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	wideInfos := assembleComparabilityArm(t, comparabilityWideCatalogForTest(t), skillRuntime, true)

	var skillInfo *schema.ToolInfo
	for _, info := range wideInfos {
		if info.Name == ToolSkill {
			skillInfo = info
			break
		}
	}
	if skillInfo == nil {
		t.Fatal("wide arm has no skill ToolInfo")
	}
	duplicated := append(append([]*schema.ToolInfo(nil), wideInfos...), skillInfo)
	if _, err := VerifyToolSelectionComparability(productionInfos, duplicated); err == nil {
		t.Fatal("duplicate skill Tool must be rejected")
	}
}

// TestVerifyToolSelectionComparabilityRejectsEmptyArms proves that empty arms
// are rejected.
func TestVerifyToolSelectionComparabilityRejectsEmptyArms(t *testing.T) {
	skillRuntime := comparabilitySkillRuntimeForTest(t)
	productionInfos := assembleComparabilityArm(t, mustDiagnosisConfiguredDefaultCatalogForTest(t), skillRuntime, true)
	if _, err := VerifyToolSelectionComparability(nil, productionInfos); err == nil {
		t.Fatal("empty production arm must be rejected")
	}
	if _, err := VerifyToolSelectionComparability(productionInfos, nil); err == nil {
		t.Fatal("empty wide arm must be rejected")
	}
}

// failedToolSelectionObservationForTest 构造失败臂观测：ErrorType 非空时
// 允许零 Usage（v4 合同），token 校准字段归零。
func failedToolSelectionObservationForTest(
	variant ToolSelectionVariant, runID, caseID, errorType string,
) ToolSelectionObservation {
	observation := validV4ToolSelectionObservation(variant, runID, 0, 100, "")
	observation.CaseID = caseID
	observation.ErrorType = errorType
	observation.Usage = ModelUsage{}
	observation.SelectedTool = ""
	observation.ToolCallCount = 0
	observation.BasePromptTokens = 0
	observation.ToolSchemaPromptTokens = 0
	return observation
}

// TestEvaluateToolSelectionProviderAccounting proves the independent
// ProviderAccounting block: endpoint attempts cover calibration + both arms,
// reported Usage accumulates exactly once per attempt, failed attempts count
// as usage-missing without any token estimation, and calibration never leaks
// into arm metrics or paired reduction.
func TestEvaluateToolSelectionProviderAccounting(t *testing.T) {
	cases := []ToolSelectionCase{
		{DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
			UserQuery: "查找代码", ExpectedTool: "search_code"},
		{DatasetVersion: "tools-v1", CaseID: "case-2", Scope: ToolSelectionGitHub,
			UserQuery: "再次查找代码", ExpectedTool: "search_code"},
	}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	production := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	failed := failedToolSelectionObservationForTest(ToolSelectionProduction, "failed-rate-limited", "case-2", "provider_rate_limited")
	calibration := []ModelUsage{
		{ModelCalls: 1, PromptTokens: 800, CompletionTokens: 20, TotalTokens: 820},
		{ModelCalls: 1, PromptTokens: 790, CompletionTokens: 10, TotalTokens: 800},
	}
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, production, failed}, calibration)
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	acc := summary.ProviderAccounting
	if acc.ModelGenerateAttempts != 5 {
		t.Fatalf("modelGenerateAttempts = %d, want 5 (2 calibration + 3 arm attempts)", acc.ModelGenerateAttempts)
	}
	if acc.UsageReportedAttempts != 4 {
		t.Fatalf("usageReportedAttempts = %d, want 4", acc.UsageReportedAttempts)
	}
	if acc.UsageMissingAttempts != 1 {
		t.Fatalf("usageMissingAttempts = %d, want 1 (failed rate-limited attempt)", acc.UsageMissingAttempts)
	}
	if acc.PromptTokens != 800+790+1000+500 || acc.CompletionTokens != 20+10+10+10 ||
		acc.TotalTokens != 820+800+1010+510 {
		t.Fatalf("accumulated usage = %+v, want 3090/50/3140", acc)
	}
	// 校准绝不混入两臂指标与 paired reduction。
	if summary.Wide.PromptTokens != 1000 || summary.Production.PromptTokens != 500 {
		t.Fatalf("arm prompt tokens polluted by calibration: wide=%d production=%d",
			summary.Wide.PromptTokens, summary.Production.PromptTokens)
	}
	if summary.PairedCases != 1 || math.Abs(summary.PairedPromptTokenReduction-0.5) > 1e-9 {
		t.Fatalf("paired reduction polluted by calibration: %+v", summary)
	}
	if summary.UnpairedRuns != 1 {
		t.Fatalf("unpairedRuns = %d, want 1 (case-2 has production only)", summary.UnpairedRuns)
	}
	if summary.Production.FailedRuns != 1 {
		t.Fatalf("production failedRuns = %d, want 1", summary.Production.FailedRuns)
	}
	if summary.FailureTypes["provider_rate_limited"] != 1 {
		t.Fatalf("failureTypes = %+v, want provider_rate_limited=1", summary.FailureTypes)
	}
}

// TestEvaluateToolSelectionProviderAccountingLegacyModelError proves the
// historical model_error category stays readable and counts as a
// usage-missing attempt without any schema/identity change.
func TestEvaluateToolSelectionProviderAccountingLegacyModelError(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	failed := failedToolSelectionObservationForTest(ToolSelectionProduction, "legacy-failure", "case-1", "model_error")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, failed})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	if summary.FailureTypes["model_error"] != 1 {
		t.Fatalf("historical model_error must stay readable: %+v", summary.FailureTypes)
	}
	acc := summary.ProviderAccounting
	if acc.ModelGenerateAttempts != 2 || acc.UsageReportedAttempts != 1 || acc.UsageMissingAttempts != 1 {
		t.Fatalf("accounting = %+v, want attempts=2 reported=1 missing=1", acc)
	}
	if acc.PromptTokens != 1000 {
		t.Fatalf("failed attempt must not estimate tokens: %+v", acc)
	}
}

// TestEvaluateToolSelectionProviderAccountingWithoutCalibration proves the
// two-argument call shape still works: accounting derives from observations
// only and stays zero-valued for reported/missing breakdown.
func TestEvaluateToolSelectionProviderAccountingWithoutCalibration(t *testing.T) {
	cases := []ToolSelectionCase{{
		DatasetVersion: "tools-v1", CaseID: "case-1", Scope: ToolSelectionGitHub,
		UserQuery: "查找代码", ExpectedTool: "search_code",
	}}
	wide := validV4ToolSelectionObservation(ToolSelectionWide, "wide", 1000, 1000, "search_code")
	production := validV4ToolSelectionObservation(ToolSelectionProduction, "filtered", 500, 400, "search_code")
	summary, err := EvaluateToolSelection(cases, []ToolSelectionObservation{wide, production})
	if err != nil {
		t.Fatalf("EvaluateToolSelection(): %v", err)
	}
	acc := summary.ProviderAccounting
	if acc.ModelGenerateAttempts != 2 || acc.UsageReportedAttempts != 2 || acc.UsageMissingAttempts != 0 {
		t.Fatalf("accounting = %+v, want attempts=2 reported=2 missing=0", acc)
	}
	if acc.PromptTokens != 1500 {
		t.Fatalf("accumulated prompt tokens = %d, want 1500", acc.PromptTokens)
	}
}
