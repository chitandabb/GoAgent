package agent

import (
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
)

func TestDefaultToolProfilesShareReadToolsButKeepCommandsRuntimeSpecific(t *testing.T) {
	profiles, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:           true,
		KnowledgeConfigured:              true,
		WebSearchConfigured:              true,
		FetchPublicPageConfigured:        true,
		AttachmentConfigured:             true,
		SQLObjectDefinitionsConfigured:   true,
		SchemaCatalogConfigured:          true,
		ReadonlyQueryConfigured:          true,
		GitHubToolNames:                  GitHubReadOnlyTools,
		DiagnosisCommandConfigured:       true,
		DiagnosisStatusConfigured:        true,
		ConversationMemoryConfigured:     true,
		ConversationToolResultConfigured: true,
		SkillReferenceConfigured:         true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles: %v", err)
	}

	conversation, ok := profiles.Profile(agentruntime.ToolProfileConversation)
	if !ok {
		t.Fatal("conversation Tool Profile is missing")
	}
	for _, name := range []string{
		ToolSearchKnowledge, ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
		ToolReadConversationToolResult,
	} {
		if !conversation.Has(name) {
			t.Fatalf("conversation Tool Profile is missing %q: %v", name, conversation.ToolNames())
		}
	}
	// 本切片接线状态：Conversation Profile 不含 SQL、Skill 与代码调查工具。
	for _, name := range []string{
		ToolSearchSchemaCatalog, ToolDatabaseObjectDefinition, ToolExecuteReadonlyQuery,
		ToolSkill, ToolReadSkillReference,
	} {
		if conversation.Has(name) {
			t.Fatalf("conversation Tool Profile leaked diagnosis-only tool %q: %v", name, conversation.ToolNames())
		}
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

func TestDefaultToolProfilesSQLToolsFollowPartialConstruction(t *testing.T) {
	// 只有对象定义成功构造：Profile 只包含成功的那个 SQL Tool。
	partial, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:         true,
		SQLObjectDefinitionsConfigured: true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(partial): %v", err)
	}
	diagnosis, _ := partial.Profile(agentruntime.ToolProfileDiagnosis)
	if !diagnosis.Has(ToolDatabaseObjectDefinition) {
		t.Fatalf("partial SQL profile missing object definition: %v", diagnosis.ToolNames())
	}
	if diagnosis.Has(ToolSearchSchemaCatalog) || diagnosis.Has(ToolExecuteReadonlyQuery) {
		t.Fatalf("partial SQL profile over-declared tools: %v", diagnosis.ToolNames())
	}

	full, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:         true,
		SQLObjectDefinitionsConfigured: true,
		SchemaCatalogConfigured:        true,
		ReadonlyQueryConfigured:        true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(full): %v", err)
	}
	fullDiagnosis, _ := full.Profile(agentruntime.ToolProfileDiagnosis)
	for _, name := range []string{
		ToolDatabaseObjectDefinition, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
	} {
		if !fullDiagnosis.Has(name) {
			t.Fatalf("full SQL profile missing %q: %v", name, fullDiagnosis.ToolNames())
		}
	}
	// Conversation 无论 SQL 配置如何都不含 SQL（本切片接线状态）。
	fullConversation, _ := full.Profile(agentruntime.ToolProfileConversation)
	if fullConversation.Has(ToolExecuteReadonlyQuery) {
		t.Fatalf("conversation profile received SQL tools: %v", fullConversation.ToolNames())
	}
}

func TestDefaultToolProfilesWebToolsFollowPartialConstruction(t *testing.T) {
	// 只有 WebSearch 成功构造：Profile 只包含 web_search。
	searchOnly, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured: true,
		WebSearchConfigured:    true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(search only): %v", err)
	}
	searchOnlyDiagnosis, _ := searchOnly.Profile(agentruntime.ToolProfileDiagnosis)
	searchOnlyConversation, _ := searchOnly.Profile(agentruntime.ToolProfileConversation)
	if !searchOnlyDiagnosis.Has(ToolWebSearch) || searchOnlyDiagnosis.Has(ToolFetchPublicPage) {
		t.Fatalf("search-only profile = %v", searchOnlyDiagnosis.ToolNames())
	}
	if !searchOnlyConversation.Has(ToolWebSearch) || searchOnlyConversation.Has(ToolFetchPublicPage) {
		t.Fatalf("search-only conversation profile = %v", searchOnlyConversation.ToolNames())
	}

	// 只有 FetchPublicPage 成功构造：Profile 只包含 fetch_public_page。
	fetchOnly, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:    true,
		FetchPublicPageConfigured: true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(fetch only): %v", err)
	}
	fetchOnlyDiagnosis, _ := fetchOnly.Profile(agentruntime.ToolProfileDiagnosis)
	fetchOnlyConversation, _ := fetchOnly.Profile(agentruntime.ToolProfileConversation)
	if fetchOnlyDiagnosis.Has(ToolWebSearch) || !fetchOnlyDiagnosis.Has(ToolFetchPublicPage) {
		t.Fatalf("fetch-only profile = %v", fetchOnlyDiagnosis.ToolNames())
	}
	if fetchOnlyConversation.Has(ToolWebSearch) || !fetchOnlyConversation.Has(ToolFetchPublicPage) {
		t.Fatalf("fetch-only conversation profile = %v", fetchOnlyConversation.ToolNames())
	}

	// 二者都有时都存在。
	both, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:    true,
		WebSearchConfigured:       true,
		FetchPublicPageConfigured: true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(both): %v", err)
	}
	bothDiagnosis, _ := both.Profile(agentruntime.ToolProfileDiagnosis)
	if !bothDiagnosis.Has(ToolWebSearch) || !bothDiagnosis.Has(ToolFetchPublicPage) {
		t.Fatalf("both profile = %v", bothDiagnosis.ToolNames())
	}

	// 二者都无时都不存在。
	none, err := BuildDefaultToolProfiles(ToolProfileConfig{ExternalCaseConfigured: true})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(none): %v", err)
	}
	noneDiagnosis, _ := none.Profile(agentruntime.ToolProfileDiagnosis)
	noneConversation, _ := none.Profile(agentruntime.ToolProfileConversation)
	if noneDiagnosis.Has(ToolWebSearch) || noneDiagnosis.Has(ToolFetchPublicPage) ||
		noneConversation.Has(ToolWebSearch) || noneConversation.Has(ToolFetchPublicPage) {
		t.Fatalf("none profile = %v / %v", noneDiagnosis.ToolNames(), noneConversation.ToolNames())
	}
}

func TestDefaultToolProfileOnlyChangesForDeploymentConfiguration(t *testing.T) {
	configured := ToolProfileConfig{
		ExternalCaseConfigured:         true,
		KnowledgeConfigured:            true,
		SQLObjectDefinitionsConfigured: true,
		SchemaCatalogConfigured:        true,
		ReadonlyQueryConfigured:        true,
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

	withoutSQL := configured
	withoutSQL.SQLObjectDefinitionsConfigured = false
	withoutSQL.SchemaCatalogConfigured = false
	withoutSQL.ReadonlyQueryConfigured = false
	withoutSQLDiagnosis, err := BuildDefaultToolProfiles(withoutSQL)
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(without SQL): %v", err)
	}
	withoutSQLDiagnosisProfile, _ := withoutSQLDiagnosis.Profile(agentruntime.ToolProfileDiagnosis)
	firstDiagnosis, _ := first.Profile(agentruntime.ToolProfileDiagnosis)
	if withoutSQLDiagnosisProfile.Has(ToolExecuteReadonlyQuery) ||
		slices.Equal(firstDiagnosis.ToolNames(), withoutSQLDiagnosisProfile.ToolNames()) {
		t.Fatalf("deployment configuration did not change the diagnosis profile: %v",
			withoutSQLDiagnosisProfile.ToolNames())
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
