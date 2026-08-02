package main

import (
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
