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
		ToolReadConversationToolResult, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
	} {
		if !conversation.Has(name) {
			t.Fatalf("conversation Tool Profile is missing %q: %v", name, conversation.ToolNames())
		}
	}
	// 本切片接线状态：Conversation Profile 含成功构造的 Schema Catalog 与
	// 只读查询 Tool，但按最小 Tool 集原则不含对象定义、Skill 与代码调查工具。
	for _, name := range []string{
		ToolDatabaseObjectDefinition, ToolSkill, ToolReadSkillReference,
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
	// Schema Catalog 与只读查询成功构造后进入 Conversation Profile，
	// 但对象定义按最小 Tool 集原则仍仅供 Diagnosis。
	fullConversation, _ := full.Profile(agentruntime.ToolProfileConversation)
	for _, name := range []string{ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery} {
		if !fullConversation.Has(name) {
			t.Fatalf("conversation profile missing constructed SQL tool %q: %v", name, fullConversation.ToolNames())
		}
	}
	if fullConversation.Has(ToolDatabaseObjectDefinition) {
		t.Fatalf("conversation profile must not receive object definition tool: %v", fullConversation.ToolNames())
	}
	// 只有对象定义成功构造时，Conversation Profile 仍不含任何 SQL Tool。
	partialConversation, _ := partial.Profile(agentruntime.ToolProfileConversation)
	if partialConversation.Has(ToolSearchSchemaCatalog) || partialConversation.Has(ToolExecuteReadonlyQuery) {
		t.Fatalf("partial SQL profile leaked tools into conversation: %v", partialConversation.ToolNames())
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

// fullyConfiguredToolProfileConfig 返回全部业务 Tool 都成功构造的部署配置，
// 等价于真实 Pilot 中 evaluation-wide-v1=7 个业务 Tool、diagnosis-default=9
// 个模型可见 Tool 的对照场景。
func fullyConfiguredToolProfileConfig() ToolProfileConfig {
	return ToolProfileConfig{
		ExternalCaseConfigured:           true,
		SkillReferenceConfigured:         true,
		KnowledgeConfigured:              true,
		WebSearchConfigured:              true,
		FetchPublicPageConfigured:        true,
		AttachmentConfigured:             true,
		SQLObjectDefinitionsConfigured:   true,
		SchemaCatalogConfigured:          true,
		ReadonlyQueryConfigured:          true,
		GitHubToolNames:                  append([]string(nil), GitHubReadOnlyTools...),
		DiagnosisCommandConfigured:       true,
		DiagnosisStatusConfigured:        true,
		ConversationMemoryConfigured:     true,
		ConversationToolResultConfigured: true,
	}
}

// TestEvaluationWideV2IsStableUnionOfProductionProfiles 证明
// evaluation-wide-v2 是 conversation-default 与 diagnosis-default 的稳定并
// 集：去重、顺序无关、包含 diagnosis-only 与 conversation-only Tool，并且
// 两个生产 Profile 名单逐项保持既有语义（本切片不得改变生产 Tool 名单）。
func TestEvaluationWideV2IsStableUnionOfProductionProfiles(t *testing.T) {
	profiles, err := BuildDefaultToolProfiles(fullyConfiguredToolProfileConfig())
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles: %v", err)
	}
	conversation, ok := profiles.Profile(agentruntime.ToolProfileConversation)
	if !ok {
		t.Fatal("conversation-default profile is missing")
	}
	diagnosis, ok := profiles.Profile(agentruntime.ToolProfileDiagnosis)
	if !ok {
		t.Fatal("diagnosis-default profile is missing")
	}
	wide, ok := profiles.Profile(agentruntime.ToolProfileEvaluationWide)
	if !ok {
		t.Fatal("evaluation-wide-v2 profile is missing")
	}

	// 1. 生产 Profile 名单逐项保持既有语义（conversation-only / diagnosis-only
	//    分界不变，SQL 三件套接线不变）。
	conversationWant := sortedUnique([]string{
		ToolReadExternalCase, ToolSearchKnowledge, ToolWebSearch, ToolFetchPublicPage,
		ToolReadAttachment, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
		ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
		ToolReadConversationMemorySources, ToolReadConversationToolResult,
	})
	diagnosisWant := sortedUnique(append([]string{
		ToolSkill, ToolReadSkillReference, ToolReadExternalCase, ToolSearchKnowledge,
		ToolWebSearch, ToolFetchPublicPage, ToolReadAttachment,
		ToolDatabaseObjectDefinition, ToolSearchSchemaCatalog, ToolExecuteReadonlyQuery,
	}, GitHubReadOnlyTools...))
	if !slices.Equal(conversation.ToolNames(), conversationWant) {
		t.Fatalf("conversation-default must stay unchanged, got %v want %v", conversation.ToolNames(), conversationWant)
	}
	if !slices.Equal(diagnosis.ToolNames(), diagnosisWant) {
		t.Fatalf("diagnosis-default must stay unchanged, got %v want %v", diagnosis.ToolNames(), diagnosisWant)
	}

	// 2. wide 是两臂的并集（去重、排序稳定）。
	wantWide := append(append([]string(nil), conversation.ToolNames()...), diagnosis.ToolNames()...)
	wantWide = sortedUnique(wantWide)
	if !slices.Equal(wide.ToolNames(), wantWide) {
		t.Fatalf("evaluation-wide-v2 = %v, want union %v", wide.ToolNames(), wantWide)
	}

	// 3. wide 覆盖 diagnosis-default 的全部 Tool 与 conversation-only Tool。
	for _, name := range diagnosis.ToolNames() {
		if !wide.Has(name) {
			t.Fatalf("evaluation-wide-v2 is missing diagnosis tool %q: %v", name, wide.ToolNames())
		}
	}
	for _, name := range []string{
		ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
		ToolReadConversationMemorySources, ToolReadConversationToolResult,
	} {
		if !wide.Has(name) {
			t.Fatalf("evaluation-wide-v2 is missing conversation-only tool %q: %v", name, wide.ToolNames())
		}
	}

	// 4. 输入顺序变化不影响最终名单（union 与排序在 Profile 构造内部完成）。
	shuffled := fullyConfiguredToolProfileConfig()
	reversed := append([]string(nil), GitHubReadOnlyTools...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	shuffled.GitHubToolNames = reversed
	shuffledProfiles, err := BuildDefaultToolProfiles(shuffled)
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(shuffled): %v", err)
	}
	shuffledWide, _ := shuffledProfiles.Profile(agentruntime.ToolProfileEvaluationWide)
	if !slices.Equal(shuffledWide.ToolNames(), wide.ToolNames()) {
		t.Fatalf("input order changed the wide profile: %v vs %v", shuffledWide.ToolNames(), wide.ToolNames())
	}
	shuffledDiagnosis, _ := shuffledProfiles.Profile(agentruntime.ToolProfileDiagnosis)
	if !slices.Equal(shuffledDiagnosis.ToolNames(), diagnosis.ToolNames()) {
		t.Fatalf("input order changed the diagnosis profile: %v vs %v", shuffledDiagnosis.ToolNames(), diagnosis.ToolNames())
	}
}

// TestEvaluationWideV2FollowsPartialConstruction 证明 wide 并集只引用已配置
// 成功构造的 Tool：部分构造配置下 wide 名单等于两臂并集，不引入假 Tool。
func TestEvaluationWideV2FollowsPartialConstruction(t *testing.T) {
	profiles, err := BuildDefaultToolProfiles(ToolProfileConfig{
		ExternalCaseConfigured:   true,
		SkillReferenceConfigured: true,
	})
	if err != nil {
		t.Fatalf("BuildDefaultToolProfiles(partial): %v", err)
	}
	conversation, _ := profiles.Profile(agentruntime.ToolProfileConversation)
	diagnosis, _ := profiles.Profile(agentruntime.ToolProfileDiagnosis)
	wide, ok := profiles.Profile(agentruntime.ToolProfileEvaluationWide)
	if !ok {
		t.Fatal("evaluation-wide-v2 profile is missing under partial construction")
	}
	want := sortedUnique(append(append([]string(nil), conversation.ToolNames()...), diagnosis.ToolNames()...))
	if !slices.Equal(wide.ToolNames(), want) {
		t.Fatalf("partial wide profile = %v, want %v", wide.ToolNames(), want)
	}
	if !wide.Has(ToolSkill) || !wide.Has(ToolReadSkillReference) {
		t.Fatalf("partial wide profile must include middleware-owned skill and read_skill_reference: %v", wide.ToolNames())
	}
	if wide.Has(ToolSearchSchemaCatalog) || wide.Has(ToolDatabaseObjectDefinition) {
		t.Fatalf("partial wide profile over-declared tools: %v", wide.ToolNames())
	}
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return slices.Compact(result)
}
