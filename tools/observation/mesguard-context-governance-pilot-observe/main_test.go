package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/memorycompactor"
	"github.com/google/uuid"
)

func TestRunWithoutExecuteProviderOnlyPrintsBoundedPlan(t *testing.T) {
	t.Setenv("MESGUARD_CONFIG_FILE", filepath.Join(t.TempDir(), "missing.toml"))
	var output bytes.Buffer
	if err := run(nil, &output); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	var plan mesagent.ContextGovernancePilotPlan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Scenarios != 4 || plan.Checkpoints != 12 || plan.MainCalls != 36 ||
		plan.SummaryCalls != 12 || plan.ProviderCalls != 48 || plan.EstimatedCostCNY != 4.716 ||
		plan.MaxProviderCalls != 200 || plan.MaxCostCNY != 10 || plan.Concurrency != 1 {
		t.Fatalf("default provider-free plan = %+v", plan)
	}
}

func TestRunRejectsUnsafeSummaryTimeoutOverrideBeforeProvider(t *testing.T) {
	for _, value := range []string{"500ms", "6m"} {
		if err := run([]string{"-summary-timeout", value}, &bytes.Buffer{}); err == nil {
			t.Fatalf("run accepted unsafe Summary timeout %q", value)
		}
	}
}

func TestRunRejectsUnsafeSummaryAttemptOverrideBeforeProvider(t *testing.T) {
	if err := run([]string{"-summary-attempts", "6"}, &bytes.Buffer{}); err == nil {
		t.Fatal("run accepted unsafe Summary attempt override")
	}
}

func TestPilotReasoningModeSupportsProviderSpecificControls(t *testing.T) {
	tests := []struct {
		identity chatmodel.Identity
		want     string
	}{
		{identity: chatmodel.Identity{ReasoningEffort: " Medium "}, want: "effort:medium"},
		{identity: chatmodel.Identity{ThinkingMode: " Disabled "}, want: "thinking:disabled"},
		{identity: chatmodel.Identity{}, want: "reasoning:unspecified"},
	}
	for _, item := range tests {
		if got := pilotReasoningMode(item.identity); got != item.want {
			t.Fatalf("pilotReasoningMode(%+v) = %q, want %q", item.identity, got, item.want)
		}
	}
}

func TestPilotSelectionAcceptsOneDiagnosticCheckpoint(t *testing.T) {
	dataset := mesagent.ContextGovernancePilotFixture()
	selection, err := newPilotSelection(dataset, "incident-correction", "incident-cp2", "experiment")
	if err != nil {
		t.Fatal(err)
	}
	if !selection.partial() || !selection.includes("incident-correction", "incident-cp2", mesagent.PilotArmExperiment) ||
		selection.includes("incident-correction", "incident-cp1", mesagent.PilotArmExperiment) ||
		selection.includes("incident-correction", "incident-cp2", mesagent.PilotArmBaseline) {
		t.Fatalf("diagnostic selection = %+v", selection)
	}
}

func TestPilotSelectionRejectsCheckpointFromAnotherScenario(t *testing.T) {
	_, err := newPilotSelection(
		mesagent.ContextGovernancePilotFixture(), "release-policy", "incident-cp2", "experiment",
	)
	if err == nil {
		t.Fatal("newPilotSelection() accepted a checkpoint from another scenario")
	}
}

func TestValidateSelectedPilotBudgetRejectsDefaultFullRun(t *testing.T) {
	dataset := mesagent.ContextGovernancePilotFixture()
	limits := mesagent.PilotModelCallLimits{
		MaxProviderCalls: 2, MaxMainCalls: 1, MaxSummaryCalls: 1,
		MaxEstimatedMainPromptTokens: 130000, MaxEstimatedSummaryPromptTokens: 130000,
		MaxEstimatedCostCNY: 0.5,
	}
	if err := validateSelectedPilotBudget(dataset, pilotSelection{}, 1, limits); err == nil {
		t.Fatal("validateSelectedPilotBudget() accepted the full Pilot under diagnostic defaults")
	}
}

func TestValidateSelectedPilotBudgetAccountsForSummaryRetries(t *testing.T) {
	dataset := mesagent.ContextGovernancePilotFixture()
	selection, err := newPilotSelection(dataset, "incident-correction", "incident-cp2", "experiment")
	if err != nil {
		t.Fatal(err)
	}
	limits := mesagent.PilotModelCallLimits{
		MaxProviderCalls: 2, MaxMainCalls: 1, MaxSummaryCalls: 1,
		MaxEstimatedMainPromptTokens: 130000, MaxEstimatedSummaryPromptTokens: 130000,
		MaxEstimatedCostCNY: 0.5,
	}
	if err := validateSelectedPilotBudget(dataset, selection, 1, limits); err != nil {
		t.Fatalf("one-attempt diagnostic selection error = %v", err)
	}
	if err := validateSelectedPilotBudget(dataset, selection, 3, limits); err == nil {
		t.Fatal("validateSelectedPilotBudget() ignored the configured Summary retry multiplier")
	}
}

func TestPilotErrorTypePreservesCompactionFailureStage(t *testing.T) {
	err := fmt.Errorf("outer: %w", fmt.Errorf("%w: %w",
		conversationmemory.ErrCompactionFailed, conversationmemory.ErrSourceOutOfRange))
	if got := pilotErrorType(err); got != "summary_source_out_of_range" {
		t.Fatalf("pilotErrorType() = %q, want summary_source_out_of_range", got)
	}
}

func TestPilotErrorTypeDistinguishesSummaryAndAgentTimeout(t *testing.T) {
	summaryErr := fmt.Errorf("outer: %w", fmt.Errorf("%w: %w",
		memorycompactor.ErrProviderRequest, context.DeadlineExceeded))
	if got := pilotErrorType(summaryErr); got != "summary_timeout" {
		t.Fatalf("pilotErrorType(summary timeout) = %q, want summary_timeout", got)
	}
	if got := pilotErrorType(context.DeadlineExceeded); got != "agent_timeout" {
		t.Fatalf("pilotErrorType(agent timeout) = %q, want agent_timeout", got)
	}
}

func TestPilotErrorTypeRecognizesWrappedNetworkTimeout(t *testing.T) {
	networkTimeout := &net.DNSError{IsTimeout: true, Err: "timeout", Name: "provider.invalid"}
	err := fmt.Errorf("outer: %w", fmt.Errorf("%w: %w", memorycompactor.ErrProviderRequest, networkTimeout))
	if got := pilotErrorType(err); got != "summary_timeout" {
		t.Fatalf("pilotErrorType(network timeout) = %q, want summary_timeout", got)
	}
}

func TestPilotErrorTypeRecognizesTruncatedSummary(t *testing.T) {
	if got := pilotErrorType(memorycompactor.ErrOutputTruncated); got != "summary_output_truncated" {
		t.Fatalf("pilotErrorType(truncated) = %q, want summary_output_truncated", got)
	}
}

func TestValidatePilotPressureRejectsAProfileThatDoesNotExerciseCompaction(t *testing.T) {
	estimator, err := contextgovernance.NewLocalTokenEstimator(contextgovernance.EstimationMethodLocalCalibrated, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		t.Fatal(err)
	}
	runtime := bootstrap.ConversationTokenBudgetRuntime{
		Estimator: estimator, Planner: planner,
		Profile: contextgovernance.ModelProfile{
			Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
			ContextWindowTokens: 131_072, MaxOutputTokens: 4096, SafetyMarginTokens: 6554,
		},
	}
	memory := config.ContextMemoryConfig{
		SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85, ToolGrowthReserveTokens: 8192,
	}
	if err := validatePilotPressure(
		context.Background(), mesagent.ContextGovernancePilotFixture(), runtime,
		"Pilot instruction", "[]", memory,
	); err != nil {
		t.Fatalf("131K Pilot pressure error = %v", err)
	}
	runtime.Profile.ContextWindowTokens = 262_144
	if err := validatePilotPressure(
		context.Background(), mesagent.ContextGovernancePilotFixture(), runtime,
		"Pilot instruction", "[]", memory,
	); err == nil {
		t.Fatal("validatePilotPressure() accepted a 262K profile that does not exercise the pinned thresholds")
	}
}

func TestPilotMemoryRepositoryClonesSavedPayload(t *testing.T) {
	repository := newPilotMemoryRepository()
	conversationID := uuid.New()
	candidate := pilotMemoryCandidate(t, conversationID, 1, 3, nil, "original")
	if _, err := repository.Save(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Payload.Facts[0].Content = "mutated by caller"

	latest, err := repository.Latest(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Payload.Facts[0].Content != "original" {
		t.Fatalf("stored payload content = %q, want original", latest.Payload.Facts[0].Content)
	}
	latest.Payload.Facts[0].Content = "mutated after read"
	again, err := repository.Get(context.Background(), latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Payload.Facts[0].Content != "original" {
		t.Fatalf("reloaded payload content = %q, want original", again.Payload.Facts[0].Content)
	}
}

func TestPilotMemoryRepositoryActivatesIncrementalCandidateWithCAS(t *testing.T) {
	repository := newPilotMemoryRepository()
	conversationID := uuid.New()
	createdAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	firstCandidate := pilotMemoryCandidateAt(t, conversationID, 1, 3, nil, "first", createdAt)
	first, err := repository.Save(context.Background(), firstCandidate)
	if err != nil {
		t.Fatal(err)
	}
	first, err = repository.Activate(context.Background(), conversationmemory.ActivationRequest{
		ConversationID: conversationID, CandidateSnapshotID: first.ID, ActivatedAt: createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate := pilotMemoryCandidateAt(t, conversationID, 1, 6, &first.ID, "second", createdAt.Add(time.Minute))
	second, err := repository.Save(context.Background(), secondCandidate)
	if err != nil {
		t.Fatal(err)
	}
	wrongExpected := uuid.New()
	if _, err := repository.Activate(context.Background(), conversationmemory.ActivationRequest{
		ConversationID: conversationID, CandidateSnapshotID: second.ID,
		ExpectedActiveSnapshotID: &wrongExpected, ActivatedAt: createdAt.Add(2 * time.Minute),
	}); !errors.Is(err, conversationmemory.ErrSnapshotActivationConflict) {
		t.Fatalf("Activate() CAS error = %v", err)
	}
	second, err = repository.Activate(context.Background(), conversationmemory.ActivationRequest{
		ConversationID: conversationID, CandidateSnapshotID: second.ID,
		ExpectedActiveSnapshotID: &first.ID, ActivatedAt: createdAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.Active(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := repository.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != second.ID || active.Status != conversationmemory.SnapshotStatusActive ||
		old.Status != conversationmemory.SnapshotStatusSuperseded || repository.SnapshotCount(conversationID) != 2 {
		t.Fatalf("active/old/count = %+v/%+v/%d", active, old, repository.SnapshotCount(conversationID))
	}
}

func TestPilotScenarioTimelineKeepsCheckpointHistoryMonotonicAndArmIndependent(t *testing.T) {
	scenario := mesagent.ContextGovernancePilotFixture().Scenarios[0]
	conversationID := uuid.New()
	timeline := newPilotScenarioTimeline(scenario, "experiment", conversationID)
	lastSeq := int64(0)
	lastLength := 0
	for _, checkpoint := range scenario.Checkpoints {
		history, current, err := timeline.Request(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) <= lastLength || current.Seq <= lastSeq || current.Seq != int64(len(history)+1) {
			t.Fatalf("%s history/current/last = %d/%d/%d", checkpoint.CheckpointID, len(history), current.Seq, lastSeq)
		}
		for index, message := range history {
			if message.Seq != int64(index+1) {
				t.Fatalf("%s history[%d].Seq = %d, want %d", checkpoint.CheckpointID, index, message.Seq, index+1)
			}
		}
		lastLength, lastSeq = len(history), current.Seq
		timeline.Complete(checkpoint, current)
	}

	history, _, err := timeline.Request(scenario.Checkpoints[len(scenario.Checkpoints)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history {
		if message.Role == "assistant" && message.Content != "" &&
			message.Content == "provider-specific answer" {
			t.Fatal("provider answer leaked into the controlled Pilot timeline")
		}
	}
}

func pilotMemoryCandidate(
	t *testing.T, conversationID uuid.UUID, fromSeq, throughSeq int64,
	supersedes *uuid.UUID, content string,
) conversationmemory.CandidateSnapshot {
	t.Helper()
	return pilotMemoryCandidateAt(t, conversationID, fromSeq, throughSeq, supersedes, content, time.Now().UTC())
}

func pilotMemoryCandidateAt(
	t *testing.T, conversationID uuid.UUID, fromSeq, throughSeq int64,
	supersedes *uuid.UUID, content string, createdAt time.Time,
) conversationmemory.CandidateSnapshot {
	t.Helper()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, SupersedesSnapshotID: supersedes,
		FromSeq: fromSeq, ThroughSeq: throughSeq, SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "fixture",
			ModelID: "summary-v1", PromptVersion: "conversation-memory-v1",
		},
		Payload: conversationmemory.Payload{
			Facts:     []conversationmemory.Entry{{EntryID: "fact_fixture", Content: content, SourceMessageSeqs: []int64{fromSeq}, Status: conversationmemory.EntryStatusActive}},
			Decisions: []conversationmemory.Entry{}, Corrections: []conversationmemory.Entry{},
			EvidenceReferences: []conversationmemory.ReferenceEntry{}, OpenQuestions: []conversationmemory.Entry{},
			Todos: []conversationmemory.Entry{}, TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
		},
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
