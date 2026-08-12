package agent

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/google/uuid"
)

func TestContextGovernancePilotFixtureHasFourScenariosAndThreeCheckpoints(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	if err := fixture.Validate(); err != nil {
		t.Fatal(err)
	}
	if fixture.DatasetVersion != "context-governance-pilot-v1" || len(fixture.Scenarios) != 4 {
		t.Fatalf("fixture identity/size = %q/%d", fixture.DatasetVersion, len(fixture.Scenarios))
	}
	for _, scenario := range fixture.Scenarios {
		if len(scenario.Checkpoints) != 3 {
			t.Fatalf("scenario %s checkpoints = %d", scenario.ScenarioID, len(scenario.Checkpoints))
		}
		if len(scenario.History) < 3 {
			t.Fatalf("scenario %s has no meaningful pre-seeded history", scenario.ScenarioID)
		}
	}
}

func TestContextGovernancePilotRejectsWrongShapeAndDuplicateCheckpoint(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	fixture.Scenarios = fixture.Scenarios[:3]
	if err := fixture.Validate(); err == nil {
		t.Fatal("Validate() accepted a pilot fixture without four scenarios")
	}

	fixture = ContextGovernancePilotFixture()
	fixture.Scenarios[0].Checkpoints[1].CheckpointID = fixture.Scenarios[0].Checkpoints[0].CheckpointID
	if err := fixture.Validate(); err == nil {
		t.Fatal("Validate() accepted duplicate checkpoint IDs")
	}
}

func TestContextGovernancePilotPlanCountsSummaryAndJudgeCalls(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	plan, err := BuildContextGovernancePilotPlan(fixture, ContextGovernancePilotPlanOptions{
		SummaryCallsPerExperimentCheckpoint: 1,
		JudgeCallsPerCheckpoint:             1,
		Pricing: ContextGovernancePilotPricing{
			MainInputCNYPerMillion:     1,
			MainOutputCNYPerMillion:    4,
			SummaryInputCNYPerMillion:  0.5,
			SummaryOutputCNYPerMillion: 2,
			JudgeInputCNYPerMillion:    0.2,
			JudgeOutputCNYPerMillion:   1,
		},
		EstimatedMainPromptTokens:    1000,
		EstimatedMainOutputTokens:    100,
		EstimatedSummaryPromptTokens: 300,
		EstimatedSummaryOutputTokens: 80,
		EstimatedJudgePromptTokens:   500,
		EstimatedJudgeOutputTokens:   100,
		Budget:                       ContextGovernancePilotBudget{MaxProviderCalls: 200, MaxEstimatedCostCNY: 10, Concurrency: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MainCalls != 36 || plan.SummaryCalls != 12 || plan.JudgeCalls != 36 || plan.ProviderCalls != 84 {
		t.Fatalf("call plan = %+v", plan)
	}
	if plan.EstimatedCostCNY <= 0 || plan.MaxProviderCalls != 200 || plan.Concurrency != 1 {
		t.Fatalf("budget plan = %+v", plan)
	}
}

func TestContextGovernancePilotPlanBlocksBeforeProviderWhenOverBudget(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	_, err := BuildContextGovernancePilotPlan(fixture, ContextGovernancePilotPlanOptions{
		SummaryCallsPerExperimentCheckpoint: 1,
		Budget:                              ContextGovernancePilotBudget{MaxProviderCalls: 10, MaxEstimatedCostCNY: 10, Concurrency: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "provider call budget") {
		t.Fatalf("BuildContextGovernancePilotPlan() error = %v", err)
	}
}

func TestContextGovernancePilotFixtureCreatesThePinnedPromptPressureGradient(t *testing.T) {
	estimator, err := contextgovernance.NewLocalTokenEstimator(contextgovernance.EstimationMethodLocalCalibrated, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		t.Fatal(err)
	}
	preflight := ConversationContextPreflightConfig{
		Enabled: true, Planner: planner,
		ModelProfile: contextgovernance.ModelProfile{
			Name: "stepfun-main", Provider: "stepfun", ModelID: "step-3.7-flash",
			ContextWindowTokens: 131_072, MaxOutputTokens: 4096, SafetyMarginTokens: 6554,
		},
		SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 8192, PreflightTimeout: time.Second,
	}
	runner := &ConversationRunner{
		contextPreflight: preflight, systemInstruction: "Pilot pressure fixture",
		modelProvider: "stepfun", modelID: "step-3.7-flash", promptVersion: "conversation-v1",
	}
	wantHard := []bool{false, true, true}
	wantExceeds := []bool{false, false, true}
	for _, scenario := range ContextGovernancePilotFixture().Scenarios {
		messages := make([]conversation.Message, 0, len(scenario.History))
		conversationID := uuid.New()
		for _, item := range scenario.History {
			role := conversation.MessageRoleUser
			if item.Role == "assistant" {
				role = conversation.MessageRoleAssistant
			}
			messages = append(messages, conversation.Message{
				ID: uuid.New(), ConversationID: conversationID, Seq: item.Seq, Role: role, Content: item.Content,
			})
		}
		for index, checkpoint := range scenario.Checkpoints {
			current := conversation.Message{
				ID: uuid.New(), ConversationID: conversationID, Seq: checkpoint.HistoryThroughSeq + 1,
				Role: conversation.MessageRoleUser, Content: checkpoint.Question,
			}
			projection, err := buildFullConversationPromptProjection(messages[:checkpoint.HistoryThroughSeq], current)
			if err != nil {
				t.Fatalf("%s/%s projection: %v", scenario.ScenarioID, checkpoint.CheckpointID, err)
			}
			manifest, err := runner.buildConversationPromptManifest(context.Background(), nil, projection)
			if err != nil {
				t.Fatalf("%s/%s manifest: %v", scenario.ScenarioID, checkpoint.CheckpointID, err)
			}
			if manifest.HardThresholdReached != wantHard[index] || manifest.ExceedsHardWindow != wantExceeds[index] {
				t.Fatalf(
					"%s/%s pressure hard/exceeds/upper/available = %v/%v/%d/%d, want %v/%v",
					scenario.ScenarioID, checkpoint.CheckpointID, manifest.HardThresholdReached,
					manifest.ExceedsHardWindow, manifest.EstimatedUpperBoundTokens, manifest.AvailableInputTokens,
					wantHard[index], wantExceeds[index],
				)
			}
		}
	}
}

func TestEvaluateContextGovernancePilotIncludesSummaryUsageInRawReduction(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	fixtureFingerprint, _ := ContextGovernancePilotDatasetFingerprint(fixture)
	contract := ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("c", 64),
		ReasoningMode:           "effort:low", ToolContractFingerprint: strings.Repeat("a", 64),
		OutputReserveTokens: 512, PromptVersion: "conversation-v6",
	}
	summaryContract := pilotSummaryContractForTest()
	observations := make([]ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			for _, arm := range []ContextGovernancePilotArm{PilotArmCurrent, PilotArmBaseline, PilotArmExperiment} {
				observation := ContextGovernancePilotObservation{
					DatasetVersion: fixture.DatasetVersion, FixtureVersion: fixture.FixtureVersion, FixtureFingerprint: fixtureFingerprint,
					ScenarioID: scenario.ScenarioID, CheckpointID: checkpoint.CheckpointID,
					RunID: string(arm) + "-" + scenario.ScenarioID + "-" + checkpoint.CheckpointID,
					Arm:   arm, Contract: contract, Answer: checkpoint.Gold.RequiredAnswerTerms[0],
					MainUsage:        ContextGovernancePilotUsage{ModelCalls: 1, PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, CachedTokens: 200},
					WithinHardWindow: true, FirstTokenLatencyMillis: 100, PromptEpochID: "epoch-a",
				}
				if arm == PilotArmBaseline {
					observation.MainUsage.PromptTokens = 2000
					observation.MainUsage.TotalTokens = 2100
				}
				if arm == PilotArmExperiment {
					observation.SummaryContract = &summaryContract
					observation.MainUsage.PromptTokens = 500
					observation.MainUsage.TotalTokens = 600
					observation.SummaryUsage = ContextGovernancePilotUsage{ModelCalls: 1, PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 10}
				}
				observations = append(observations, observation)
			}
		}
	}
	report, err := EvaluateContextGovernancePilot(fixture, observations, ContextGovernancePilotPricing{
		MainInputCNYPerMillion: 1, MainOutputCNYPerMillion: 4,
		SummaryInputCNYPerMillion: 0.5, SummaryOutputCNYPerMillion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Twelve checkpoints are paired: Baseline = 12 * 2100;
	// Experiment = 12 * (600 + 150).
	wantReduction := 1 - float64(12*750)/float64(12*2100)
	if math.Abs(report.RawTokenReduction-wantReduction) > 1e-9 {
		t.Fatalf("raw token reduction = %v, want %v", report.RawTokenReduction, wantReduction)
	}
	if report.SummaryOverheadTokens != 12*150 || report.MainPromptReduction <= 0 || report.CacheHitRatio <= 0 {
		t.Fatalf("token report = %+v", report)
	}
}

func TestEvaluateContextGovernancePilotExcludesBaselineOverWindowFromTokenComparison(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	contract := ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("c", 64),
		ReasoningMode:           "effort:low", ToolContractFingerprint: strings.Repeat("b", 64),
		OutputReserveTokens: 512, PromptVersion: "conversation-v6",
	}
	observations := make([]ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			observations = append(observations,
				pilotObservationForTest(fixture, checkpoint, PilotArmBaseline, contract, 2000, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmExperiment, contract, 500, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmCurrent, contract, 700, true),
			)
		}
	}
	observations[0] = pilotObservationForTest(fixture, fixture.Scenarios[0].Checkpoints[0], PilotArmBaseline, contract, 2000, false)
	report, err := EvaluateContextGovernancePilot(fixture, observations, ContextGovernancePilotPricing{})
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineOverWindowCount != 1 || report.ComparablePairs != 11 || report.RawTokenReduction <= 0 || report.ProviderHardWindowViolationCount != 0 {
		t.Fatalf("over-window report = %+v", report)
	}
}

func TestEvaluateContextGovernancePilotExcludesFailedProviderPairAndFailsGate(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	contract := ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("c", 64),
		ReasoningMode:           "effort:low", ToolContractFingerprint: strings.Repeat("b", 64),
		OutputReserveTokens: 512, PromptVersion: "conversation-v6",
	}
	observations := make([]ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			observations = append(observations,
				pilotObservationForTest(fixture, checkpoint, PilotArmBaseline, contract, 2000, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmExperiment, contract, 500, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmCurrent, contract, 700, true),
			)
		}
	}
	observations[1].Answer = ""
	observations[1].MainUsage = ContextGovernancePilotUsage{ModelCalls: 1}
	observations[1].FirstTokenLatencyMillis = 0
	observations[1].ErrorType = "provider_or_runner_failed"

	report, err := EvaluateContextGovernancePilot(fixture, observations, ContextGovernancePilotPricing{})
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedRuns != 1 || report.ComparablePairs != 11 ||
		!slices.Contains(report.GateFailures, "run_failure") {
		t.Fatalf("failed-run report = %+v", report)
	}
}

func TestEvaluateContextGovernancePilotAccountsForFailedSummaryRetries(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	contract := ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("c", 64), ReasoningMode: "effort:low",
		ToolContractFingerprint: strings.Repeat("b", 64), OutputReserveTokens: 512,
		PromptVersion: "conversation-v6",
	}
	observations := make([]ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			observations = append(observations,
				pilotObservationForTest(fixture, checkpoint, PilotArmBaseline, contract, 2000, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmExperiment, contract, 500, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmCurrent, contract, 700, true),
			)
		}
	}
	failed := &observations[1]
	failed.Answer = ""
	failed.MainUsage = ContextGovernancePilotUsage{}
	failed.SummaryUsage = ContextGovernancePilotUsage{
		ModelCalls: 3, PromptTokens: 150_000, CompletionTokens: 3_000, TotalTokens: 153_000,
	}
	failed.FirstTokenLatencyMillis = 0
	failed.ErrorType = "summary_payload_schema_invalid"

	report, err := EvaluateContextGovernancePilot(fixture, observations, ContextGovernancePilotPricing{
		SummaryInputCNYPerMillion: 1, SummaryOutputCNYPerMillion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedAccounting := report.FailedAccountingByArm[PilotArmExperiment]
	incomparable := report.IncomparableAccountingByArm[PilotArmExperiment]
	observed := report.ObservedAccountingByArm[PilotArmExperiment]
	if failedAccounting.Observations != 1 || failedAccounting.SummaryUsage.ModelCalls != 3 ||
		failedAccounting.SummaryUsage.TotalTokens != 153_000 || failedAccounting.EstimatedCostCNY != 0.156 ||
		incomparable.SummaryUsage.TotalTokens != 153_000 || observed.SummaryUsage.TotalTokens != 153_000 {
		t.Fatalf("accounting failed/incomparable/observed = %+v/%+v/%+v", failedAccounting, incomparable, observed)
	}
}

func TestEvaluateContextGovernancePilotSeparatesBlockedOverWindowFromProviderViolation(t *testing.T) {
	fixture := ContextGovernancePilotFixture()
	contract := ContextGovernancePilotContract{
		ModelProvider: "fixture", ModelID: "main-v1", ModelProfile: "main-profile",
		ModelProfileFingerprint: strings.Repeat("c", 64), ReasoningMode: "effort:low",
		ToolContractFingerprint: strings.Repeat("b", 64), OutputReserveTokens: 512,
		PromptVersion: "conversation-v6",
	}
	observations := make([]ContextGovernancePilotObservation, 0, 36)
	for _, scenario := range fixture.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			observations = append(observations,
				pilotObservationForTest(fixture, checkpoint, PilotArmBaseline, contract, 2000, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmExperiment, contract, 500, true),
				pilotObservationForTest(fixture, checkpoint, PilotArmCurrent, contract, 700, true),
			)
		}
	}
	blocked := &observations[1]
	blocked.Answer = ""
	blocked.MainUsage = ContextGovernancePilotUsage{}
	blocked.WithinHardWindow = false
	blocked.FirstTokenLatencyMillis = 0
	blocked.ErrorType = "prompt_window_exceeded"

	report, err := EvaluateContextGovernancePilot(fixture, observations, ContextGovernancePilotPricing{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ExperimentOverWindowCount != 1 || report.ProviderHardWindowViolationCount != 0 ||
		slices.Contains(report.GateFailures, "hard_window_violation") {
		t.Fatalf("blocked over-window report = %+v", report)
	}
}

func pilotObservationForTest(
	fixture ContextGovernancePilotDataset, checkpoint ContextGovernancePilotCheckpoint,
	arm ContextGovernancePilotArm, contract ContextGovernancePilotContract,
	promptTokens int, withinWindow bool,
) ContextGovernancePilotObservation {
	fixtureFingerprint, _ := ContextGovernancePilotDatasetFingerprint(fixture)
	scenarioID := ""
	for _, scenario := range fixture.Scenarios {
		for _, candidate := range scenario.Checkpoints {
			if candidate.CheckpointID == checkpoint.CheckpointID {
				scenarioID = scenario.ScenarioID
			}
		}
	}
	mainUsage := ContextGovernancePilotUsage{}
	firstTokenLatencyMillis := int64(0)
	answer := ""
	if withinWindow {
		mainUsage = ContextGovernancePilotUsage{
			ModelCalls: 1, PromptTokens: promptTokens, CompletionTokens: 100, TotalTokens: promptTokens + 100,
		}
		firstTokenLatencyMillis = 100
		answer = strings.Join(checkpoint.Gold.RequiredAnswerTerms, " ")
	}
	var summaryContract *ContextGovernancePilotSummaryContract
	if arm == PilotArmExperiment {
		contract := pilotSummaryContractForTest()
		summaryContract = &contract
	}
	errorType := ""
	if !withinWindow {
		errorType = "prompt_window_exceeded"
	}
	runID := string(arm) + "-" + checkpoint.CheckpointID
	return ContextGovernancePilotObservation{
		DatasetVersion: fixture.DatasetVersion, FixtureVersion: fixture.FixtureVersion, FixtureFingerprint: fixtureFingerprint,
		ScenarioID: scenarioID, CheckpointID: checkpoint.CheckpointID,
		RunID: runID, Arm: arm, Contract: contract, SummaryContract: summaryContract,
		Answer: answer, MainUsage: mainUsage,
		WithinHardWindow: withinWindow, FirstTokenLatencyMillis: firstTokenLatencyMillis,
		PromptEpochID: "epoch-test", ErrorType: errorType,
	}
}

func pilotSummaryContractForTest() ContextGovernancePilotSummaryContract {
	return ContextGovernancePilotSummaryContract{
		ModelProvider: "fixture", ModelID: "summary-v1",
		ModelProfile: "summary-profile", ModelProfileFingerprint: strings.Repeat("d", 64),
		PromptVersion: "conversation-memory-v1",
	}
}
