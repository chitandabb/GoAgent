package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestRunReplaysToolSelectionWithoutProviderAndRefusesOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inventoryPath := filepath.Join(dir, "inventory.json")
	datasetPath := filepath.Join(dir, "dataset.jsonl")
	observationsPath := filepath.Join(dir, "observations.jsonl")
	outputPath := filepath.Join(dir, "ledger.json")
	writeJSONFile(t, inventoryPath, evaluationledger.Inventory{
		SchemaVersion: evaluationledger.InventorySchemaVersion, ArtifactRoot: ".",
		Assets: []evaluationledger.Asset{{
			ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
			Status: evaluationledger.AssetRetestNeeded, Reason: "Current Tool contracts require a selective retest.",
			EntryPoint: "mesguard-tool-selection-eval", DatasetArtifact: "dataset.jsonl",
			ObservationArtifact: "observations.jsonl", ReportArtifact: "summary.json",
		}},
	})
	writeJSONLines(t, datasetPath, []mesagent.ToolSelectionCase{{
		DatasetVersion: "tool-selection-v1", CaseID: "case-1", Scope: mesagent.ToolSelectionTicket,
		UserQuery: "读取工单", ExpectedTool: mesagent.ToolReadExternalCase,
	}})
	writeJSONLines(t, observationsPath, []mesagent.ToolSelectionObservation{
		ledgerCLIObservation("run-wide", mesagent.ToolSelectionWide),
		ledgerCLIObservation("run-filtered", mesagent.ToolSelectionFiltered),
	})

	args := []string{
		"-inventory", inventoryPath, "-asset", "tool-selection-v1",
		"-output", outputPath,
		"-model-profile", "stepfun-main", "-config-fingerprint", "sha256:config",
		"-implementation-revision", "git:revision-1",
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d stderr=%s", code, stderr.String())
	}
	if stdout.String() != "evaluation_ledger asset=tool-selection-v1 runs=2 provider_calls=0\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var report evaluationledger.Report
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if report.SchemaVersion != evaluationledger.SchemaVersion || report.Summary.Runs != 2 ||
		report.Summary.Usage.ModelCalls != 2 {
		t.Fatalf("report = %+v", report)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 1 {
		t.Fatalf("overwrite run() code = %d stderr=%s", code, stderr.String())
	}
	if after, err := os.ReadFile(outputPath); err != nil || !bytes.Equal(after, contents) {
		t.Fatalf("existing report changed: err=%v", err)
	}
}

func TestRunReplaysEvidenceGateEarlyExitWithoutProvider(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inventoryPath := filepath.Join(dir, "inventory.json")
	datasetPath := filepath.Join(dir, "dataset.jsonl")
	observationsPath := filepath.Join(dir, "observations.jsonl")
	outputPath := filepath.Join(dir, "ledger.json")
	writeJSONFile(t, inventoryPath, evaluationledger.Inventory{
		SchemaVersion: evaluationledger.InventorySchemaVersion, ArtifactRoot: ".",
		Assets: []evaluationledger.Asset{{
			ID: "evidence-gate-early-exit-v1", Domain: "evidence_gate_early_exit",
			ObservationKind: "evidence_gate_early_exit", Status: evaluationledger.AssetRecomputed,
			Reason: "Deterministic paired fixture.", EntryPoint: "mesguard-evaluation-ledger",
			DatasetArtifact: "dataset.jsonl", ObservationArtifact: "observations.jsonl",
		}},
	})
	writeJSONLines(t, datasetPath, []mesagent.EvidenceGateEvaluationCase{{
		DatasetVersion: "evidence-gate-v1", CaseID: "case-1", EvidenceSufficientAtRun: 1,
	}})
	makeObservation := func(variant mesagent.EvaluationVariant, runs int) mesagent.EvidenceGateEvaluationObservation {
		return mesagent.EvidenceGateEvaluationObservation{
			DatasetVersion: "evidence-gate-v1", CaseID: "case-1", Variant: variant,
			RunID: "case-1-" + string(variant), EarlyExitEnabled: variant == mesagent.EvaluationExperiment,
			PairingFingerprint: "sha256:pair", ModelProvider: "fixture", ModelID: "scripted-v1",
			ModelProfile: "fixture", PromptVersion: "diagnosis-v1", ReasoningEffort: "none",
			AgentRuns: runs, Completed: true, QualityReviewed: true,
			ConclusionCorrect: true, CitationCorrect: true,
			Usage:     mesagent.ModelUsage{ModelCalls: runs, PromptTokens: runs * 90, CompletionTokens: runs * 10, TotalTokens: runs * 100},
			ToolCalls: runs, DurationMillis: int64(runs * 100),
		}
	}
	writeJSONLines(t, observationsPath, []mesagent.EvidenceGateEvaluationObservation{
		makeObservation(mesagent.EvaluationBaseline, 2),
		makeObservation(mesagent.EvaluationExperiment, 1),
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-inventory", inventoryPath, "-asset", "evidence-gate-early-exit-v1", "-output", outputPath,
		"-model-profile", "fixture", "-config-fingerprint", "sha256:config",
		"-implementation-revision", "git:revision-1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code=%d stderr=%s", code, stderr.String())
	}
	var report evaluationledger.Report
	contents, err := os.ReadFile(outputPath)
	if err != nil || json.Unmarshal(contents, &report) != nil {
		t.Fatalf("read report: %v", err)
	}
	var summary mesagent.EvidenceGateEarlyExitSummary
	if err := report.DecodeDomainSummary(&summary); err != nil {
		t.Fatal(err)
	}
	if !summary.PerformanceClaimsAllowed || summary.PairedCases != 1 || report.Summary.Usage.ModelCalls != 3 {
		t.Fatalf("report = %+v domain=%+v", report, summary)
	}
}

func TestEvaluationInventoryCoversExistingEvaluationEntryPoints(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file")
	}
	commandDir := filepath.Dir(currentFile)
	repositoryRoot := filepath.Clean(filepath.Join(commandDir, "..", "..", ".."))
	inventoryFile, err := os.Open(filepath.Join(repositoryRoot, "config", "evaluation-assets-v1.json"))
	if err != nil {
		t.Fatalf("open inventory: %v", err)
	}
	defer inventoryFile.Close()
	inventory, err := evaluationledger.ParseInventory(inventoryFile)
	if err != nil {
		t.Fatalf("ParseInventory() error = %v", err)
	}
	covered := make(map[string]struct{}, len(inventory.Assets))
	for _, asset := range inventory.Assets {
		covered[asset.EntryPoint] = struct{}{}
	}
	entries, err := os.ReadDir(filepath.Join(repositoryRoot, "tools", "evaluation"))
	if err != nil {
		t.Fatalf("read evaluation commands: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "mesguard-evaluation-ledger" || entry.Name() == "mesguard-m4-acceptance" || !strings.HasPrefix(entry.Name(), "mesguard-") {
			continue
		}
		if _, exists := covered[entry.Name()]; !exists {
			t.Errorf("evaluation entry point %q is missing from inventory", entry.Name())
		}
	}
}

func TestRunRejectsHistoricalObservationMissingDurationField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inventoryPath := filepath.Join(dir, "inventory.json")
	datasetPath := filepath.Join(dir, "dataset.jsonl")
	observationsPath := filepath.Join(dir, "observations.jsonl")
	writeJSONFile(t, inventoryPath, evaluationledger.Inventory{
		SchemaVersion: evaluationledger.InventorySchemaVersion, ArtifactRoot: ".",
		Assets: []evaluationledger.Asset{{
			ID: "tool-selection-v1", Domain: "tool_selection", ObservationKind: "tool_selection",
			Status: evaluationledger.AssetRetestNeeded, Reason: "Selective retest required.",
			EntryPoint: "mesguard-tool-selection-eval", DatasetArtifact: "dataset.jsonl",
			ObservationArtifact: "observations.jsonl",
		}},
	})
	writeJSONLines(t, datasetPath, []mesagent.ToolSelectionCase{{
		DatasetVersion: "tool-selection-v1", CaseID: "case-1", Scope: mesagent.ToolSelectionTicket,
		UserQuery: "读取工单", ExpectedTool: mesagent.ToolReadExternalCase,
	}})
	observation := ledgerCLIObservation("run-wide", mesagent.ToolSelectionWide)
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode observation fields: %v", err)
	}
	delete(fields, "durationMillis")
	writeJSONLines(t, observationsPath, []map[string]json.RawMessage{fields})

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-inventory", inventoryPath, "-asset", "tool-selection-v1", "-output", filepath.Join(dir, "ledger.json"),
		"-model-profile", "stepfun-main", "-config-fingerprint", "sha256:config",
		"-implementation-revision", "git:revision-1",
	}, &stdout, &stderr)
	if code != 1 || !bytes.Contains(stderr.Bytes(), []byte(`required field "durationMillis" is missing`)) {
		t.Fatalf("run() code=%d stderr=%s", code, stderr.String())
	}
}

func TestReadJSONLinesRejectsNullRequiredField(t *testing.T) {
	t.Parallel()

	_, err := readJSONLines(
		bytes.NewBufferString("{\"durationMillis\":null}\n"),
		"observations",
		func() map[string]any { return map[string]any{} },
		"durationMillis",
	)
	if err == nil || !strings.Contains(err.Error(), `required field "durationMillis" is missing`) {
		t.Fatalf("readJSONLines() error = %v", err)
	}
}

func ledgerCLIObservation(runID string, variant mesagent.ToolSelectionVariant) mesagent.ToolSelectionObservation {
	return mesagent.ToolSelectionObservation{
		DatasetVersion: "tool-selection-v1", CaseID: "case-1", Variant: variant, RunID: runID,
		ModelProvider: "stepfun", ModelID: "step-3.7-flash", ReasoningEffort: "low",
		PromptVersion: "tool-selection-v1", MaxOutputTokens: 64,
		AvailableTools: []string{mesagent.ToolReadExternalCase}, SelectedTool: mesagent.ToolReadExternalCase,
		ToolCallCount: 1, ToolSchemaHash: "sha256:schema", ToolSchemaBytes: 100,
		BasePromptTokens: 80, ToolSchemaPromptTokens: 40,
		Usage:          mesagent.ModelUsage{ModelCalls: 1, PromptTokens: 120, CompletionTokens: 5, TotalTokens: 125},
		DurationMillis: 200,
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeJSONLines[T any](t *testing.T, path string, values []T) {
	t.Helper()
	var contents bytes.Buffer
	encoder := json.NewEncoder(&contents)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}
	if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
