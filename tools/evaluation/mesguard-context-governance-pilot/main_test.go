package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
)

func TestRunValidatesAndExportsPinnedPilotFixture(t *testing.T) {
	directory := t.TempDir()
	fixturePath := filepath.Join(directory, "fixture.json")
	stdout := &bytes.Buffer{}
	if err := run([]string{"-validate-only", "-fixture-output", fixturePath}, stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"datasetVersion": "context-governance-pilot-v1"`) ||
		!strings.Contains(stdout.String(), `"checkpoints": 12`) {
		t.Fatalf("validation output = %s", stdout.String())
	}
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture mesagent.ContextGovernancePilotDataset
	if err := json.Unmarshal(contents, &fixture); err != nil || fixture.Validate() != nil || len(fixture.Scenarios) != 4 {
		t.Fatalf("exported fixture invalid: decode=%v validate=%v", err, fixture.Validate())
	}
}

func TestRunEstimatesProviderPlanWithoutExecuting(t *testing.T) {
	stdout := &bytes.Buffer{}
	if err := run([]string{"-estimate-only"}, stdout); err != nil {
		t.Fatal(err)
	}
	var plan mesagent.ContextGovernancePilotPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ProviderCalls != 48 || plan.MainCalls != 36 || plan.SummaryCalls != 12 ||
		plan.JudgeCalls != 0 || plan.EstimatedCostCNY <= 0 || plan.Concurrency != 1 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRunAggregatesStrictPilotObservations(t *testing.T) {
	fixture := mesagent.ContextGovernancePilotFixture()
	contract := mesagent.ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("a", 64), ReasoningMode: "effort:low",
		ToolContractFingerprint: strings.Repeat("b", 64), OutputReserveTokens: 512,
		PromptVersion: "conversation-v6",
	}
	observations := make([]mesagent.ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			observations = append(observations,
				commandObservation(fixture, checkpoint, mesagent.PilotArmCurrent, contract, 800),
				commandObservation(fixture, checkpoint, mesagent.PilotArmBaseline, contract, 1000),
				commandObservation(fixture, checkpoint, mesagent.PilotArmExperiment, contract, 400),
			)
		}
	}
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "observations.jsonl")
	outputPath := filepath.Join(directory, "summary.json")
	file, err := os.OpenFile(inputPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-input", inputPath, "-output", outputPath}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var report mesagent.ContextGovernancePilotReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	if report.ObservedRuns != 36 || report.ComparablePairs != 12 || report.RawTokenReduction <= 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunRejectsUnknownObservationFieldAndConflictingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	if err := os.WriteFile(path, []byte(`{"unexpected":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-input", path}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if err := run([]string{"-validate-only", "-estimate-only"}, &bytes.Buffer{}); err == nil {
		t.Fatal("run() accepted conflicting modes")
	}
}

func commandObservation(
	fixture mesagent.ContextGovernancePilotDataset,
	checkpoint mesagent.ContextGovernancePilotCheckpoint,
	arm mesagent.ContextGovernancePilotArm,
	contract mesagent.ContextGovernancePilotContract,
	promptTokens int,
) mesagent.ContextGovernancePilotObservation {
	scenarioID := ""
	for _, scenario := range fixture.Scenarios {
		for _, candidate := range scenario.Checkpoints {
			if candidate.CheckpointID == checkpoint.CheckpointID {
				scenarioID = scenario.ScenarioID
			}
		}
	}
	answer := strings.Join(checkpoint.Gold.RequiredAnswerTerms, " ")
	usage := mesagent.ContextGovernancePilotUsage{
		ModelCalls: 1, PromptTokens: promptTokens, CompletionTokens: 100, TotalTokens: promptTokens + 100,
	}
	observation := mesagent.ContextGovernancePilotObservation{
		DatasetVersion: fixture.DatasetVersion, FixtureVersion: fixture.FixtureVersion,
		ScenarioID: scenarioID, CheckpointID: checkpoint.CheckpointID,
		RunID: string(arm) + "-" + checkpoint.CheckpointID + "-command-test", Arm: arm, Contract: contract,
		Answer: answer, MainUsage: usage, WithinHardWindow: true,
		FirstTokenLatencyMillis: 100, PromptEpochID: "epoch-command-test",
	}
	if arm == mesagent.PilotArmExperiment {
		observation.SummaryContract = &mesagent.ContextGovernancePilotSummaryContract{
			ModelProvider: "fixture", ModelID: "summary-v1",
			ModelProfile: "summary-profile", ModelProfileFingerprint: strings.Repeat("c", 64),
			PromptVersion: "conversation-memory-v1",
		}
		observation.SummaryUsage = mesagent.ContextGovernancePilotUsage{
			ModelCalls: 1, PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70,
		}
	}
	return observation
}
