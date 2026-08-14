package agent

import (
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
)

func TestDefaultToolProfilesShareReadToolsButKeepCommandsRuntimeSpecific(t *testing.T) {
	profiles, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:       true,
		KnowledgeConfigured:          true,
		WebSearchConfigured:          true,
		AttachmentConfigured:         true,
		SQLConfigured:                true,
		GitHubToolNames:              GitHubReadOnlyTools,
		DiagnosisCommandConfigured:   true,
		DiagnosisStatusConfigured:    true,
		ConversationMemoryConfigured: true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles: %v", err)
	}

	conversation, ok := profiles.Profile(agentruntime.ToolProfileConversation)
	if !ok {
		t.Fatal("conversation Tool Profile is missing")
	}
	for _, name := range []string{
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
		ToolSearchKnowledge, ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
	} {
		if !conversation.Has(name) {
			t.Fatalf("conversation Tool Profile is missing %q: %v", name, conversation.ToolNames())
		}
	}
	if conversation.Has(ToolSkill) || conversation.Has("search_code") {
		t.Fatalf("conversation Tool Profile leaked diagnosis-only tools: %v", conversation.ToolNames())
	}

	diagnosis, ok := profiles.Profile(agentruntime.ToolProfileDiagnosis)
	if !ok {
		t.Fatal("diagnosis Tool Profile is missing")
	}
	for _, name := range []string{
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
		ToolSearchKnowledge, ToolSkill, ToolReadSkillReference, "search_code",
	} {
		if !diagnosis.Has(name) {
			t.Fatalf("diagnosis Tool Profile is missing %q: %v", name, diagnosis.ToolNames())
		}
	}
	if diagnosis.Has(ToolCreateDiagnosisTask) || diagnosis.Has(ToolGetDiagnosisTaskStatus) {
		t.Fatalf("diagnosis Tool Profile leaked conversation commands: %v", diagnosis.ToolNames())
	}
}

func TestDefaultToolProfileOnlyChangesForDeploymentConfiguration(t *testing.T) {
	configured := ToolProfileConfig{
		ExternalCaseConfigured: true,
		KnowledgeConfigured:    true,
		SQLConfigured:          true,
	}
	first, err := BuildDefaultToolProfiles(configured)
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(first): %v", err)
	}
	second, err := BuildDefaultToolProfiles(configured)
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(second): %v", err)
	}
	firstConversation, _ := first.Profile(agentruntime.ToolProfileConversation)
	secondConversation, _ := second.Profile(agentruntime.ToolProfileConversation)
	if !slices.Equal(firstConversation.ToolNames(), secondConversation.ToolNames()) {
		t.Fatalf("same deployment config produced different profiles: %v vs %v",
			firstConversation.ToolNames(), secondConversation.ToolNames())
	}

	configured.SQLConfigured = false
	withoutSQL, err := BuildDefaultToolProfiles(configured)
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(without SQL): %v", err)
	}
	withoutSQLConversation, _ := withoutSQL.Profile(agentruntime.ToolProfileConversation)
	if withoutSQLConversation.Has(ToolExecuteReadonlyQuery) ||
		slices.Equal(firstConversation.ToolNames(), withoutSQLConversation.ToolNames()) {
		t.Fatalf("deployment configuration did not change the profile: %v", withoutSQLConversation.ToolNames())
	}
}

func TestBuildDefaultToolProfilesRejectsInvalidGitHubToolNames(t *testing.T) {
	if _, err := BuildDefaultToolProfiles(ToolProfileConfig{GitHubToolNames: []string{"search_code", "search_code"}}); err == nil {
		t.Fatal("BuildDefaultToolProfiles accepted duplicate GitHub Tool names")
	}
	if _, err := BuildDefaultToolProfiles(ToolProfileConfig{GitHubToolNames: []string{"not a tool"}}); err == nil {
		t.Fatal("BuildDefaultToolProfiles accepted an invalid GitHub Tool name")
	}
}
