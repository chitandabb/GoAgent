package agent

import "time"

func testSkills() []SkillDefinition {
	return []SkillDefinition{
		{
			ID: SkillTicketDiagnosis, Version: "test-v1", Description: "ticket diagnosis", SystemPrompt: "diagnose ticket",
			AllowedTools: []string{ToolReadExternalCase, ToolRequestCodeInvestigation},
			Budget: ContextBudget{
				MaxContextTokens: 32_000, ReservedOutputTokens: 4_000,
				MaxEvidenceTokens: 12_000, MaxToolResultTokens: 6_000, MaxToolResultBytes: 24 * 1024,
			},
			MaxSteps: 8, Timeout: 45 * time.Second,
		},
		{
			ID: SkillCodeInvestigation, Version: "test-v1", Description: "code investigation", SystemPrompt: "investigate code",
			AllowedTools: append([]string(nil), GitHubReadOnlyTools...),
			Budget: ContextBudget{
				MaxContextTokens: 48_000, ReservedOutputTokens: 4_000,
				MaxEvidenceTokens: 24_000, MaxToolResultTokens: 8_000, MaxToolResultBytes: 32 * 1024,
			},
			MaxSteps: 12, Timeout: 90 * time.Second,
		},
	}
}
