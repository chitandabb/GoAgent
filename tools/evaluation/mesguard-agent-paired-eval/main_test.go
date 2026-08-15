package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type pairedEvaluationModelStub struct{}

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

	orchestrator, fingerprint, err := buildPairedEvaluationRun(
		context.Background(), cfg, prompts, pairedEvaluationModelStub{},
		nil, nil, nil, nil, zap.NewNop(),
		mesagent.EvaluationExperiment, "tool-selection",
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
		implementationIdentity{revision: "git:test-revision", dirty: false},
		strings.Repeat("a", 64),
	)
	if observation.PromptVersion != "diagnosis-v7" {
		t.Fatalf("PromptVersion = %q, want diagnosis-v7", observation.PromptVersion)
	}
}

func TestNewEvaluationObservationRecordsV2ArmSpecificIdentity(t *testing.T) {
	base := mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"}
	cfg := config.Config{Agent: config.AgentConfig{PromptVersion: "diagnosis-v7"}}
	identity := implementationIdentity{revision: "git:test-revision", dirty: false}
	experiment := observationFromResult(
		base, mesagent.EvaluationExperiment, cfg, mesagent.OrchestrationResult{},
		time.Second, strings.Repeat("2", 64), identity, strings.Repeat("a", 64),
	)
	baseline := observationFromResult(
		base, mesagent.EvaluationBaseline, cfg, mesagent.OrchestrationResult{},
		time.Second, strings.Repeat("1", 64), identity, strings.Repeat("a", 64),
	)
	if experiment.ObservationSchemaVersion != mesagent.EvaluationObservationV2 ||
		baseline.ObservationSchemaVersion != mesagent.EvaluationObservationV2 {
		t.Fatal("observations must carry the v2 observation schema version")
	}
	if experiment.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
		t.Fatalf("experiment toolProfileId = %q, want diagnosis-default", experiment.ToolProfileID)
	}
	if baseline.ToolProfileID != string(agentruntime.ToolProfileEvaluationWide) {
		t.Fatalf("baseline toolProfileId = %q, want evaluation-wide-v1", baseline.ToolProfileID)
	}
	if experiment.ToolSchemaFingerprint != strings.Repeat("2", 64) ||
		baseline.ToolSchemaFingerprint != strings.Repeat("1", 64) {
		t.Fatal("toolSchemaFingerprint must be arm-specific")
	}
	if experiment.ModelProfileFingerprint != strings.Repeat("a", 64) ||
		experiment.ImplementationRevision != "git:test-revision" || experiment.ImplementationDirty {
		t.Fatalf("experiment identity fields = %+v", experiment)
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
