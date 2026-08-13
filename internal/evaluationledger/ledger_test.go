package evaluationledger_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestBuildReportPreservesDomainSummaryAndMissingUsage(t *testing.T) {
	t.Parallel()

	asset := evaluationledger.Asset{
		ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
		Status: evaluationledger.AssetRetestNeeded, Reason: "Tool catalog changed after the recorded run.",
		EntryPoint: "mesguard-tool-selection-eval", DatasetArtifact: "dataset.jsonl",
		ObservationArtifact: "observations.jsonl", ReportArtifact: "summary.json",
	}
	records := []evaluationledger.Record{
		{
			Domain: "tool_selection", DatasetVersion: "tool-selection-v1", CaseID: "case-1",
			Variant: "wide", RunID: "run-wide", Operation: "tool_selection", Outcome: evaluationledger.OutcomeSucceeded,
			ModelProvider: "stepfun", ModelID: "step-3.7-flash", ModelProfile: "stepfun-main",
			PromptVersion: "tool-selection-v1", ReasoningEffort: "low",
			Usage:          &evaluationledger.Usage{ModelCalls: 1, PromptTokens: 120, CompletionTokens: 5, TotalTokens: 125},
			DurationMillis: 250, ConfigFingerprint: "sha256:config", ImplementationRevision: "revision-1",
		},
		{
			Domain: "tool_selection", DatasetVersion: "tool-selection-v1", CaseID: "case-2",
			Variant: "filtered", RunID: "run-filtered", Operation: "tool_selection", Outcome: evaluationledger.OutcomeFailed,
			ModelProvider: "stepfun", ModelID: "step-3.7-flash", ModelProfile: "stepfun-main",
			PromptVersion: "tool-selection-v1", ReasoningEffort: "low", ErrorType: "missing_provider_usage",
			DurationMillis: 100, ConfigFingerprint: "sha256:config", ImplementationRevision: "revision-1",
		},
	}
	domainSummary := struct {
		Accuracy float64 `json:"accuracy"`
	}{Accuracy: 0.75}

	report, err := evaluationledger.BuildReport(asset, ledgerSourceForTest(), records, domainSummary)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if report.Summary.Runs != 2 || report.Summary.SucceededRuns != 1 || report.Summary.FailedRuns != 1 {
		t.Fatalf("summary outcomes = %+v", report.Summary)
	}
	if report.Summary.UsageAvailableRuns != 1 || report.Summary.UsageUnavailableRuns != 1 ||
		report.Summary.Usage.PromptTokens != 120 || report.Summary.Usage.TotalTokens != 125 {
		t.Fatalf("summary usage = %+v", report.Summary)
	}
	if len(report.Summary.UsageBreakdown) != 1 ||
		report.Summary.UsageBreakdown[0].ModelProvider != "stepfun" ||
		report.Summary.UsageBreakdown[0].ModelID != "step-3.7-flash" ||
		report.Summary.UsageBreakdown[0].Operation != "tool_selection" ||
		report.Summary.UsageBreakdown[0].UsageAvailableRuns != 1 ||
		report.Summary.UsageBreakdown[0].UsageUnavailableRuns != 1 {
		t.Fatalf("usage breakdown = %+v", report.Summary.UsageBreakdown)
	}
	var preserved map[string]float64
	if err := json.Unmarshal(report.DomainSummary, &preserved); err != nil {
		t.Fatalf("unmarshal domain summary: %v", err)
	}
	if preserved["accuracy"] != 0.75 {
		t.Fatalf("domain summary = %s", report.DomainSummary)
	}
	if report.Records[1].Usage != nil {
		t.Fatalf("missing provider usage was fabricated: %+v", report.Records[1].Usage)
	}
}

func TestRecordRejectsContradictoryOutcomeAndUsage(t *testing.T) {
	t.Parallel()

	base := evaluationledger.Record{
		Domain: "tool_selection", DatasetVersion: "tool-selection-v1", CaseID: "case-1",
		Variant: "wide", RunID: "run-1", Operation: "tool_selection", Outcome: evaluationledger.OutcomeSucceeded,
		ConfigFingerprint: "sha256:config", ImplementationRevision: "revision-1",
	}
	tests := []struct {
		name   string
		mutate func(*evaluationledger.Record)
		want   string
	}{
		{
			name: "successful record with error",
			mutate: func(record *evaluationledger.Record) {
				record.ErrorType = "provider_timeout"
			},
			want: "succeeded record cannot contain errorType",
		},
		{
			name: "tokens without model call",
			mutate: func(record *evaluationledger.Record) {
				record.Usage = &evaluationledger.Usage{PromptTokens: 10, TotalTokens: 10}
			},
			want: "token usage requires a positive modelCalls value",
		},
		{
			name: "model call without total tokens",
			mutate: func(record *evaluationledger.Record) {
				record.Usage = &evaluationledger.Usage{ModelCalls: 1}
			},
			want: "positive modelCalls requires positive totalTokens",
		},
		{
			name: "cached tokens exceed prompt tokens",
			mutate: func(record *evaluationledger.Record) {
				record.Usage = &evaluationledger.Usage{ModelCalls: 1, PromptTokens: 5, TotalTokens: 5, CachedTokens: 6}
			},
			want: "cachedTokens cannot exceed promptTokens",
		},
		{
			name: "reasoning tokens exceed completion tokens",
			mutate: func(record *evaluationledger.Record) {
				record.Usage = &evaluationledger.Usage{ModelCalls: 1, CompletionTokens: 5, TotalTokens: 5, ReasoningTokens: 6}
			},
			want: "reasoningTokens cannot exceed completionTokens",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			test.mutate(&record)
			if err := record.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestBuildReportRejectsUsageOverflow(t *testing.T) {
	t.Parallel()

	asset := evaluationledger.Asset{
		ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
		Status: evaluationledger.AssetRetestNeeded, Reason: "Selective retest required.",
		EntryPoint: "mesguard-tool-selection-eval",
	}
	maxInt := int(^uint(0) >> 1)
	record := evaluationledger.Record{
		Domain: "tool_selection", DatasetVersion: "tool-selection-v1", CaseID: "case-1",
		Variant: "wide", RunID: "run-1", Operation: "tool_selection", Outcome: evaluationledger.OutcomeSucceeded,
		Usage:             &evaluationledger.Usage{ModelCalls: 1, PromptTokens: maxInt, TotalTokens: maxInt},
		ConfigFingerprint: "sha256:config", ImplementationRevision: "revision-1",
	}
	second := record
	second.CaseID = "case-2"
	second.RunID = "run-2"
	second.Variant = "filtered"
	second.Usage = &evaluationledger.Usage{ModelCalls: 1, PromptTokens: 1, TotalTokens: 1}

	_, err := evaluationledger.BuildReport(asset, ledgerSourceForTest(), []evaluationledger.Record{record, second}, struct{}{})
	if err == nil || !strings.Contains(err.Error(), "usage promptTokens overflow") {
		t.Fatalf("BuildReport() error = %v", err)
	}
}

func TestBuildReportRejectsDuplicateAndConflictingRecords(t *testing.T) {
	t.Parallel()

	asset := evaluationledger.Asset{
		ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
		Status: evaluationledger.AssetRetestNeeded, Reason: "Selective retest required.",
		EntryPoint: "mesguard-tool-selection-eval",
	}
	base := evaluationledger.Record{
		Domain: "tool_selection", DatasetVersion: "tool-selection-v1", CaseID: "case-1",
		Variant: "wide", RunID: "run-1", Operation: "tool_selection", Outcome: evaluationledger.OutcomeSucceeded,
		ModelProvider: "stepfun", ModelID: "step-3.7-flash", ModelProfile: "stepfun-main",
		PromptVersion: "tool-selection-v1", ReasoningEffort: "low",
		Usage:             &evaluationledger.Usage{ModelCalls: 1, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		ConfigFingerprint: "sha256:config", ImplementationRevision: "revision-1",
	}

	t.Run("duplicate run id", func(t *testing.T) {
		duplicate := base
		duplicate.CaseID = "case-2"
		_, err := evaluationledger.BuildReport(asset, ledgerSourceForTest(), []evaluationledger.Record{base, duplicate}, struct{}{})
		if err == nil || !strings.Contains(err.Error(), `duplicate runId "run-1"`) {
			t.Fatalf("BuildReport() error = %v", err)
		}
	})

	t.Run("same case and variant with another run", func(t *testing.T) {
		conflict := base
		conflict.RunID = "run-2"
		_, err := evaluationledger.BuildReport(asset, ledgerSourceForTest(), []evaluationledger.Record{base, conflict}, struct{}{})
		if err == nil || !strings.Contains(err.Error(), `conflicting records "run-1" and "run-2"`) {
			t.Fatalf("BuildReport() error = %v", err)
		}
	})
}

func ledgerSourceForTest() evaluationledger.SourceMetadata {
	return evaluationledger.SourceMetadata{
		ModelProfile: "stepfun-main", ConfigFingerprint: "sha256:config",
		ImplementationRevision: "git:revision-1", DatasetSHA256: "sha256:dataset",
		ObservationSHA256: "sha256:observations",
	}
}
