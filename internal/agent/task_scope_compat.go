package agent

import (
	"errors"
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/google/uuid"
)

// capabilityPermission 是旧 TaskScope 能力到 v2 Permission 的唯一映射表。
// 任何 Middleware、ToolCatalog 或 Tool 都不得自行重复该映射。
var capabilityPermission = map[ToolCapability]agentruntime.Permission{
	ToolCapabilityCase:       agentruntime.PermissionCaseRead,
	ToolCapabilityCode:       agentruntime.PermissionCodeRead,
	ToolCapabilitySQL:        agentruntime.PermissionSQLRead,
	ToolCapabilityKnowledge:  agentruntime.PermissionKnowledgeRead,
	ToolCapabilityAttachment: agentruntime.PermissionAttachmentRead,
	ToolCapabilityWebSearch:  agentruntime.PermissionWebRead,
	ToolCapabilityTask:       agentruntime.PermissionTaskRead,
	ToolCapabilityMemory:     agentruntime.PermissionMemoryRead,
}

// runAccessFromTaskScope 是旧 TaskScope 到 v2 RunAccess 的唯一兼容转换入口。
//
// 它只服务于旧任务和旧测试的兼容迁移；v2 新代码必须直接构造 RunAccess
// （Conversation 用 NewConversationRunAccess，Diagnosis 用 InvestigationPolicy
// 派生）。转换失败时调用方必须 fail-closed，绝不能放宽权限。
func runAccessFromTaskScope(scope TaskScope) (agentruntime.RunAccess, error) {
	if scope.UserID() == uuid.Nil || !scope.Role().Valid() || !scope.TaskType().Valid() {
		return agentruntime.RunAccess{}, errors.New("task scope is invalid")
	}
	actor := agentruntime.Actor{UserID: scope.UserID(), Role: scope.Role()}
	capabilities := scope.AllowedCapabilities()
	permissions := make([]agentruntime.Permission, 0, len(capabilities)+1)
	for _, capability := range capabilities {
		permission, ok := capabilityPermission[capability]
		if !ok {
			return agentruntime.RunAccess{}, fmt.Errorf("capability %q has no v2 permission mapping", capability)
		}
		permissions = append(permissions, permission)
	}
	// 只映射 TaskScope 已有的、且为只读模式的数据源 ID：SQL Tool 的
	// 执行授权只覆盖只读数据源，bounded_lab 等非只读源必须与旧的
	// resolveGrantedSQLDataSource 角色/安全模式过滤保持一致，不得进入 Grant。
	// 不凭空生成工单、附件、任务或仓库 Grant，GitHub 仓库级授权不在本
	// 兼容层实现。
	dataSourceIDs := make([]uuid.UUID, 0, len(scope.DataSources()))
	for _, dataSource := range scope.DataSources() {
		if dataSource.SafetyMode == DataSourceSafetyReadOnly {
			dataSourceIDs = append(dataSourceIDs, dataSource.ID)
		}
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		DataSourceIDs: dataSourceIDs,
	})
	if err != nil {
		return agentruntime.RunAccess{}, fmt.Errorf("build compat resource grants: %w", err)
	}
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		return agentruntime.RunAccess{}, fmt.Errorf("build compat permissions: %w", err)
	}

	switch scope.TaskType() {
	case TaskTypeDiagnosis:
		// 诊断任务必须经由冻结 InvestigationPolicy 派生 RunAccess，
		// 不绕过 Diagnosis 必须从 Policy 派生的领域约束。
		policy, err := agentruntime.NewInvestigationPolicy(1, permissionSet, grants)
		if err != nil {
			return agentruntime.RunAccess{}, fmt.Errorf("build compat investigation policy: %w", err)
		}
		ceiling := agentruntime.AccessCeiling{Permissions: permissionSet, Grants: grants}
		access, err := agentruntime.DeriveDiagnosisRunAccess(policy, actor, ceiling)
		if err != nil {
			return agentruntime.RunAccess{}, fmt.Errorf("derive compat diagnosis run access: %w", err)
		}
		return access, nil
	case TaskTypeKnowledge:
		// 旧知识任务在 v2 中归属 Conversation Runtime 兼容执行；
		// TaskTypeKnowledge 将在 v2 后续阶段退役。
		return agentruntime.NewConversationRunAccess(actor, permissionSet, grants)
	case TaskTypeConversation:
		// 兼容旧动态可见语义：仅当会话携带 case 能力时额外授予
		// diagnosis.create；该兼容逻辑后续由固定 Conversation ToolProfile
		// 与命令 Guard 取代。case 能力本身始终只映射 case.read。
		if slices.Contains(capabilities, ToolCapabilityCase) {
			permissionSet, err = agentruntime.NewPermissionSet(
				append(permissionSet.Values(), agentruntime.PermissionDiagnosisCreate)...,
			)
			if err != nil {
				return agentruntime.RunAccess{}, fmt.Errorf("build compat diagnosis create permission: %w", err)
			}
		}
		return agentruntime.NewConversationRunAccess(actor, permissionSet, grants)
	default:
		return agentruntime.RunAccess{}, fmt.Errorf("task type %q has no v2 runtime mapping", scope.TaskType())
	}
}
