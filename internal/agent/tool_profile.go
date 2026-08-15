package agent

import "github.com/chitandabb/GoAgent/internal/agentruntime"

// ToolProfileConfig 是启动期从"实际成功构造并注册的 Adapter"推导一次的部署级
// 配置。它故意不包含 actor、消息引用、资源授权或依赖瞬时健康状态字段；
// 一个进程启动后 Profile 不再变化，临时 Tool 执行失败也不能删除 Schema。
//
// 本切片接线状态：SQL Tool 只进入 Diagnosis Profile；Conversation Profile
// 暂不含 SQL（下一切片完成 RunAccess/ResourceGrant 与 SQL Tool 内部资源检查后
// 再开放）。Web 两件套与 SQL 三件套按各自成功构造分别声明，部分构造不会
// 让 Profile 引用未注册的 Tool。
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

// BuildDefaultToolProfiles 从部署配置派生 Conversation 与 Diagnosis 两个固定
// Tool Profile。ToolSkill 是 Middleware-owned（由 Eino Skill Middleware 追加，
// 不在 ToolCatalog 注册表），因此始终声明在 Diagnosis Profile 的可见名单中，
// 但永远不会由 Catalog 生成一个假的 skill Tool。
func BuildDefaultToolProfiles(config ToolProfileConfig) (agentruntime.ToolProfiles, error) {
	conversationNames := make([]string, 0, 16)
	diagnosisNames := []string{ToolSkill}

	if config.SkillReferenceConfigured {
		diagnosisNames = append(diagnosisNames, ToolReadSkillReference)
	}
	if config.ExternalCaseConfigured {
		conversationNames = append(conversationNames, ToolReadExternalCase)
		diagnosisNames = append(diagnosisNames, ToolReadExternalCase)
	}
	if config.KnowledgeConfigured {
		conversationNames = append(conversationNames, ToolSearchKnowledge)
		diagnosisNames = append(diagnosisNames, ToolSearchKnowledge)
	}
	if config.WebSearchConfigured {
		conversationNames = append(conversationNames, ToolWebSearch)
		diagnosisNames = append(diagnosisNames, ToolWebSearch)
	}
	if config.FetchPublicPageConfigured {
		conversationNames = append(conversationNames, ToolFetchPublicPage)
		diagnosisNames = append(diagnosisNames, ToolFetchPublicPage)
	}
	if config.AttachmentConfigured {
		conversationNames = append(conversationNames, ToolReadAttachment)
		diagnosisNames = append(diagnosisNames, ToolReadAttachment)
	}
	// SQL 三件套按"实际成功构造"逐个加入 Diagnosis Profile；本切片不加入
	// Conversation Profile。同一组 Tool 只成功构造一部分时，Profile 只声明
	// 实际注册的名字，避免引用不存在的 Catalog Tool。
	if config.SQLObjectDefinitionsConfigured {
		diagnosisNames = append(diagnosisNames, ToolDatabaseObjectDefinition)
	}
	if config.SchemaCatalogConfigured {
		diagnosisNames = append(diagnosisNames, ToolSearchSchemaCatalog)
	}
	if config.ReadonlyQueryConfigured {
		diagnosisNames = append(diagnosisNames, ToolExecuteReadonlyQuery)
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
	return agentruntime.NewToolProfiles(conversation, diagnosis)
}
