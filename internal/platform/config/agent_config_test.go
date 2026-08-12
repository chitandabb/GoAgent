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
		ShadowPreflightEnabled:    true,
		DiagnosisPreflightEnabled: true,
		ContinuousTailEnabled:     true,
		TailMaxRatio:              0.15,
		PreflightTimeoutMillis:    250,
		SoftThresholdRatio:        0.70,
		HardThresholdRatio:        0.85,
		ToolGrowthReserveTokens:   8192,
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
	continuousWithoutPreflight := configured
	continuousWithoutPreflight.ContextMemory.ShadowPreflightEnabled = false
	if err := continuousWithoutPreflight.Validate(); err == nil {
		t.Fatal("Validate accepted continuous Tail without shadow preflight")
	}
	diagnosisOnly := configured
	diagnosisOnly.ContextMemory.ShadowPreflightEnabled = false
	diagnosisOnly.ContextMemory.ContinuousTailEnabled = false
	diagnosisOnly.ContextMemory.DiagnosisPreflightEnabled = true
	if err := diagnosisOnly.Validate(); err != nil {
		t.Fatalf("Validate diagnosis-only preflight: %v", err)
	}
	invalidTailRatio := configured
	invalidTailRatio.ContextMemory.TailMaxRatio = 0.21
	if err := invalidTailRatio.Validate(); err == nil {
		t.Fatal("Validate accepted Tail ratio above the 20 percent memory budget")
	}
	configured.ContextMemory.Summary = ConversationMemorySummaryConfig{
		Enabled: true, PromptFile: "config/prompts/conversation-memory-summary.md",
		PromptVersion: "conversation-memory-v1", MaxPayloadBytes: 65536,
		MaxAttempts: 3, RetryBaseDelayMillis: 250,
	}
	configured.ContextMemory.SummaryTailEnabled = true
	configured.ContextMemory.SyncCompactionTimeoutMillis = 45_000
	configured.ContextMemory.AsyncCompactionEnabled = true
	configured.ContextMemory.AsyncMaxAttempts = 3
	configured.ContextMemory.RetryJitterRatio = 0.10
	configured.ContextMemory.MemoryMaxRatio = 0.20
	configured.ContextMemory.SummaryMaxRatio = 0.05
	configured.ContextMemory.MemoryCacheEnabled = true
	configured.ContextMemory.MemoryCacheTTL = "2h"
	configured.ContextMemory.MemoryCacheJitterRatio = 0.10
	configured.ContextMemory.MemoryCacheTimeoutMillis = 50
	configured.ContextMemory.SourceRecoveryEnabled = true
	configured.ContextMemory.SourceRecoveryMaxMessages = 20
	configured.ContextMemory.SourceRecoveryMaxTokens = 8192
	configured.ContextMemory.SourceRecoveryMaxCalls = 2
	if err := configured.Validate(); err != nil {
		t.Fatalf("Validate configured Summary + Tail: %v", err)
	}
	invalidSyncBudget := configured
	invalidSyncBudget.ContextMemory.SyncCompactionTimeoutMillis = configured.ConversationTimeoutMillis
	if err := invalidSyncBudget.Validate(); err == nil {
		t.Fatal("Validate accepted synchronous compaction without answer time reserve")
	}
	if ttl, err := configured.ContextMemory.MemoryCacheDuration(); err != nil || ttl.String() != "2h0m0s" {
		t.Fatalf("MemoryCacheDuration() = %s, %v", ttl, err)
	}
	invalidMemoryRatio := configured
	invalidMemoryRatio.ContextMemory.MemoryMaxRatio = 0.19
	if err := invalidMemoryRatio.Validate(); err == nil {
		t.Fatal("Validate accepted Summary + Tail above the total memory ratio")
	}
	invalidSummaryRatio := configured
	invalidSummaryRatio.ContextMemory.SummaryMaxRatio = 0.06
	if err := invalidSummaryRatio.Validate(); err == nil {
		t.Fatal("Validate accepted Summary ratio above five percent")
	}
	summaryTailWithoutSummary := configured
	summaryTailWithoutSummary.ContextMemory.Summary.Enabled = false
	if err := summaryTailWithoutSummary.Validate(); err == nil {
		t.Fatal("Validate accepted Summary + Tail while the Summary model is disabled")
	}
	invalidSummary := configured
	invalidSummary.ContextMemory.Summary.PromptVersion = ""
	if err := invalidSummary.Validate(); err == nil {
		t.Fatal("Validate accepted enabled Summary without a prompt version")
	}
	invalidSummary = configured
	invalidSummary.ContextMemory.Summary.MaxAttempts = 6
	if err := invalidSummary.Validate(); err == nil {
		t.Fatal("Validate accepted Summary retry count above the bounded limit")
	}
	invalidAsyncAttempts := configured
	invalidAsyncAttempts.ContextMemory.AsyncMaxAttempts = 0
	if err := invalidAsyncAttempts.Validate(); err == nil {
		t.Fatal("Validate accepted async compaction without bounded attempts")
	}
	invalidJitter := configured
	invalidJitter.ContextMemory.RetryJitterRatio = 0.51
	if err := invalidJitter.Validate(); err == nil {
		t.Fatal("Validate accepted async compaction jitter above 50 percent")
	}
	invalidCacheTTL := configured
	invalidCacheTTL.ContextMemory.MemoryCacheTTL = "30s"
	if err := invalidCacheTTL.Validate(); err == nil {
		t.Fatal("Validate accepted memory cache TTL below one minute")
	}
	invalidCacheJitter := configured
	invalidCacheJitter.ContextMemory.MemoryCacheJitterRatio = 0.51
	if err := invalidCacheJitter.Validate(); err == nil {
		t.Fatal("Validate accepted memory cache jitter above 50 percent")
	}
	invalidCacheTimeout := configured
	invalidCacheTimeout.ContextMemory.MemoryCacheTimeoutMillis = 1_001
	if err := invalidCacheTimeout.Validate(); err == nil {
		t.Fatal("Validate accepted memory cache timeout above one second")
	}
	invalidSourceMessages := configured
	invalidSourceMessages.ContextMemory.SourceRecoveryMaxMessages = 21
	if err := invalidSourceMessages.Validate(); err == nil {
		t.Fatal("Validate accepted source recovery above the 20 message hard limit")
	}
	invalidSourceTokens := configured
	invalidSourceTokens.ContextMemory.SourceRecoveryMaxTokens = 8193
	if err := invalidSourceTokens.Validate(); err == nil {
		t.Fatal("Validate accepted source recovery above the 8192 token hard limit")
	}
	invalidSourceCalls := configured
	invalidSourceCalls.ContextMemory.SourceRecoveryMaxCalls = 3
	if err := invalidSourceCalls.Validate(); err == nil {
		t.Fatal("Validate accepted source recovery above the two-call hard limit")
	}
	insufficientSourceReserve := configured
	insufficientSourceReserve.ContextMemory.ToolGrowthReserveTokens = 8191
	if err := insufficientSourceReserve.Validate(); err == nil {
		t.Fatal("Validate accepted source recovery above the Tool growth reserve")
	}
	sourceWithoutSummaryTail := configured
	sourceWithoutSummaryTail.ContextMemory.SummaryTailEnabled = false
	if err := sourceWithoutSummaryTail.Validate(); err == nil {
		t.Fatal("Validate accepted source recovery without Summary + Tail")
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
