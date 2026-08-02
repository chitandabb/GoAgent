package config

import "testing"

func TestAgentConfigValidate(t *testing.T) {
	valid := validAgentConfigForTest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid config: %v", err)
	}
	configured := valid
	configured.MaxAgentRuns = 2
	configured.MaxToolCalls = 8
	configured.MaxEvidenceItems = 16
	configured.MaxTotalTokens = 16000
	configured.TimeoutMillis = 90000
	if err := configured.Validate(); err != nil {
		t.Fatalf("Validate configured budgets: %v", err)
	}
	if err := (AgentConfig{}).Validate(); err == nil {
		t.Fatal("Validate accepted empty skillsDirectory")
	}
	invalidBudget := valid
	invalidBudget.MaxTotalTokens = 999
	if err := invalidBudget.Validate(); err == nil {
		t.Fatal("Validate accepted too-small maxTotalTokens")
	}
	invalidVersion := valid
	invalidVersion.PromptVersion = "invalid version"
	if err := invalidVersion.Validate(); err == nil {
		t.Fatal("Validate accepted invalid promptVersion")
	}
	missingPromptPath := valid
	missingPromptPath.ReportContractFile = ""
	if err := missingPromptPath.Validate(); err == nil {
		t.Fatal("Validate accepted empty reportContractFile")
	}
}

func validAgentConfigForTest() AgentConfig {
	return AgentConfig{
		SkillsDirectory:    "config/skills",
		PromptVersion:      "diagnosis-v1",
		SystemPromptFile:   "config/prompts/diagnosis-system.md",
		BaselinePromptFile: "config/prompts/evaluation-baseline.md",
		ReportContractFile: "config/prompts/report-contract.md",
	}
}
