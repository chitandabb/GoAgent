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
	configured.ConversationMaxIterations = 8
	configured.ConversationMaxContextRunes = 32000
	configured.ConversationTimeoutMillis = 60000
	configured.ConversationCitationRepairEnabled = true
	configured.ConversationCitationRepairPromptVersion = "citation-repair-v1"
	configured.ConversationCitationRepairPromptFile = "config/prompts/conversation-citation-repair.md"
	configured.ConversationCitationRepairTimeoutMillis = 30000
	configured.ConversationCitationRepairMaxOutputTokens = 768
	configured.ContextMemory = ContextMemoryConfig{
		ShadowPreflightEnabled:  true,
		PreflightTimeoutMillis:  250,
		SoftThresholdRatio:      0.70,
		HardThresholdRatio:      0.85,
		ToolGrowthReserveTokens: 8192,
	}
	if err := configured.Validate(); err != nil {
		t.Fatalf("Validate configured budgets: %v", err)
	}
	invalidRepair := configured
	invalidRepair.ConversationCitationRepairMaxOutputTokens = 64
	if err := invalidRepair.Validate(); err == nil {
		t.Fatal("Validate accepted too-small citation repair output budget")
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
	invalidThresholds := configured
	invalidThresholds.ContextMemory.HardThresholdRatio = 0.60
	if err := invalidThresholds.Validate(); err == nil {
		t.Fatal("Validate accepted hard threshold below soft threshold")
	}
	missingGrowthReserve := configured
	missingGrowthReserve.ContextMemory.ToolGrowthReserveTokens = 0
	if err := missingGrowthReserve.Validate(); err == nil {
		t.Fatal("Validate accepted enabled shadow preflight without Tool growth reserve")
	}
	invalidPreflightTimeout := configured
	invalidPreflightTimeout.ContextMemory.PreflightTimeoutMillis = 0
	if err := invalidPreflightTimeout.Validate(); err == nil {
		t.Fatal("Validate accepted enabled shadow preflight without a bounded timeout")
	}
}

func validAgentConfigForTest() AgentConfig {
	return AgentConfig{
		SkillsDirectory:           "config/skills",
		PromptVersion:             "diagnosis-v1",
		SystemPromptFile:          "config/prompts/diagnosis-system.md",
		BaselinePromptFile:        "config/prompts/evaluation-baseline.md",
		ReportContractFile:        "config/prompts/report-contract.md",
		ConversationPromptVersion: "conversation-v1",
		ConversationPromptFile:    "config/prompts/conversation-system.md",
	}
}
