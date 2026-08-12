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
	"strings"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/auth"
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
	maxMainCalls := flags.Int("max-main-calls", 1, "hard main-model call limit enforced before Provider access")
	maxSummaryCalls := flags.Int("max-summary-calls", 1, "hard Summary call limit enforced before Provider access")
	maxMainPromptTokens := flags.Int("max-estimated-main-prompt-tokens", 130000, "cumulative estimated main prompt Token limit")
	maxSummaryPromptTokens := flags.Int("max-estimated-summary-prompt-tokens", 130000, "cumulative estimated Summary prompt Token limit")
	maxEstimatedCostCNY := flags.Float64("max-estimated-cost-cny", 0.50, "conservative estimated cost admission limit")
	summaryTimeout := flags.Duration("summary-timeout", 0, "optional observer-only Summary timeout override (1s..5m)")
	summaryAttempts := flags.Int("summary-attempts", 0, "optional observer-only Summary attempt override (1..5)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-context-governance-pilot-observe [-execute-provider] [-config path] [-fixture path] [-output path] [-summary-output path] [-scenario-id id] [-checkpoint-id id] [-arm arm] [-summary-timeout duration] [-summary-attempts n] [-max-main-calls n] [-max-summary-calls n] [-max-estimated-main-prompt-tokens n] [-max-estimated-summary-prompt-tokens n] [-max-estimated-cost-cny amount]")
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
	selection, err := newPilotSelection(dataset, *scenarioID, *checkpointID, *arm)
	if err != nil {
		return err
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
	observations, err := executePilot(ctx, cfg, dataset, plan, selection, executionLimits, progress)
	if err != nil {
		return err
	}
	if err := writeJSONL(*outputPath, observations); err != nil {
		return err
	}
	if !selection.partial() {
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
		Output         string `json:"output"`
		SummaryOutput  string `json:"summaryOutput"`
	}{dataset.DatasetVersion, dataset.FixtureVersion, len(observations), *outputPath, *summaryOutputPath})
}

type pilotSelection struct {
	scenarioID   string
	checkpointID string
	arm          mesagent.ContextGovernancePilotArm
}

func newPilotSelection(
	dataset mesagent.ContextGovernancePilotDataset,
	scenarioID, checkpointID, arm string,
) (pilotSelection, error) {
	selection := pilotSelection{
		scenarioID: strings.TrimSpace(scenarioID), checkpointID: strings.TrimSpace(checkpointID),
		arm: mesagent.ContextGovernancePilotArm(strings.ToLower(strings.TrimSpace(arm))),
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
	return s.scenarioID != "" || s.checkpointID != "" || s.arm != ""
}

func (s pilotSelection) includes(
	scenarioID, checkpointID string,
	arm mesagent.ContextGovernancePilotArm,
) bool {
	return (s.scenarioID == "" || s.scenarioID == scenarioID) &&
		(s.checkpointID == "" || s.checkpointID == checkpointID) &&
		(s.arm == "" || s.arm == arm)
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
	executionLimits mesagent.PilotModelCallLimits,
	progress io.Writer,
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
	catalog, err := mesagent.NewDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: pilotExternalCaseGetter{},
	})
	if err != nil {
		return nil, fmt.Errorf("build Pilot Tool catalog: %w", err)
	}
	actorID := uuid.New()
	scope, err := mesagent.NewTaskScope(mesagent.TaskScopeConfig{
		UserID: actorID, Role: auth.RoleAnalyst, TaskType: mesagent.TaskTypeConversation,
	})
	if err != nil {
		return nil, err
	}
	tools, err := catalog.ToolsFor(ctx, scope)
	if err != nil {
		return nil, err
	}
	toolContract, err := mesagent.CanonicalToolContract(ctx, tools)
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
	return runPilotArms(ctx, dataset, contract, armRuntimes, mainModel, selection, progress)
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
		TailMaxRatio: cfg.Agent.ContextMemory.TailMaxRatio, SoftThresholdRatio: cfg.Agent.ContextMemory.SoftThresholdRatio,
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
					UserMessage:  current, History: history,
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
	observation := mesagent.ContextGovernancePilotObservation{
		DatasetVersion: dataset.DatasetVersion, FixtureVersion: dataset.FixtureVersion, ScenarioID: scenario.ScenarioID,
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
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
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
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}
func writeJSONTo(writer io.Writer, value any) error { return json.NewEncoder(writer).Encode(value) }
