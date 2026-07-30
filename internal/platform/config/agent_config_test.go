package config

import "testing"

func TestAgentConfigValidate(t *testing.T) {
	if err := (AgentConfig{SkillsDirectory: "config/skills"}).Validate(); err != nil {
		t.Fatalf("Validate valid config: %v", err)
	}
	if err := (AgentConfig{}).Validate(); err == nil {
		t.Fatal("Validate accepted empty skillsDirectory")
	}
}
