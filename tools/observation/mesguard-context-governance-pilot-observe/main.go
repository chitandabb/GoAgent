// Command mesguard-context-governance-pilot-observe runs the explicitly
// enabled Provider-backed M3 Pilot. Without -execute-provider it only prints
// the bounded plan and never reads credentials or calls a remote model.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/memorycompactor"
	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const pilotRunTimeout = 30 * time.Minute

func main() {
	if err := runWithProgress(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	return runWithProgress(args, stdout, io.Discard)
}

func runWithProgress(args []string, stdout, progress io.Writer) error {
	flags := flag.NewFlagSet("mesguard-context-governance-pilot-observe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "optional MESGuard TOML configuration path")
	fixturePath := flags.String("fixture", "", "optional strict Pilot fixture JSON")
	outputPath := flags.String("output", "output/evaluation/context-governance-pilot-v1.observations.jsonl", "raw observation JSONL output")
	summaryOutputPath := flags.String("summary-output", "output/evaluation/context-governance-pilot-v1.summary.json", "aggregated Pilot summary JSON output")
	executeProvider := flags.Bool("execute-provider", false, "required to create models and call Providers")
	scenarioID := flags.String("scenario-id", "", "optional diagnostic scenario filter")
	checkpointID := flags.String("checkpoint-id", "", "optional diagnostic checkpoint filter")
	arm := flags.String("arm", "", "optional diagnostic arm filter: current, baseline, or experiment")
	pairedCheckpoint := flags.Bool("paired-checkpoint", false, "execute Baseline and Experiment for one selected checkpoint")
	resume := flags.Bool("resume", false, "reuse validated observations from the output JSONL and execute only missing runs")
	maxMainCalls := flags.Int("max-main-calls", 1, "hard main-model call limit enforced before Provider access")
	maxSummaryCalls := flags.Int("max-summary-calls", 1, "hard Summary call limit enforced before Provider access")
	maxMainPromptTokens := flags.Int("max-estimated-main-prompt-tokens", 130000, "cumulative estimated main prompt Token limit")
	maxSummaryPromptTokens := flags.Int("max-estimated-summary-prompt-tokens", 130000, "cumulative estimated Summary prompt Token limit")
	maxEstimatedCostCNY := flags.Float64("max-estimated-cost-cny", 0.50, "conservative estimated cost admission limit")
	summaryTimeout := flags.Duration("summary-timeout", 0, "optional observer-only Summary timeout override (1s..5m)")
	summaryAttempts := flags.Int("summary-attempts", 0, "optional observer-only Summary attempt override (1..5)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-context-governance-pilot-observe [-execute-provider] [-resume] [-config path] [-fixture path] [-output path] [-summary-output path] [-scenario-id id] [-checkpoint-id id] [-arm arm | -paired-checkpoint] [-summary-timeout duration] [-summary-attempts n] [-max-main-calls n] [-max-summary-calls n] [-max-estimated-main-prompt-tokens n] [-max-estimated-summary-prompt-tokens n] [-max-estimated-cost-cny amount]")
	}
	if *summaryTimeout < 0 || *summaryTimeout > 5*time.Minute || (*summaryTimeout > 0 && *summaryTimeout < time.Second) {
		return errors.New("observer Summary timeout override must be between 1s and 5m")
	}
	if *summaryAttempts < 0 || *summaryAttempts > 5 {
		return errors.New("observer Summary attempt override must be between 1 and 5")
	}
	dataset := mesagent.ContextGovernancePilotFixture()
	if strings.TrimSpace(*fixturePath) != "" {
		if err := readStrictJSON(*fixturePath, &dataset); err != nil {
			return fmt.Errorf("read Pilot fixture: %w", err)
		}
	}
	if err := dataset.Validate(); err != nil {
		return fmt.Errorf("validate Pilot fixture: %w", err)
	}
	selection, err := newPilotSelection(dataset, *scenarioID, *checkpointID, *arm, *pairedCheckpoint)
	if err != nil {
		return err
	}
	var existing []mesagent.ContextGovernancePilotObservation
	if *resume {
		existing, err = readPilotObservations(*outputPath, dataset, true)
		if err != nil {
			return fmt.Errorf("resume Pilot observations: %w", err)
		}
		selection = selection.withCompleted(existing)
	}
	planOptions := mesagent.DefaultContextGovernancePilotPlanOptions()
	if !*executeProvider {
		plan, err := mesagent.BuildContextGovernancePilotPlan(dataset, planOptions)
		if err != nil {
			return err
		}
		return writeJSONTo(stdout, plan)
	}
	if strings.TrimSpace(*configPath) != "" {
		if err := os.Setenv("MESGUARD_CONFIG_FILE", strings.TrimSpace(*configPath)); err != nil {
			return fmt.Errorf("set config path: %w", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load Pilot config: %w", err)
	}
	if *summaryTimeout > 0 {
		profileName := cfg.Models.Chat.ConversationMemoryProfileName
		profile := cfg.Models.Chat.Profiles[profileName]
		profile.TimeoutMillis = int(summaryTimeout.Milliseconds())
		cfg.Models.Chat.Profiles[profileName] = profile
	}
	if *summaryAttempts > 0 {
		cfg.Agent.ContextMemory.Summary.MaxAttempts = *summaryAttempts
	}
	planOptions.SummaryCallsPerExperimentCheckpoint = cfg.Agent.ContextMemory.Summary.MaxAttempts
	plan, err := mesagent.BuildContextGovernancePilotPlan(dataset, planOptions)
	if err != nil {
		return err
	}
	executionLimits := mesagent.PilotModelCallLimits{
		MaxProviderCalls: *maxMainCalls + *maxSummaryCalls,
		MaxMainCalls:     *maxMainCalls, MaxSummaryCalls: *maxSummaryCalls,
		MaxEstimatedMainPromptTokens:    *maxMainPromptTokens,
		MaxEstimatedSummaryPromptTokens: *maxSummaryPromptTokens,
		MaxEstimatedCostCNY:             *maxEstimatedCostCNY,
	}
	if err := validateSelectedPilotBudget(dataset, selection, cfg.Agent.ContextMemory.Summary.MaxAttempts, executionLimits); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pilotRunTimeout)
	defer cancel()
	if len(existing) < expectedPilotObservationCount(dataset) {
		if err := removeIfExists(*summaryOutputPath); err != nil {
			return fmt.Errorf("invalidate incomplete Pilot summary: %w", err)
		}
	}
	persisted := append([]mesagent.ContextGovernancePilotObservation(nil), existing...)
	persistObservation := func(observation mesagent.ContextGovernancePilotObservation) error {
		merged, mergeErr := mergePilotObservations(dataset, persisted, []mesagent.ContextGovernancePilotObservation{observation})
		if mergeErr != nil {
			return mergeErr
		}
		if writeErr := writeJSONL(*outputPath, merged); writeErr != nil {
			return writeErr
		}
		persisted = merged
		return nil
	}
	added, err := executePilot(ctx, cfg, dataset, plan, selection, existing, executionLimits, progress, persistObservation)
	if err != nil {
		return err
	}
	observations, err := mergePilotObservations(dataset, existing, added)
	if err != nil {
		return fmt.Errorf("merge Pilot observations: %w", err)
	}
	if err := writeJSONL(*outputPath, observations); err != nil {
		return err
	}
	complete := len(observations) == expectedPilotObservationCount(dataset)
	if complete {
		report, err := mesagent.EvaluateContextGovernancePilot(dataset, observations, planOptions.Pricing)
		if err != nil {
			return fmt.Errorf("aggregate Pilot observations: %w", err)
		}
		if err := writeJSONFile(*summaryOutputPath, report); err != nil {
			return err
		}
	} else {
		*summaryOutputPath = ""
	}
	return writeJSONTo(stdout, struct {
		DatasetVersion string `json:"datasetVersion"`
		FixtureVersion string `json:"fixtureVersion"`
		Observations   int    `json:"observations"`
		Added          int    `json:"added"`
		Remaining      int    `json:"remaining"`
		Output         string `json:"output"`
		SummaryOutput  string `json:"summaryOutput"`
	}{dataset.DatasetVersion, dataset.FixtureVersion, len(observations), len(added), expectedPilotObservationCount(dataset) - len(observations), *outputPath, *summaryOutputPath})
}

type pilotSelection struct {
	scenarioID   string
	checkpointID string
	arm          mesagent.ContextGovernancePilotArm
	paired       bool
	completed    map[pilotObservationKey]struct{}
}

type pilotObservationKey struct {
	checkpointID string
	arm          mesagent.ContextGovernancePilotArm
}

func newPilotSelection(
	dataset mesagent.ContextGovernancePilotDataset,
	scenarioID, checkpointID, arm string, paired bool,
) (pilotSelection, error) {
	selection := pilotSelection{
		scenarioID: strings.TrimSpace(scenarioID), checkpointID: strings.TrimSpace(checkpointID),
		arm: mesagent.ContextGovernancePilotArm(strings.ToLower(strings.TrimSpace(arm))), paired: paired,
	}
	if selection.paired && (selection.scenarioID == "" || selection.checkpointID == "") {
		return pilotSelection{}, errors.New("paired Pilot execution requires one scenario and checkpoint")
	}
	if selection.paired && selection.arm != "" {
		return pilotSelection{}, errors.New("paired Pilot execution cannot be combined with an explicit arm")
	}
	if selection.arm != "" && !selection.arm.Valid() {
		return pilotSelection{}, errors.New("Pilot diagnostic arm must be current, baseline, or experiment")
	}
	matchedScenario, matchedCheckpoint := selection.scenarioID == "", selection.checkpointID == ""
	checkpointScenario := ""
	for _, scenario := range dataset.Scenarios {
		if scenario.ScenarioID == selection.scenarioID {
			matchedScenario = true
		}
		for _, checkpoint := range scenario.Checkpoints {
			if checkpoint.CheckpointID == selection.checkpointID {
				matchedCheckpoint = true
				checkpointScenario = scenario.ScenarioID
			}
		}
	}
	if !matchedScenario || !matchedCheckpoint || (selection.scenarioID != "" && checkpointScenario != "" &&
		checkpointScenario != selection.scenarioID) {
		return pilotSelection{}, errors.New("Pilot diagnostic selection does not match the fixed fixture")
	}
	return selection, nil
}

func (s pilotSelection) partial() bool {
	return s.scenarioID != "" || s.checkpointID != "" || s.arm != "" || s.paired
}

func (s pilotSelection) includes(
	scenarioID, checkpointID string,
	arm mesagent.ContextGovernancePilotArm,
) bool {
	_, completed := s.completed[pilotObservationKey{checkpointID: checkpointID, arm: arm}]
	return !completed && (s.scenarioID == "" || s.scenarioID == scenarioID) &&
		(s.checkpointID == "" || s.checkpointID == checkpointID) &&
		(s.arm == "" || s.arm == arm) &&
		(!s.paired || arm == mesagent.PilotArmBaseline || arm == mesagent.PilotArmExperiment)
}

func (s pilotSelection) withCompleted(observations []mesagent.ContextGovernancePilotObservation) pilotSelection {
	s.completed = make(map[pilotObservationKey]struct{}, len(observations))
	for _, observation := range observations {
		s.completed[pilotObservationKey{checkpointID: observation.CheckpointID, arm: observation.Arm}] = struct{}{}
	}
	return s
}

func (s pilotSelection) count(dataset mesagent.ContextGovernancePilotDataset) int {
	count := 0
	for _, scenario := range dataset.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			for _, arm := range pilotArmOrder() {
				if s.includes(scenario.ScenarioID, checkpoint.CheckpointID, arm) {
					count++
				}
			}
		}
	}
	return count
}

func validateSelectedPilotBudget(
	dataset mesagent.ContextGovernancePilotDataset,
	selection pilotSelection,
	summaryAttempts int,
	limits mesagent.PilotModelCallLimits,
) error {
	mainCalls, summaryCalls := 0, 0
	for _, scenario := range dataset.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			for _, candidateArm := range []mesagent.ContextGovernancePilotArm{
				mesagent.PilotArmCurrent, mesagent.PilotArmBaseline, mesagent.PilotArmExperiment,
			} {
				if !selection.includes(scenario.ScenarioID, checkpoint.CheckpointID, candidateArm) {
					continue
				}
				mainCalls++
				if candidateArm == mesagent.PilotArmExperiment {
					summaryCalls += summaryAttempts
				}
			}
		}
	}
	if mainCalls > limits.MaxMainCalls || summaryCalls > limits.MaxSummaryCalls ||
		mainCalls+summaryCalls > limits.MaxProviderCalls {
		return fmt.Errorf(
			"selected Pilot exceeds explicit execution budget before model creation: main calls %d/%d, Summary calls %d/%d; narrow the selection or explicitly raise all relevant limits",
			mainCalls, limits.MaxMainCalls, summaryCalls, limits.MaxSummaryCalls,
		)
	}
	return nil
}

type pilotArmRuntime struct {
	runner          *mesagent.ConversationRunner
	summaryModel    *mesagent.PilotMeasuredModel
	summaryContract *mesagent.ContextGovernancePilotSummaryContract
}

func executePilot(
	ctx context.Context,
	cfg config.Config,
	dataset mesagent.ContextGovernancePilotDataset,
	plan mesagent.ContextGovernancePilotPlan,
	selection pilotSelection,
	existing []mesagent.ContextGovernancePilotObservation,
	executionLimits mesagent.PilotModelCallLimits,
	progress io.Writer,
	persist func(mesagent.ContextGovernancePilotObservation) error,
) ([]mesagent.ContextGovernancePilotObservation, error) {
	mainProfile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return nil, err
	}
	mainProfileFingerprint, err := mainProfile.PromptProfileFingerprint(cfg.Models.Chat.ActiveProfileName)
	if err != nil {
		return nil, err
	}
	tokenBudget, err := bootstrap.BuildConversationTokenBudgetRuntime(cfg)
	if err != nil {
		return nil, err
	}
	catalog, err := mesagent.NewConversationDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: pilotExternalCaseGetter{},
	})
	if err != nil {
		return nil, fmt.Errorf("build Pilot Tool catalog: %w", err)
	}
	// Pilot 使用固定 conversation-default Profile 解析 Schema，不按任何
	// per-run 状态（权限/引用）变化；执行授权由 RunAccess 单独负责。
	resolved, err := catalog.ResolveProfile(ctx, agentruntime.ToolProfileConversation)
	if err != nil {
		return nil, fmt.Errorf("resolve Pilot conversation Profile: %w", err)
	}
	toolContract, err := mesagent.CanonicalToolContract(ctx, resolved.Tools)
	if err != nil {
		return nil, err
	}
	prompt, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return nil, err
	}
	if err := validatePilotPressure(
		ctx, dataset, tokenBudget, prompt.ConversationInstruction,
		toolContract.ModelVisibleJSON, cfg.Agent.ContextMemory,
	); err != nil {
		return nil, err
	}
	summaryProfile, err := cfg.Models.Chat.ConversationMemoryProfile()
	if err != nil {
		return nil, err
	}
	budget, err := mesagent.NewPilotModelCallBudgetWithLimits(
		executionLimits, planPricing(), mainProfile.MaxOutputTokens, summaryProfile.MaxOutputTokens,
	)
	if err != nil {
		return nil, err
	}
	mainInstance, err := chatmodel.NewActive(ctx, cfg.Models.Chat)
	if err != nil {
		return nil, fmt.Errorf("build Pilot main model: %w", err)
	}
	contract := mesagent.ContextGovernancePilotContract{
		ModelProvider: strings.ToLower(mainInstance.Identity.Provider), ModelID: mainInstance.Identity.ModelID,
		ModelProfile: cfg.Models.Chat.ActiveProfileName, ModelProfileFingerprint: mainProfileFingerprint,
		ReasoningMode: pilotReasoningMode(mainInstance.Identity), ToolContractFingerprint: toolContract.Fingerprint,
		OutputReserveTokens: mainProfile.MaxOutputTokens, PromptVersion: cfg.Agent.ConversationPromptVersion,
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	if err := validateResumedPilotContracts(existing, contract, nil); err != nil {
		return nil, err
	}
	mainModel, err := mesagent.NewPilotMeasuredModel(mainInstance.Model, mesagent.PilotMainModelCall, budget)
	if err != nil {
		return nil, err
	}
	armRuntimes := make(map[string]map[mesagent.ContextGovernancePilotArm]pilotArmRuntime)
	for _, scenario := range dataset.Scenarios {
		runtimes, err := buildScenarioRuntimes(ctx, cfg, tokenBudget, contract, prompt.ConversationInstruction, mainModel, catalog, budget)
		if err != nil {
			return nil, err
		}
		armRuntimes[scenario.ScenarioID] = runtimes
	}
	var summaryContract *mesagent.ContextGovernancePilotSummaryContract
	for _, runtimes := range armRuntimes {
		if runtime, ok := runtimes[mesagent.PilotArmExperiment]; ok && runtime.summaryContract != nil {
			summaryContract = runtime.summaryContract
			break
		}
	}
	if err := validateResumedPilotContracts(existing, contract, summaryContract); err != nil {
		return nil, err
	}
	return runPilotArms(ctx, dataset, contract, armRuntimes, mainModel, selection, progress, persist)
}

func buildScenarioRuntimes(
	ctx context.Context,
	cfg config.Config,
	tokenBudget bootstrap.ConversationTokenBudgetRuntime,
	contract mesagent.ContextGovernancePilotContract,
	instruction string,
	mainModel *mesagent.PilotMeasuredModel,
	catalog *mesagent.ToolCatalog,
	budget *mesagent.PilotModelCallBudget,
) (map[mesagent.ContextGovernancePilotArm]pilotArmRuntime, error) {
	runtimes := make(map[mesagent.ContextGovernancePilotArm]pilotArmRuntime, 3)
	for _, arm := range []mesagent.ContextGovernancePilotArm{mesagent.PilotArmCurrent, mesagent.PilotArmBaseline, mesagent.PilotArmExperiment} {
		preflight, summaryModel, summaryContract, err := buildPilotPreflight(ctx, cfg, tokenBudget, arm, budget)
		if err != nil {
			return nil, err
		}
		maxTotalTokens := tokenBudget.Profile.ContextWindowTokens
		if maxTotalTokens > 200_000 {
			maxTotalTokens = 200_000
		}
		runner, err := mesagent.NewConversationRunner(mesagent.ConversationRunnerConfig{
			ChatModel: mainModel, ToolCatalog: catalog, SystemInstruction: instruction,
			ModelProvider: contract.ModelProvider, ModelID: contract.ModelID, PromptVersion: contract.PromptVersion,
			Logger: zap.NewNop(), MaxIterations: 1, MaxToolCalls: 1, MaxTotalTokens: maxTotalTokens,
			MaxContextRunes: cfg.Agent.ConversationMaxContextRunes, Timeout: time.Duration(cfg.Agent.ConversationTimeoutMillis) * time.Millisecond,
			EnableStreaming: true, ContextPreflight: preflight,
		})
		if err != nil {
			return nil, fmt.Errorf("build %s Pilot runner: %w", arm, err)
		}
		runtimes[arm] = pilotArmRuntime{runner: runner, summaryModel: summaryModel, summaryContract: summaryContract}
	}
	return runtimes, nil
}

func buildPilotPreflight(
	ctx context.Context,
	cfg config.Config,
	tokenBudget bootstrap.ConversationTokenBudgetRuntime,
	arm mesagent.ContextGovernancePilotArm,
	budget *mesagent.PilotModelCallBudget,
) (mesagent.ConversationContextPreflightConfig, *mesagent.PilotMeasuredModel, *mesagent.ContextGovernancePilotSummaryContract, error) {
	preflight := mesagent.ConversationContextPreflightConfig{
		Enabled: true, Planner: tokenBudget.Planner, ModelProfile: tokenBudget.Profile,
		SoftThresholdRatio:      cfg.Agent.ContextMemory.SoftThresholdRatio,
		HardThresholdRatio:      cfg.Agent.ContextMemory.HardThresholdRatio,
		ToolGrowthReserveTokens: cfg.Agent.ContextMemory.ToolGrowthReserveTokens,
		PreflightTimeout:        time.Duration(cfg.Agent.ContextMemory.PreflightTimeoutMillis) * time.Millisecond,
		SyncCompactionTimeout:   time.Duration(cfg.Agent.ContextMemory.SyncCompactionTimeoutMillis) * time.Millisecond,
	}
	if arm == mesagent.PilotArmBaseline {
		preflight.HardWindowEnforced, preflight.FullHistoryEnabled = true, true
		return preflight, nil, nil, nil
	}
	if arm == mesagent.PilotArmCurrent {
		return preflight, nil, nil, nil
	}
	selector, err := contextgovernance.NewContinuousTailSelector(tokenBudget.Estimator)
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	summaryProfile, err := cfg.Models.Chat.ConversationMemoryProfile()
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	summaryFingerprint, err := summaryProfile.PromptProfileFingerprint(cfg.Models.Chat.ConversationMemoryProfileName)
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	summaryInstance, err := chatmodel.NewProfileWithResponseSchema(
		ctx, cfg.Models.Chat, cfg.Models.Chat.ConversationMemoryProfileName, chatmodel.ResponseSchema{
			Name:        conversationmemory.ResponseSchemaName,
			Description: "MESGuard structured conversation memory snapshot",
			Schema:      conversationmemory.PayloadJSONSchema(), Strict: true,
		},
	)
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, fmt.Errorf("build Pilot Summary model: %w", err)
	}
	summaryModel, err := mesagent.NewPilotMeasuredModel(summaryInstance.Model, mesagent.PilotSummaryModelCall, budget)
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	memoryRepository := newPilotMemoryRepository()
	memoryService, _, err := bootstrap.BuildConversationMemoryServiceWithModel(ctx, cfg, summaryModel, memoryRepository)
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	activationGate, err := mesagent.NewConversationMemoryActivationGate(mesagent.ConversationContextPreflightConfig{
		Enabled: true, SummaryTailEnabled: true, ContinuousTailEnabled: true, Planner: tokenBudget.Planner,
		TailSelector: selector, ModelProfile: tokenBudget.Profile,
		MemoryMaxRatio: cfg.Agent.ContextMemory.MemoryMaxRatio, SummaryMaxRatio: cfg.Agent.ContextMemory.SummaryMaxRatio,
		SummaryPromptMaxEntries: cfg.Agent.ContextMemory.Summary.EffectivePromptMaxEntries(),
		TailMaxRatio:            cfg.Agent.ContextMemory.TailMaxRatio, SoftThresholdRatio: cfg.Agent.ContextMemory.SoftThresholdRatio,
		HardThresholdRatio: cfg.Agent.ContextMemory.HardThresholdRatio, ToolGrowthReserveTokens: cfg.Agent.ContextMemory.ToolGrowthReserveTokens,
		SyncCompactionTimeout: time.Duration(cfg.Agent.ContextMemory.SyncCompactionTimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	_ = activationGate
	preflight.ContinuousTailEnabled, preflight.SummaryTailEnabled, preflight.HardWindowEnforced = true, true, true
	preflight.TailSelector, preflight.Memory = selector, memoryService
	preflight.MemoryMaxRatio, preflight.SummaryMaxRatio, preflight.TailMaxRatio = cfg.Agent.ContextMemory.MemoryMaxRatio, cfg.Agent.ContextMemory.SummaryMaxRatio, cfg.Agent.ContextMemory.TailMaxRatio
	summaryContract := &mesagent.ContextGovernancePilotSummaryContract{
		ModelProvider: strings.ToLower(strings.TrimSpace(summaryProfile.Provider)), ModelID: strings.TrimSpace(summaryProfile.Model),
		ModelProfile: strings.TrimSpace(cfg.Models.Chat.ConversationMemoryProfileName), ModelProfileFingerprint: summaryFingerprint,
		PromptVersion: cfg.Agent.ContextMemory.Summary.PromptVersion,
	}
	if err := summaryContract.Validate(); err != nil {
		return mesagent.ConversationContextPreflightConfig{}, nil, nil, err
	}
	return preflight, summaryModel, summaryContract, nil
}

func runPilotArms(
	ctx context.Context, dataset mesagent.ContextGovernancePilotDataset,
	contract mesagent.ContextGovernancePilotContract,
	runtimes map[string]map[mesagent.ContextGovernancePilotArm]pilotArmRuntime,
	mainModel *mesagent.PilotMeasuredModel,
	selection pilotSelection,
	progress io.Writer,
	persist func(mesagent.ContextGovernancePilotObservation) error,
) ([]mesagent.ContextGovernancePilotObservation, error) {
	var observations []mesagent.ContextGovernancePilotObservation
	for _, scenario := range dataset.Scenarios {
		for _, arm := range []mesagent.ContextGovernancePilotArm{mesagent.PilotArmCurrent, mesagent.PilotArmBaseline, mesagent.PilotArmExperiment} {
			selectedCheckpoints := make([]mesagent.ContextGovernancePilotCheckpoint, 0, len(scenario.Checkpoints))
			for _, checkpoint := range scenario.Checkpoints {
				if selection.includes(scenario.ScenarioID, checkpoint.CheckpointID, arm) {
					selectedCheckpoints = append(selectedCheckpoints, checkpoint)
				}
			}
			if len(selectedCheckpoints) == 0 {
				continue
			}
			runtime := runtimes[scenario.ScenarioID][arm]
			conversationID := uuid.New()
			timeline := newPilotScenarioTimeline(scenario, string(arm), conversationID)
			for _, checkpoint := range scenario.Checkpoints {
				history, current, timelineErr := timeline.Request(checkpoint)
				if timelineErr != nil {
					return nil, fmt.Errorf("build %s/%s Pilot timeline: %w", arm, checkpoint.CheckpointID, timelineErr)
				}
				if !selection.includes(scenario.ScenarioID, checkpoint.CheckpointID, arm) {
					timeline.Complete(checkpoint, current)
					continue
				}
				started := time.Now()
				fmt.Fprintf(progress, "pilot scenario=%s arm=%s checkpoint=%s stage=started\n", scenario.ScenarioID, arm, checkpoint.CheckpointID)
				request := conversation.AgentRequest{
					Conversation: conversation.Conversation{ID: conversationID, UserID: uuid.Nil, Status: conversation.StatusActive},
					UserMessage:  current,
					History:      history,
				}
				// Respond requires a real command actor; keep the fixture isolated by
				// using one synthetic actor per scenario/arm.
				actor := conversation.Actor{UserID: pilotActorID(scenario.ScenarioID, string(arm))}
				commandCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{ConversationID: conversationID, UserMessageID: current.ID, Actor: actor})
				request.Conversation.UserID = actor.UserID
				beforeMain := mainModel.Snapshot()
				beforeSummary := runtime.summaryModelSnapshot()
				response, runErr := runtime.runner.Respond(commandCtx, request)
				mainDelta := mainModel.Delta(beforeMain)
				summaryDelta := runtime.summaryModelDelta(beforeSummary)
				observation := makePilotObservation(dataset, scenario, checkpoint, arm, contract, runtime.summaryContract, response, runErr, mainDelta, summaryDelta)
				if err := observation.Validate(); err != nil {
					return nil, fmt.Errorf("validate %s/%s Pilot observation: %w", arm, checkpoint.CheckpointID, err)
				}
				if persist != nil {
					if err := persist(observation); err != nil {
						return nil, fmt.Errorf("persist %s/%s Pilot observation: %w", arm, checkpoint.CheckpointID, err)
					}
				}
				observations = append(observations, observation)
				fmt.Fprintf(progress, "pilot scenario=%s arm=%s checkpoint=%s stage=completed main_calls=%d summary_calls=%d elapsed=%s error=%s\n",
					scenario.ScenarioID, arm, checkpoint.CheckpointID, mainDelta.Usage.ModelCalls,
					summaryDelta.ModelCalls, time.Since(started).Round(time.Millisecond), observation.ErrorType)
				timeline.Complete(checkpoint, current)
			}
		}
	}
	return observations, nil
}

func (r pilotArmRuntime) summaryModelSnapshot() mesagent.PilotMeasuredModelSnapshot {
	if r.summaryModel == nil {
		return mesagent.PilotMeasuredModelSnapshot{}
	}
	return r.summaryModel.Snapshot()
}
func (r pilotArmRuntime) summaryModelDelta(before mesagent.PilotMeasuredModelSnapshot) mesagent.ContextGovernancePilotUsage {
	if r.summaryModel == nil {
		return mesagent.ContextGovernancePilotUsage{}
	}
	return r.summaryModel.Delta(before).Usage
}

func makePilotObservation(
	dataset mesagent.ContextGovernancePilotDataset, scenario mesagent.ContextGovernancePilotScenario,
	checkpoint mesagent.ContextGovernancePilotCheckpoint, arm mesagent.ContextGovernancePilotArm,
	contract mesagent.ContextGovernancePilotContract, summaryContract *mesagent.ContextGovernancePilotSummaryContract,
	response conversation.AgentResponse, runErr error,
	mainDelta mesagent.PilotMeasuredModelSnapshot, summaryDelta mesagent.ContextGovernancePilotUsage,
) mesagent.ContextGovernancePilotObservation {
	fixtureFingerprint, _ := mesagent.ContextGovernancePilotDatasetFingerprint(dataset)
	observation := mesagent.ContextGovernancePilotObservation{
		DatasetVersion: dataset.DatasetVersion, FixtureVersion: dataset.FixtureVersion, FixtureFingerprint: fixtureFingerprint, ScenarioID: scenario.ScenarioID,
		CheckpointID: checkpoint.CheckpointID, RunID: fmt.Sprintf("%s-%s", arm, checkpoint.CheckpointID), Arm: arm,
		Contract: contract, Answer: response.Content, MainUsage: mainDelta.Usage, SummaryUsage: summaryDelta,
		FirstTokenLatencyMillis: mainDelta.LastFirstTokenLatencyMS, WithinHardWindow: true,
	}
	if summaryContract != nil {
		copyContract := *summaryContract
		observation.SummaryContract = &copyContract
	}
	if response.RunObservation != nil && response.RunObservation.PromptManifest != nil {
		manifest := response.RunObservation.PromptManifest
		observation.EstimatedPromptTokens, observation.PromptEpochID = manifest.EstimatedPromptTokens, manifest.PromptEpochID
		observation.WithinHardWindow = !manifest.ExceedsHardWindow
	}
	if response.RunObservation == nil && runErr != nil {
		if failure, ok := conversation.AgentRunFailureRecordFrom(runErr); ok && failure.Observation.PromptManifest != nil {
			manifest := failure.Observation.PromptManifest
			observation.EstimatedPromptTokens, observation.PromptEpochID = manifest.EstimatedPromptTokens, manifest.PromptEpochID
			observation.WithinHardWindow = !manifest.ExceedsHardWindow
		}
	}
	if runErr != nil {
		observation.ErrorType = pilotErrorType(runErr)
		observation.SummaryAttemptFailureCodes = conversationmemory.CompactionAttemptFailureCodes(runErr)
		if errors.Is(runErr, mesagent.ErrConversationPromptWindowExceeded) {
			observation.WithinHardWindow = false
		}
	}
	if observation.ErrorType == "" && observation.WithinHardWindow && observation.FirstTokenLatencyMillis < 1 {
		observation.FirstTokenLatencyMillis = 1
	}
	return observation
}

func pilotErrorType(err error) string {
	if errors.Is(err, mesagent.ErrConversationPromptWindowExceeded) {
		return "prompt_window_exceeded"
	}
	if errors.Is(err, memorycompactor.ErrProviderRequest) {
		if codes := conversationmemory.CompactionAttemptFailureCodes(err); len(codes) > 0 {
			return "summary_" + codes[len(codes)-1]
		}
		var apiErr *modelopenai.APIError
		if errors.As(err, &apiErr) && apiErr != nil {
			switch {
			case apiErr.HTTPStatusCode == 400:
				return "summary_bad_request"
			case apiErr.HTTPStatusCode == 401 || apiErr.HTTPStatusCode == 403:
				return "summary_auth_failed"
			case apiErr.HTTPStatusCode == 429:
				return "summary_rate_limited"
			case apiErr.HTTPStatusCode >= 500:
				return "summary_provider_5xx"
			}
		}
		if isTimeoutError(err) {
			return "summary_timeout"
		}
		return "summary_provider_failed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "agent_timeout"
	}
	if errors.Is(err, memorycompactor.ErrOutputTooLarge) {
		return "summary_output_too_large"
	}
	if errors.Is(err, memorycompactor.ErrOutputTruncated) {
		return "summary_output_truncated"
	}
	if code := conversationmemory.FailureCode(err); code != "" {
		return "summary_" + code
	}
	if errors.Is(err, conversationmemory.ErrInvalidSnapshot) {
		return "summary_snapshot_invalid"
	}
	if errors.Is(err, conversationmemory.ErrCompactionFailed) {
		return "summary_compaction_failed"
	}
	if errors.Is(err, mesagent.ErrConversationContextPreparationFailed) {
		return "context_preparation_failed"
	}
	return "provider_or_runner_failed"
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

type pilotScenarioTimeline struct {
	scenario       mesagent.ContextGovernancePilotScenario
	arm            string
	conversationID uuid.UUID
	history        []conversation.Message
	baseCount      int
	nextSeq        int64
}

func newPilotScenarioTimeline(
	scenario mesagent.ContextGovernancePilotScenario,
	arm string,
	conversationID uuid.UUID,
) *pilotScenarioTimeline {
	return &pilotScenarioTimeline{scenario: scenario, arm: arm, conversationID: conversationID, nextSeq: 1}
}

func (t *pilotScenarioTimeline) Request(
	checkpoint mesagent.ContextGovernancePilotCheckpoint,
) ([]conversation.Message, conversation.Message, error) {
	target := int(checkpoint.HistoryThroughSeq)
	if t == nil || t.conversationID == uuid.Nil || target < t.baseCount || target > len(t.scenario.History) {
		return nil, conversation.Message{}, errors.New("Pilot checkpoint history boundary is invalid")
	}
	for t.baseCount < target {
		item := t.scenario.History[t.baseCount]
		role := conversation.MessageRoleUser
		if item.Role == "assistant" {
			role = conversation.MessageRoleAssistant
		}
		t.history = append(t.history, conversation.Message{
			ID:             pilotMessageID(t.scenario.ScenarioID, t.arm, fmt.Sprintf("base-%d", item.Seq)),
			ConversationID: t.conversationID, Seq: t.nextSeq, Role: role, Content: item.Content,
		})
		for _, reference := range item.ReportReferences {
			t.history[len(t.history)-1].ReportReferences = append(
				t.history[len(t.history)-1].ReportReferences,
				conversation.ReportReference{ReferenceID: reference},
			)
		}
		t.baseCount++
		t.nextSeq++
	}
	current := conversation.Message{
		ID:             pilotMessageID(t.scenario.ScenarioID, t.arm, checkpoint.CheckpointID+"-question"),
		ConversationID: t.conversationID, Seq: t.nextSeq, Role: conversation.MessageRoleUser,
		Content: checkpoint.Question,
	}
	return append([]conversation.Message(nil), t.history...), current, nil
}

func (t *pilotScenarioTimeline) Complete(
	checkpoint mesagent.ContextGovernancePilotCheckpoint,
	current conversation.Message,
) {
	if t == nil || current.ID == uuid.Nil || current.Seq != t.nextSeq {
		return
	}
	t.history = append(t.history, current)
	t.nextSeq++
	// Provider answers are observations, not input to later checkpoints. A
	// stable acknowledgement keeps all arms on the same controlled history.
	t.history = append(t.history, conversation.Message{
		ID:             pilotMessageID(t.scenario.ScenarioID, t.arm, checkpoint.CheckpointID+"-ack"),
		ConversationID: t.conversationID, Seq: t.nextSeq, Role: conversation.MessageRoleAssistant,
		Content: "该检查点回答已独立记录；继续依据后续新增证据更新当前结论。",
	})
	t.nextSeq++
}

func pilotMessageID(scenario, arm, key string) uuid.UUID {
	return uuid.NewMD5(uuid.Nil, []byte(scenario+"\x00"+arm+"\x00"+key))
}
func pilotActorID(scenario, arm string) uuid.UUID {
	return uuid.NewMD5(uuid.Nil, []byte("actor\x00"+scenario+"\x00"+arm))
}

type pilotExternalCaseGetter struct{}

func (pilotExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("Pilot external case is unavailable")
}

func planPricing() mesagent.ContextGovernancePilotPricing {
	return mesagent.DefaultContextGovernancePilotPlanOptions().Pricing
}

func pilotReasoningMode(identity chatmodel.Identity) string {
	if effort := strings.ToLower(strings.TrimSpace(identity.ReasoningEffort)); effort != "" {
		return "effort:" + effort
	}
	if thinking := strings.ToLower(strings.TrimSpace(identity.ThinkingMode)); thinking != "" {
		return "thinking:" + thinking
	}
	return "reasoning:unspecified"
}

func validatePilotPressure(
	ctx context.Context,
	dataset mesagent.ContextGovernancePilotDataset,
	tokenBudget bootstrap.ConversationTokenBudgetRuntime,
	systemInstruction string,
	toolContractJSON string,
	memory config.ContextMemoryConfig,
) error {
	wantHard := []bool{false, true, true}
	wantExceeds := []bool{false, false, true}
	for _, scenario := range dataset.Scenarios {
		timeline := newPilotScenarioTimeline(scenario, "pressure", uuid.New())
		for index, checkpoint := range scenario.Checkpoints {
			history, current, err := timeline.Request(checkpoint)
			if err != nil {
				return err
			}
			var historyText strings.Builder
			for _, message := range history {
				fmt.Fprintf(&historyText, "role=%s seq=%d\n%s\n", message.Role, message.Seq, strings.TrimSpace(message.Content))
			}
			plan, err := tokenBudget.Planner.Plan(ctx, contextgovernance.TokenBudgetRequest{
				ContextWindowTokens: tokenBudget.Profile.ContextWindowTokens,
				MaxOutputTokens:     tokenBudget.Profile.MaxOutputTokens,
				SafetyMarginTokens:  tokenBudget.Profile.SafetyMarginTokens,
				SoftThresholdRatio:  memory.SoftThresholdRatio,
				HardThresholdRatio:  memory.HardThresholdRatio,
				Prompt: contextgovernance.PromptInput{
					Profile: tokenBudget.Profile.Name,
					Segments: []contextgovernance.PromptSegment{
						{Kind: contextgovernance.PromptSegmentSystem, Content: systemInstruction},
						{Kind: contextgovernance.PromptSegmentToolSchema, Content: toolContractJSON},
						{Kind: contextgovernance.PromptSegmentPreloadedSkill},
						{Kind: contextgovernance.PromptSegmentSummary},
						{Kind: contextgovernance.PromptSegmentHistory, Content: historyText.String()},
						{Kind: contextgovernance.PromptSegmentDynamicReferences},
						{Kind: contextgovernance.PromptSegmentCurrentUser, Content: current.Content},
						{Kind: contextgovernance.PromptSegmentToolGrowthReserve, ReservedTokens: memory.ToolGrowthReserveTokens},
					},
				},
			})
			if err != nil {
				return fmt.Errorf("plan %s/%s Pilot pressure: %w", scenario.ScenarioID, checkpoint.CheckpointID, err)
			}
			if plan.HardThresholdReached != wantHard[index] || plan.ExceedsHardWindow != wantExceeds[index] {
				return fmt.Errorf(
					"Pilot fixture pressure does not match the selected model profile at %s/%s: hard=%t exceeds=%t upper=%d available=%d",
					scenario.ScenarioID, checkpoint.CheckpointID, plan.HardThresholdReached,
					plan.ExceedsHardWindow, plan.EstimatedUpperBoundTokens, plan.AvailableInputTokens,
				)
			}
			timeline.Complete(checkpoint, current)
		}
	}
	return nil
}

func readPilotObservations(
	path string,
	dataset mesagent.ContextGovernancePilotDataset,
	requireExisting bool,
) ([]mesagent.ContextGovernancePilotObservation, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if requireExisting {
			return nil, errors.New("resume output does not exist; omit -resume for the first batch")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	observations := make([]mesagent.ContextGovernancePilotObservation, 0)
	for index := 0; ; index++ {
		var observation mesagent.ContextGovernancePilotObservation
		if err := decoder.Decode(&observation); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode observation %d: %w", index+1, err)
		}
		observations = append(observations, observation)
	}
	if err := validatePilotObservationSet(dataset, observations); err != nil {
		return nil, err
	}
	return observations, nil
}

func mergePilotObservations(
	dataset mesagent.ContextGovernancePilotDataset,
	existing, added []mesagent.ContextGovernancePilotObservation,
) ([]mesagent.ContextGovernancePilotObservation, error) {
	merged := append(append(make([]mesagent.ContextGovernancePilotObservation, 0, len(existing)+len(added)), existing...), added...)
	if err := validatePilotObservationSet(dataset, merged); err != nil {
		return nil, err
	}
	scenarioOrder := make(map[string]int, len(dataset.Scenarios))
	checkpointOrder := make(map[string]int)
	for scenarioIndex, scenario := range dataset.Scenarios {
		scenarioOrder[scenario.ScenarioID] = scenarioIndex
		for checkpointIndex, checkpoint := range scenario.Checkpoints {
			checkpointOrder[checkpoint.CheckpointID] = checkpointIndex
		}
	}
	armOrder := make(map[mesagent.ContextGovernancePilotArm]int, len(pilotArmOrder()))
	for index, arm := range pilotArmOrder() {
		armOrder[arm] = index
	}
	sort.SliceStable(merged, func(left, right int) bool {
		leftValue, rightValue := merged[left], merged[right]
		if scenarioOrder[leftValue.ScenarioID] != scenarioOrder[rightValue.ScenarioID] {
			return scenarioOrder[leftValue.ScenarioID] < scenarioOrder[rightValue.ScenarioID]
		}
		if checkpointOrder[leftValue.CheckpointID] != checkpointOrder[rightValue.CheckpointID] {
			return checkpointOrder[leftValue.CheckpointID] < checkpointOrder[rightValue.CheckpointID]
		}
		return armOrder[leftValue.Arm] < armOrder[rightValue.Arm]
	})
	return merged, nil
}

func validatePilotObservationSet(
	dataset mesagent.ContextGovernancePilotDataset,
	observations []mesagent.ContextGovernancePilotObservation,
) error {
	expectedFixtureFingerprint, err := mesagent.ContextGovernancePilotDatasetFingerprint(dataset)
	if err != nil {
		return err
	}
	scenarioByCheckpoint := make(map[string]string)
	for _, scenario := range dataset.Scenarios {
		for _, checkpoint := range scenario.Checkpoints {
			scenarioByCheckpoint[checkpoint.CheckpointID] = scenario.ScenarioID
		}
	}
	seenRuns := make(map[string]struct{}, len(observations))
	seenKeys := make(map[pilotObservationKey]struct{}, len(observations))
	var commonContract *mesagent.ContextGovernancePilotContract
	var commonSummaryContract *mesagent.ContextGovernancePilotSummaryContract
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("observation %d: %w", index+1, err)
		}
		if observation.DatasetVersion != dataset.DatasetVersion || observation.FixtureVersion != dataset.FixtureVersion ||
			observation.FixtureFingerprint != expectedFixtureFingerprint ||
			scenarioByCheckpoint[observation.CheckpointID] != observation.ScenarioID {
			return fmt.Errorf("observation %q does not match the Pilot fixture", observation.RunID)
		}
		if _, duplicate := seenRuns[observation.RunID]; duplicate {
			return fmt.Errorf("duplicate runId %q", observation.RunID)
		}
		seenRuns[observation.RunID] = struct{}{}
		key := pilotObservationKey{checkpointID: observation.CheckpointID, arm: observation.Arm}
		if _, duplicate := seenKeys[key]; duplicate {
			return fmt.Errorf("duplicate %s observation for %s", observation.Arm, observation.CheckpointID)
		}
		seenKeys[key] = struct{}{}
		if commonContract == nil {
			contract := observation.Contract
			commonContract = &contract
		} else if *commonContract != observation.Contract {
			return errors.New("Pilot observations do not share one main-model, Tool, output, and Prompt contract")
		}
		if observation.SummaryContract != nil {
			if commonSummaryContract == nil {
				contract := *observation.SummaryContract
				commonSummaryContract = &contract
			} else if *commonSummaryContract != *observation.SummaryContract {
				return errors.New("Experiment observations do not share one Summary model, Profile, and Prompt contract")
			}
		}
	}
	return nil
}

func validateResumedPilotContracts(
	existing []mesagent.ContextGovernancePilotObservation,
	mainContract mesagent.ContextGovernancePilotContract,
	summaryContract *mesagent.ContextGovernancePilotSummaryContract,
) error {
	for _, observation := range existing {
		if observation.Contract != mainContract {
			return errors.New("resumed Pilot main-model, Tool, output, or Prompt contract differs from the current configuration")
		}
		if observation.SummaryContract != nil && summaryContract != nil && *observation.SummaryContract != *summaryContract {
			return errors.New("resumed Pilot Summary model, Profile, or Prompt contract differs from the current configuration")
		}
	}
	return nil
}

func pilotArmOrder() []mesagent.ContextGovernancePilotArm {
	return []mesagent.ContextGovernancePilotArm{
		mesagent.PilotArmCurrent,
		mesagent.PilotArmBaseline,
		mesagent.PilotArmExperiment,
	}
}

func expectedPilotObservationCount(dataset mesagent.ContextGovernancePilotDataset) int {
	count := 0
	for _, scenario := range dataset.Scenarios {
		count += len(scenario.Checkpoints) * len(pilotArmOrder())
	}
	return count
}

func readStrictJSON(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func writeJSONL(path string, values []mesagent.ContextGovernancePilotObservation) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".context-governance-pilot-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = temp.Close()
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, path)
}
func writeJSONFile(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".context-governance-pilot-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := writeJSONTo(temp, value); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, path)
}
func writeJSONTo(writer io.Writer, value any) error { return json.NewEncoder(writer).Encode(value) }

func replaceFile(source, target string) error {
	backup := target + ".backup-" + uuid.NewString()
	targetExists := false
	if _, err := os.Stat(target); err == nil {
		targetExists = true
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if targetExists {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if targetExists {
		if err := os.Remove(backup); err != nil {
			return err
		}
	}
	return nil
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
