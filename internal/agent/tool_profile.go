package agent

import "github.com/chitandabb/GoAgent/internal/agentruntime"

// ToolProfileConfig is derived once from deployment configuration. A configured
// adapter keeps its Schema in the Profile even while the remote dependency is
// temporarily unavailable. The config deliberately contains no actor,
// message-reference, resource-grant, or transient dependency-health fields.
type ToolProfileConfig struct {
	ExternalCaseConfigured       bool
	KnowledgeConfigured          bool
	WebSearchConfigured          bool
	AttachmentConfigured         bool
	SQLConfigured                bool
	GitHubToolNames              []string
	DiagnosisCommandConfigured   bool
	DiagnosisStatusConfigured    bool
	ConversationMemoryConfigured bool
}

func BuildDefaultToolProfiles(config ToolProfileConfig) (agentruntime.ToolProfiles, error) {
	conversationNames := make([]string, 0, 16)
	diagnosisNames := []string{ToolSkill, ToolReadSkillReference}

	if config.ExternalCaseConfigured {
		conversationNames = append(conversationNames, ToolReadExternalCase)
		diagnosisNames = append(diagnosisNames, ToolReadExternalCase)
	}
	if config.KnowledgeConfigured {
		conversationNames = append(conversationNames, ToolSearchKnowledge)
		diagnosisNames = append(diagnosisNames, ToolSearchKnowledge)
	}
	if config.WebSearchConfigured {
		conversationNames = append(conversationNames, ToolWebSearch, ToolFetchPublicPage)
		diagnosisNames = append(diagnosisNames, ToolWebSearch, ToolFetchPublicPage)
	}
	if config.AttachmentConfigured {
		conversationNames = append(conversationNames, ToolReadAttachment)
		diagnosisNames = append(diagnosisNames, ToolReadAttachment)
	}
	if config.SQLConfigured {
		sqlTools := []string{ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery}
		conversationNames = append(conversationNames, sqlTools...)
		diagnosisNames = append(diagnosisNames, sqlTools...)
	}
	if len(config.GitHubToolNames) != 0 {
		diagnosisNames = append(diagnosisNames, config.GitHubToolNames...)
	}
	if config.DiagnosisCommandConfigured {
		conversationNames = append(conversationNames, ToolCreateDiagnosisTask)
	}
	if config.DiagnosisStatusConfigured {
		conversationNames = append(conversationNames, ToolGetDiagnosisTaskStatus)
	}
	if config.ConversationMemoryConfigured {
		conversationNames = append(conversationNames,
			ToolReadConversationMemorySources,
			ToolReadConversationToolResult,
		)
	}

	conversation, err := agentruntime.NewToolProfile(agentruntime.ToolProfileConversation, conversationNames)
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	diagnosis, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis, diagnosisNames)
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	return agentruntime.NewToolProfiles(conversation, diagnosis)
}
