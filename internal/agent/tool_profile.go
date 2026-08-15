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
//   - evaluation-wide-v2：评测 wide 臂专用宽名单，是上面两个生产 Profile
//     在同一部署配置下的稳定并集（union(conversation, diagnosis)），不再
//     维护第三套手写名单。wide 是生产两臂的 Schema 超集，因此 baseline
//     的 Prompt Token 对照变量有效。
//
// ToolSkill 是 Middleware-owned（由 Eino Skill Middleware 追加，不在
// ToolCatalog 注册表），因此始终声明在 Diagnosis Profile（以及 wide 并集）
// 的可见名单中，但永远不会由 Catalog 生成一个假的 skill Tool。Profile 只
// 反映"启动时成功构造的 Adapter"，不随引用、权限、依赖瞬时健康或调用次数
// 变化。
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
	// SQL 三件套按"实际成功构造"逐个声明：search_schema_catalog 与
	// execute_readonly_query 同时进入 Conversation 与 Diagnosis Profile；
	// get_database_object_definition 按最小 Tool 集原则仅供 Diagnosis。
	// 同一组 Tool 只成功构造一部分时，Profile 只声明实际注册的名字，避免
	// 引用不存在的 Catalog Tool。
	if config.SQLObjectDefinitionsConfigured {
		diagnosisNames = append(diagnosisNames, ToolDatabaseObjectDefinition)
	}
	if config.SchemaCatalogConfigured {
		conversationNames = append(conversationNames, ToolSearchSchemaCatalog)
		diagnosisNames = append(diagnosisNames, ToolSearchSchemaCatalog)
	}
	if config.ReadonlyQueryConfigured {
		conversationNames = append(conversationNames, ToolExecuteReadonlyQuery)
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
	// wide 是两臂的并集：先构造 conversation 与 diagnosis 名单，再从二者
	// 生成 wideNames，避免继续维护第三套手写名单。unionToolNames 去重，
	// NewToolProfile 内部排序，因此输入顺序不影响最终名单与指纹。
	wide, err := agentruntime.NewToolProfile(agentruntime.ToolProfileEvaluationWide,
		unionToolNames(conversationNames, diagnosisNames))
	if err != nil {
		return agentruntime.ToolProfiles{}, err
	}
	return agentruntime.NewToolProfiles(conversation, diagnosis, wide)
}

// unionToolNames 返回多个名单的并集：按输入和首次出现顺序去重。最终
// ToolProfile 的规范排序由 NewToolProfile 完成，因此最终 Profile 不依赖
// 此处的输入顺序。
func unionToolNames(parts ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 32)
	for _, part := range parts {
		for _, name := range part {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	return result
}
