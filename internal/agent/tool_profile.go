package agent

import "github.com/chitandabb/GoAgent/internal/agentruntime"

// ToolProfileConfig 是启动期从"实际成功构造并注册的 Adapter"推导一次的部署级
// 配置。它故意不包含 actor、消息引用、资源授权或依赖瞬时健康状态字段；
// 一个进程启动后 Profile 不再变化，临时 Tool 执行失败也不能删除 Schema。
//
// 本切片接线状态：search_schema_catalog 与 execute_readonly_query 在成功构造
// 后同时进入 Conversation 与 Diagnosis 两个固定 Profile（Conversation 经
// RunAccess sql.read + 只读数据源 Grant 执行期授权）；get_database_object_definition
// 按最小 Tool 集原则继续仅供 Diagnosis（Text-to-SQL 不需要对象 DDL）。Web
// 两件套与 SQL 三件套按各自成功构造分别声明，部分构造不会让 Profile 引用
// 未注册的 Tool。
type ToolProfileConfig struct {
	ExternalCaseConfigured           bool
	SkillReferenceConfigured         bool
	KnowledgeConfigured              bool
	WebSearchConfigured              bool
	FetchPublicPageConfigured        bool
	AttachmentConfigured             bool
	SQLObjectDefinitionsConfigured   bool
	SchemaCatalogConfigured          bool
	ReadonlyQueryConfigured          bool
	GitHubToolNames                  []string
	DiagnosisCommandConfigured       bool
	DiagnosisStatusConfigured        bool
	ConversationMemoryConfigured     bool
	ConversationToolResultConfigured bool
}

// BuildDefaultToolProfiles 从部署配置派生固定 Tool Profile：
//   - conversation-default：Conversation Runtime 的 Schema 名单；
//   - diagnosis-default：Diagnosis Runtime 的 Schema 名单；
//   - evaluation-wide-v1：评测 wide 臂专用宽名单（全部实际注册的业务 Tool，
//     不含 skill/read_skill_reference）。
//
// ToolSkill 是 Middleware-owned（由 Eino Skill Middleware 追加，不在
// ToolCatalog 注册表），因此始终声明在 Diagnosis Profile 的可见名单中，
// 但永远不会由 Catalog 生成一个假的 skill Tool。Profile 只反映"启动时成功
// 构造的 Adapter"，不随引用、权限、依赖瞬时健康或调用次数变化。
func BuildDefaultToolProfiles(config ToolProfileConfig) (agentruntime.ToolProfiles, error) {
	conversationNames := make([]string, 0, 16)
	diagnosisNames := []string{ToolSkill}
	wideNames := make([]string, 0, 16)

	if config.SkillReferenceConfigured {
		diagnosisNames = append(diagnosisNames, ToolReadSkillReference)
	}
	if config.ExternalCaseConfigured {
		conversationNames = append(conversationNames, ToolReadExternalCase)
		diagnosisNames = append(diagnosisNames, ToolReadExternalCase)
		wideNames = append(wideNames, ToolReadExternalCase)
	}
	if config.KnowledgeConfigured {
		conversationNames = append(conversationNames, ToolSearchKnowledge)
		diagnosisNames = append(diagnosisNames, ToolSearchKnowledge)
		wideNames = append(wideNames, ToolSearchKnowledge)
	}
	if config.WebSearchConfigured {
		conversationNames = append(conversationNames, ToolWebSearch)
		diagnosisNames = append(diagnosisNames, ToolWebSearch)
		wideNames = append(wideNames, ToolWebSearch)
	}
	if config.FetchPublicPageConfigured {
		conversationNames = append(conversationNames, ToolFetchPublicPage)
		diagnosisNames = append(diagnosisNames, ToolFetchPublicPage)
		wideNames = append(wideNames, ToolFetchPublicPage)
	}
	if config.AttachmentConfigured {
		conversationNames = append(conversationNames, ToolReadAttachment)
		diagnosisNames = append(diagnosisNames, ToolReadAttachment)
		wideNames = append(wideNames, ToolReadAttachment)
	}
	// SQL 三件套按"实际成功构造"逐个声明：search_schema_catalog 与
	// execute_readonly_query 同时进入 Conversation 与 Diagnosis Profile；
	// get_database_object_definition 按最小 Tool 集原则仅供 Diagnosis。
	// 同一组 Tool 只成功构造一部分时，Profile 只声明实际注册的名字，避免
	// 引用不存在的 Catalog Tool。
	if config.SQLObjectDefinitionsConfigured {
		diagnosisNames = append(diagnosisNames, ToolDatabaseObjectDefinition)
		wideNames = append(wideNames, ToolDatabaseObjectDefinition)
	}
	if config.SchemaCatalogConfigured {
		conversationNames = append(conversationNames, ToolSearchSchemaCatalog)
		diagnosisNames = append(diagnosisNames, ToolSearchSchemaCatalog)
		wideNames = append(wideNames, ToolSearchSchemaCatalog)
	}
	if config.ReadonlyQueryConfigured {
		conversationNames = append(conversationNames, ToolExecuteReadonlyQuery)
		diagnosisNames = append(diagnosisNames, ToolExecuteReadonlyQuery)
		wideNames = append(wideNames, ToolExecuteReadonlyQuery)
	}
	if len(config.GitHubToolNames) != 0 {
		diagnosisNames = append(diagnosisNames, config.GitHubToolNames...)
		wideNames = append(wideNames, config.GitHubToolNames...)
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
		)
	}
	if config.ConversationToolResultConfigured {
		conversationNames = append(conversationNames, ToolReadConversationToolResult)
	}

	conversation, err := agentruntime.NewToolProfile(agentruntime.ToolProfileConversation, conversationNames)
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	diagnosis, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis, diagnosisNames)
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	wide, err := agentruntime.NewToolProfile(agentruntime.ToolProfileEvaluationWide, wideNames)
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	return agentruntime.NewToolProfiles(conversation, diagnosis, wide)
}
