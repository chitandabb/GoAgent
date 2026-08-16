package agent

import (
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

// conversationRunContext 是 Conversation 运行上下文的唯一生成点（深模块）。
//
// 它用一个纯函数式入口集中生成本轮的两样东西：
//   - 本轮 agentruntime.RunAccess：直接经 NewConversationRunAccess 构造，
//     权限与资源 Grant 完全由"固定 Conversation Profile 实际存在的 Tool"和
//     "当前 user message 的引用"推导；
//   - 追加到当前 user 原文尾部的安全 turn_context（确定性排序、只含安全投影）。
//
// 本模块不读取 Context、不访问任何 Service、不保存状态，也不新增全局
// Registry、意图分类器、Skill-Tool 绑定或数据库表。调用方（ConversationRunner）
// 把生成的 RunAccess 用 WithRunAccess 绑定为权威值；旧 TaskScope 兼容
// 双写已硬切删除。
type conversationRunContext struct {
	access        agentruntime.RunAccess
	promptContext conversationPromptContext
	turnContext   string
}

// Access 返回本轮 Conversation RunAccess 快照。
func (c conversationRunContext) Access() agentruntime.RunAccess {
	return c.access
}

// TurnContext 返回追加到当前 user 原文尾部的结构化上下文；为空表示无需追加。
func (c conversationRunContext) TurnContext() string {
	return c.turnContext
}

// PromptContext returns the deployment-stable model projection facts derived
// alongside the authoritative RunAccess.
func (c conversationRunContext) PromptContext() conversationPromptContext {
	return c.promptContext
}

// buildConversationRunContext 从一次 Conversation 消息推导本轮运行上下文。
//
// 权限规则（fail-closed，空 Grant 永远表示无权限）：每一项 Permission 都要求
// "对应 Tool 在固定 Conversation Profile 中"与"当前消息满足条件"同时成立：
//   - read_external_case 在 Profile 中且 case 引用存在 -> case.read
//   - create_diagnosis_task 在 Profile 中且恰好一个 selected case -> diagnosis.create
//   - get_diagnosis_task_status 在 Profile 中且 task 引用存在 -> task.read
//   - read_attachment 在 Profile 中且 attachment 存在 -> attachment.read
//   - 已装配 SQL Tool 且配置了只读数据源 -> sql.read；Grant 只包含配置的数据源 UUID
//   - knowledge/web/memory 权限按固定 Conversation Profile 实际存在的 Tool 派生
//   - 永不授予 code.read，也从不携带仓库 Grant
//
// ResourceGrant 还要支持 create_diagnosis_task 的资源参数：case/task/attachment
// 资源 ID 在对应 read Tool 或 create_diagnosis_task 任一存在时投影到 Grant，使
// create 命令能校验其 externalCaseId/attachmentId/parentTaskId 参数。
func buildConversationRunContext(
	actor conversation.Actor,
	message conversation.Message,
	modelVisibleNames []string,
	sqlDataSourceID uuid.UUID,
) (conversationRunContext, error) {
	if actor.UserID == uuid.Nil {
		return conversationRunContext{}, conversation.ErrCommandContextRequired
	}
	role := auth.RoleAnalyst
	if actor.IsAdmin {
		role = auth.RoleAdmin
	}
	profileTool := func(name string) bool { return slices.Contains(modelVisibleNames, name) }

	permissions := make([]agentruntime.Permission, 0, 8)
	grants := agentruntime.ResourceGrantsConfig{}

	if profileTool(ToolSearchKnowledge) {
		permissions = append(permissions, agentruntime.PermissionKnowledgeRead)
	}
	if profileTool(ToolWebSearch) || profileTool(ToolFetchPublicPage) {
		permissions = append(permissions, agentruntime.PermissionWebRead)
	}
	if profileTool(ToolReadConversationMemorySources) {
		permissions = append(permissions, agentruntime.PermissionMemoryRead)
	}

	createToolPresent := profileTool(ToolCreateDiagnosisTask)

	// case：read Tool 或 create Tool 任一存在即投影 Grant（create 需要
	// externalCaseId 参数），但 case.read 权限只随 read_external_case 授予。
	if (profileTool(ToolReadExternalCase) || createToolPresent) && len(message.CaseReferences) > 0 {
		for _, reference := range message.CaseReferences {
			grants.ExternalCaseIDs = append(grants.ExternalCaseIDs, reference.ExternalCaseID)
		}
	}
	if profileTool(ToolReadExternalCase) && len(message.CaseReferences) > 0 {
		permissions = append(permissions, agentruntime.PermissionCaseRead)
	}
	// 与 Conversation Service 的真实命令门禁保持一致：仅当 CaseReferences
	// 总数恰好为 1 且该引用为 selected 时授予 diagnosis.create。
	if createToolPresent && len(message.CaseReferences) == 1 &&
		message.CaseReferences[0].Kind == conversation.ReferenceKindSelected {
		permissions = append(permissions, agentruntime.PermissionDiagnosisCreate)
	}

	// task：read Tool 或 create Tool 任一存在即投影 Grant（create 需要
	// parentTaskId 参数），但 task.read 权限只随 get_diagnosis_task_status 授予。
	if (profileTool(ToolGetDiagnosisTaskStatus) || createToolPresent) && len(message.TaskReferences) > 0 {
		for _, reference := range message.TaskReferences {
			grants.TaskIDs = append(grants.TaskIDs, reference.TaskID)
		}
	}
	if profileTool(ToolGetDiagnosisTaskStatus) && len(message.TaskReferences) > 0 {
		permissions = append(permissions, agentruntime.PermissionTaskRead)
	}

	// attachment：read Tool 或 create Tool 任一存在即投影 Grant（create 需要
	// attachmentIds 参数），但 attachment.read 权限只随 read_attachment 授予。
	if (profileTool(ToolReadAttachment) || createToolPresent) && len(message.Attachments) > 0 {
		for _, reference := range message.Attachments {
			grants.AttachmentIDs = append(grants.AttachmentIDs, reference.AttachmentID)
		}
	}
	if profileTool(ToolReadAttachment) && len(message.Attachments) > 0 {
		permissions = append(permissions, agentruntime.PermissionAttachmentRead)
	}
	sqlAuthorized := false
	if sqlDataSourceID != uuid.Nil &&
		(profileTool(ToolSearchSchemaCatalog) || profileTool(ToolExecuteReadonlyQuery)) {
		permissions = append(permissions, agentruntime.PermissionSQLRead)
		grants.DataSourceIDs = []uuid.UUID{sqlDataSourceID}
		sqlAuthorized = true
	}

	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		return conversationRunContext{}, fmt.Errorf("build conversation permissions: %w", err)
	}
	resourceGrants, err := agentruntime.NewResourceGrants(grants)
	if err != nil {
		return conversationRunContext{}, fmt.Errorf("build conversation resource grants: %w", err)
	}
	access, err := agentruntime.NewConversationRunAccess(
		agentruntime.Actor{UserID: actor.UserID, Role: role},
		permissionSet,
		resourceGrants,
	)
	if err != nil {
		return conversationRunContext{}, fmt.Errorf("build conversation run access: %w", err)
	}
	promptContext := conversationPromptContext{
		sqlDataSourceID: sqlDataSourceID,
		sqlAuthorized:   sqlAuthorized,
	}
	return conversationRunContext{
		access:        access,
		promptContext: promptContext,
		turnContext:   buildConversationTurnContext(message, sqlDataSourceID, sqlAuthorized),
	}, nil
}

// buildConversationTurnContext 渲染追加到当前 user 原文尾部的安全结构化上下文，
// 委托给共享 JSON 投影渲染器（conversation_context_projection.go）。输出只含
// 安全白名单字段、按 ID 确定性排序；没有可写内容时返回空字符串。
func buildConversationTurnContext(
	message conversation.Message,
	sqlDataSourceID uuid.UUID,
	sqlAuthorized bool,
) string {
	return renderConversationTurnContext(message, sqlDataSourceID, sqlAuthorized)
}
