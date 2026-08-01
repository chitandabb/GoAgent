package config

import "testing"

func TestAgentConfigValidate(t *testing.T) {
	if err := (AgentConfig{SkillsDirectory: "config/skills"}).Validate(); err != nil {
		t.Fatalf("Validate valid config: %v", err)
	}
	if err := (AgentConfig{
		SkillsDirectory: "config/skills", MaxAgentRuns: 2, MaxToolCalls: 8,
		MaxEvidenceItems: 16, MaxTotalTokens: 16000, TimeoutMillis: 90000,
	}).Validate(); err != nil {
		t.Fatalf("Validate configured budgets: %v", err)
	}
	if err := (AgentConfig{}).Validate(); err == nil {
		t.Fatal("Validate accepted empty skillsDirectory")
	}
	if err := (AgentConfig{SkillsDirectory: "config/skills", MaxTotalTokens: 999}).Validate(); err == nil {
		t.Fatal("Validate accepted too-small maxTotalTokens")
	}
}
