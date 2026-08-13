package main

import (
	"errors"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestNewEvaluationObservationUsesConfiguredPromptVersion(t *testing.T) {
	observation := observationFromResult(
		mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"},
		mesagent.EvaluationExperiment,
		config.Config{Agent: config.AgentConfig{PromptVersion: "diagnosis-v7"}},
		mesagent.OrchestrationResult{},
		time.Second,
	)
	if observation.PromptVersion != "diagnosis-v7" {
		t.Fatalf("PromptVersion = %q, want diagnosis-v7", observation.PromptVersion)
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
