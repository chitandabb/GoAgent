package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"go.uber.org/zap"
)

func TestReadCasesLoadsVersionedDataset(t *testing.T) {
	datasetPath := writeEvaluationDataset(t)

	cases, err := readCases(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("cases = %d, want 3", len(cases))
	}
	if cases[0].CaseID != "evidence-gap-search" || cases[2].Scenario != mesagent.AgenticScenarioValid {
		t.Fatalf("unexpected cases: %+v", cases)
	}
}

func TestRunRequiresExplicitProviderAuthorization(t *testing.T) {
	err := run(context.Background(), []string{
		"-dataset", writeEvaluationDataset(t),
		"-output", filepath.Join(t.TempDir(), "observations.jsonl"),
		"-summary", filepath.Join(t.TempDir(), "summary.json"),
	}, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "provider execution is disabled") {
		t.Fatalf("run error = %v", err)
	}
}

func TestParseOptionsRejectsSharedOutputPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	_, err := parseOptions([]string{"-output", path, "-summary", path})
	if err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("parseOptions error = %v", err)
	}
}

func TestParseOptionsUsesProductionTokenBudget(t *testing.T) {
	options, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.maxTotalTokens != 16_000 {
		t.Fatalf("maxTotalTokens = %d, want 16000", options.maxTotalTokens)
	}
}

func TestSeededRunResultBuildsScenarioSpecificFirstPass(t *testing.T) {
	tests := []struct {
		name              string
		scenario          mesagent.AgenticRetrievalScenario
		wantEvidence      int
		wantEvidenceItems int
		wantConfidence    mesagent.ConfidenceLevel
	}{
		{name: "evidence gap", scenario: mesagent.AgenticScenarioEvidenceGap, wantConfidence: mesagent.ConfidenceMedium},
		{name: "format only", scenario: mesagent.AgenticScenarioFormatOnly, wantEvidence: 1, wantEvidenceItems: 1, wantConfidence: mesagent.ConfidenceLevel("certain")},
		{name: "valid first pass", scenario: mesagent.AgenticScenarioValid, wantEvidence: 1, wantEvidenceItems: 1, wantConfidence: mesagent.ConfidenceMedium},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := seededRunResult(mesagent.AgenticRetrievalEvaluationCase{Scenario: test.scenario})
			if err != nil {
				t.Fatal(err)
			}
			var report mesagent.StructuredReport
			if err := json.Unmarshal([]byte(result.Answer), &report); err != nil {
				t.Fatalf("decode report: %v", err)
			}
			if len(report.Evidence) != test.wantEvidence || len(result.EvidenceItems) != test.wantEvidenceItems {
				t.Fatalf("evidence = %d, evidence items = %d", len(report.Evidence), len(result.EvidenceItems))
			}
			if report.Confidence != test.wantConfidence {
				t.Fatalf("confidence = %q, want %q", report.Confidence, test.wantConfidence)
			}
		})
	}
}

func writeEvaluationDataset(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentic-retrieval.jsonl")
	contents := strings.Join([]string{
		`{"datasetVersion":"agentic-retrieval-v1","caseId":"evidence-gap-search","scenario":"evidence_gap","userQuery":"补充企业知识证据","expectedAttempted":true,"expectedAddedEvidence":true,"expectedStopReason":"new_evidence_added"}`,
		`{"datasetVersion":"agentic-retrieval-v1","caseId":"format-only-no-search","scenario":"format_only","userQuery":"只修正报告格式","expectedAttempted":false,"expectedAddedEvidence":false,"expectedStopReason":"not_eligible"}`,
		`{"datasetVersion":"agentic-retrieval-v1","caseId":"valid-first-pass-no-search","scenario":"valid_first_pass","userQuery":"直接输出最终结论","expectedAttempted":false,"expectedAddedEvidence":false,"expectedStopReason":"not_needed"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
