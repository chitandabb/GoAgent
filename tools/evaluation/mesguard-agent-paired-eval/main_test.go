package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type pairedEvaluationModelStub struct{}

func pairedComparabilityForTest() mesagent.ToolSelectionComparability {
	return mesagent.ToolSelectionComparability{
		ComparisonFingerprint: "sha256:" + strings.Repeat("c", 64),
		SharedToolNames:       []string{mesagent.ToolReadExternalCase, mesagent.ToolSkill},
		BaselineOnlyToolNames: []string{"search_code"},
	}
}

func (pairedEvaluationModelStub) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (pairedEvaluationModelStub) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := (pairedEvaluationModelStub{}).Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (pairedEvaluationModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return pairedEvaluationModelStub{}, nil
}

func TestBuildPairedEvaluationRunUsesStrictEvidenceReportPolicy(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{
		SkillsDirectory: filepath.Join("..", "..", "..", "config", "skills"),
		MaxAgentRuns:    2, MaxToolCalls: 8, MaxEvidenceItems: 16,
		MaxTotalTokens: 16000, TimeoutMillis: 60000,
	}}
	prompts := config.AgentPrompts{
		SystemInstruction:         "system",
		BaselineInstruction:       "baseline",
		ReportContractInstruction: "report contract",
	}
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}

	orchestrator, fingerprint, err := buildPairedEvaluationRun(
		context.Background(), cfg, prompts, pairedEvaluationModelStub{},
		zap.NewNop(), mesagent.EvaluationExperiment, "tool-selection", assembly,
	)
	if err != nil {
		t.Fatalf("buildPairedEvaluationRun: %v", err)
	}
	if orchestrator == nil || len(fingerprint) != 64 {
		t.Fatalf("orchestrator=%v fingerprint=%q", orchestrator, fingerprint)
	}
}

func TestNewEvaluationObservationUsesConfiguredPromptVersion(t *testing.T) {
	observation := observationFromResult(
		mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"},
		mesagent.EvaluationExperiment,
		config.Config{Agent: config.AgentConfig{PromptVersion: "diagnosis-v7"}},
		mesagent.OrchestrationResult{},
		time.Second,
		strings.Repeat("2", 64),
		pairedComparabilityForTest(),
		evaluationidentity.Identity{Revision: "git:test-revision", Dirty: false},
		strings.Repeat("a", 64),
	)
	if observation.PromptVersion != "diagnosis-v7" {
		t.Fatalf("PromptVersion = %q, want diagnosis-v7", observation.PromptVersion)
	}
}

func TestNewEvaluationObservationRecordsV3ArmSpecificIdentity(t *testing.T) {
	base := mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"}
	cfg := pairedEvalTestConfig()
	identity := evaluationidentity.Identity{Revision: "git:test-revision", Dirty: false}
	skillResult := mesagent.OrchestrationResult{
		SelectedSkill: mesagent.SkillTicketDiagnosis,
		AllowedTools:  []string{mesagent.ToolReadExternalCase},
		Report: mesagent.StructuredReport{
			ConclusionStatus: mesagent.ConclusionProbable,
		},
	}
	experiment := observationFromResult(
		base, mesagent.EvaluationExperiment, cfg, skillResult,
		time.Second, strings.Repeat("2", 64), pairedComparabilityForTest(), identity, strings.Repeat("a", 64),
	)
	baseline := observationFromResult(
		base, mesagent.EvaluationBaseline, cfg, skillResult,
		time.Second, strings.Repeat("1", 64), pairedComparabilityForTest(), identity, strings.Repeat("a", 64),
	)
	if experiment.ObservationSchemaVersion != mesagent.EvaluationObservationV3 ||
		baseline.ObservationSchemaVersion != mesagent.EvaluationObservationV3 {
		t.Fatal("observations must carry the v3 observation schema version")
	}
	if experiment.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
		t.Fatalf("experiment toolProfileId = %q, want diagnosis-default", experiment.ToolProfileID)
	}
	if baseline.ToolProfileID != string(agentruntime.ToolProfileEvaluationWide) {
		t.Fatalf("baseline toolProfileId = %q, want evaluation-wide-v2", baseline.ToolProfileID)
	}
	if experiment.ToolSchemaFingerprint != strings.Repeat("2", 64) ||
		baseline.ToolSchemaFingerprint != strings.Repeat("1", 64) {
		t.Fatal("toolSchemaFingerprint must be arm-specific")
	}
	if experiment.ModelProfileFingerprint != strings.Repeat("a", 64) ||
		experiment.ImplementationRevision != "git:test-revision" || experiment.ImplementationDirty {
		t.Fatalf("experiment identity fields = %+v", experiment)
	}
	if experiment.ComparisonFingerprint != pairedComparabilityForTest().ComparisonFingerprint ||
		!slices.Equal(experiment.SharedToolNames, pairedComparabilityForTest().SharedToolNames) ||
		!slices.Equal(experiment.BaselineOnlyToolNames, pairedComparabilityForTest().BaselineOnlyToolNames) {
		t.Fatalf("experiment comparison identity = %+v", experiment)
	}
	if err := experiment.Validate(); err != nil {
		t.Fatalf("experiment Validate: %v", err)
	}
	if err := baseline.Validate(); err != nil {
		t.Fatalf("baseline Validate: %v", err)
	}
}

func TestEvidenceGateObservationRecordsInvocationFailureWithoutQualityLabels(t *testing.T) {
	cfg := config.Config{
		Agent: config.AgentConfig{PromptVersion: "diagnosis-v1"},
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			ActiveProfileName: "fixture",
			Profiles: map[string]config.ChatModelProfileConfig{
				"fixture": {Provider: "stepfun", Model: "step-3.7-flash", ReasoningEffort: "medium"},
			},
		}},
	}
	observation := evidenceGateObservationFromResult(
		mesagent.EvaluationCase{DatasetVersion: "gate-v1", CaseID: "case-1"},
		mesagent.EvaluationExperiment,
		cfg,
		"sha256:pair",
		mesagent.OrchestrationResult{},
		time.Second,
		errors.New("provider unavailable"),
	)
	if err := observation.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if observation.Completed || observation.QualityReviewed ||
		observation.ErrorType != "provider_or_orchestration_error" ||
		len(observation.DegradationReasons) != 1 {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestValidateEvidenceGateProviderBudgetRequiresExplicitBoundedAuthorization(t *testing.T) {
	if _, err := validateEvidenceGateProviderBudget(3, 2, 8, 16000, false, 3, 60, 96000); err == nil {
		t.Fatal("Provider run was accepted without explicit authorization")
	}
	if _, err := validateEvidenceGateProviderBudget(3, 2, 8, 16000, true, 3, 59, 96000); err == nil {
		t.Fatal("Provider call upper bound exceeded the authorization")
	}
	budget, err := validateEvidenceGateProviderBudget(3, 2, 8, 16000, true, 3, 60, 96000)
	if err != nil {
		t.Fatalf("validateEvidenceGateProviderBudget: %v", err)
	}
	if budget.Cases != 3 || budget.ProviderCalls != 60 || budget.TotalTokens != 96000 {
		t.Fatalf("budget = %+v", budget)
	}
}

func pairedEvalTestConfig() config.Config {
	return config.Config{
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			Enabled:           true,
			ActiveProfileName: "stepfun-main",
			Profiles: map[string]config.ChatModelProfileConfig{
				"stepfun-main": {
					Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
					APIKeyEnv: "MESGUARD_STEPFUN_API_KEY",
					Model:     "step-3.7-flash", ReasoningEffort: "medium",
					TimeoutMillis:       120_000,
					ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
					PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
					TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
				},
			},
		}},
		GitHubMCP: config.GitHubMCPConfig{Enabled: true},
		Agent: config.AgentConfig{
			SkillsDirectory:        filepath.Join("..", "..", "..", "config", "skills"),
			PromptVersion:          "diagnosis-v7",
			SystemPromptFile:       filepath.Join("..", "..", "..", "config", "prompts", "diagnosis-system.md"),
			BaselinePromptFile:     filepath.Join("..", "..", "..", "config", "prompts", "evaluation-baseline.md"),
			ReportContractFile:     filepath.Join("..", "..", "..", "config", "prompts", "report-contract.md"),
			ConversationPromptFile: filepath.Join("..", "..", "..", "config", "prompts", "conversation-system.md"),
			MaxAgentRuns:           2, MaxToolCalls: 8, MaxEvidenceItems: 16,
			MaxTotalTokens: 16000, TimeoutMillis: 60000,
		},
	}
}

func writePairedDatasetForTest(t *testing.T) string {
	t.Helper()
	dataset := filepath.Join(t.TempDir(), "dataset.jsonl")
	line := `{"datasetVersion":"dev-v1","caseId":"ticket","taskType":"diagnosis","userQuery":"读取并诊断工单","expectedSkill":"ticket-diagnosis","expectedFirstTool":"read_external_case","expectedTools":["read_external_case"],"requiredEvidence":["ticket"],"expectedRootCause":"status-sync","acceptableConclusionStatuses":["probable"]}` + "\n"
	if err := os.WriteFile(dataset, []byte(line), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	return dataset
}

// TestPairedPreflightFailClosedBeforeProvider 证明两臂可比性校验在创建任何
// Provider 之前 fail-closed：verify 返回漂移错误时，runWithDependencies 立即
// 失败且 newChatModel factory 从未被调用（factory.calls == 0）。
func TestPairedPreflightFailClosedBeforeProvider(t *testing.T) {
	dataset := writePairedDatasetForTest(t)
	output := filepath.Join(t.TempDir(), "obs.jsonl")

	factoryCalls := atomic.Int32{}
	deps := defaultPairedEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		return nil, errors.New("factory must not be reached before comparability preflight")
	}
	deps.verifyPairedComparability = func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error) {
		return mesagent.ToolSelectionComparability{}, errors.New("drifted shared schema")
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-allow-dirty",
	}, zap.NewNop(), deps)
	if err == nil || !strings.Contains(err.Error(), "comparability preflight") {
		t.Fatalf("runWithDependencies error = %v, want comparability preflight rejection", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("newChatModel factory called %d times before comparability preflight, want 0", factoryCalls.Load())
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("observations output must not be created on preflight failure: %v", err)
	}
}

// TestPairedPreflightAcceptsRealArms 证明真实两臂装配与真实
// VerifyToolSelectionComparability 通过 preflight：wide 臂（evaluation-wide-
// v2 并集，经 Skill Middleware）是 production 的严格 Schema 超集，共享
// Tool 完全一致。
func TestPairedPreflightAcceptsRealArms(t *testing.T) {
	cfg := pairedEvalTestConfig()
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	if !strings.HasPrefix(assembly.comparability.ComparisonFingerprint, "sha256:") {
		t.Fatalf("comparisonFingerprint = %q", assembly.comparability.ComparisonFingerprint)
	}
	if len(assembly.comparability.SharedToolNames) == 0 || len(assembly.comparability.BaselineOnlyToolNames) == 0 {
		t.Fatalf("comparison Tool lists are empty: shared=%v baselineOnly=%v",
			assembly.comparability.SharedToolNames, assembly.comparability.BaselineOnlyToolNames)
	}
	if !slices.Contains(assembly.comparability.SharedToolNames, mesagent.ToolSkill) {
		t.Fatalf("sharedToolNames must include the Middleware-owned skill: %v", assembly.comparability.SharedToolNames)
	}
	if assembly.productionCatalog == nil || assembly.wideCatalog == nil || assembly.skillRuntime == nil ||
		len(assembly.productionSchemaFingerprint) != 64 || len(assembly.wideSchemaFingerprint) != 64 {
		t.Fatalf("preflight assembly is incomplete: %+v", assembly)
	}
}

// TestBuildPairedEvaluationRunRejectsSchemaDriftFromPreflight proves the
// runtime Runner is bound to the exact Tool Schema accepted by the preflight,
// rather than silently rebuilding and running a different contract.
func TestBuildPairedEvaluationRunRejectsSchemaDriftFromPreflight(t *testing.T) {
	cfg := pairedEvalTestConfig()
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	assembly.productionSchemaFingerprint = strings.Repeat("f", 64)

	_, _, err = buildPairedEvaluationRun(
		context.Background(), cfg, prompts, pairedEvaluationModelStub{}, zap.NewNop(),
		mesagent.EvaluationExperiment, "tool-selection", assembly,
	)
	if err == nil || !strings.Contains(err.Error(), "preflight Tool Schema") {
		t.Fatalf("buildPairedEvaluationRun error = %v, want preflight Schema drift rejection", err)
	}
}
