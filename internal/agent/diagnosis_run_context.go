package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/google/uuid"
)

// DiagnosisCeilingDataSource 是"当前仍 active 且任务绑定"的数据源上限事实，
// 不包含连接信息或凭证。Worker 仓储已过滤 active；本模块再收窄到
// read_only 且角色属于现有 SQL Tool 允许范围的数据源。
type DiagnosisCeilingDataSource struct {
	ID         uuid.UUID
	Role       DataSourceRole
	SafetyMode DataSourceSafetyMode
}

// DiagnosisRunContextInput 是单次诊断任务执行的纯输入。
type DiagnosisRunContextInput struct {
	// Policy 是任务创建时冻结的 InvestigationPolicy，也是任务的唯一授权
	// 事实。旧授权体系（legacy 派生/request_scope）已硬切删除：缺失、损坏
	// 或版本不一致的任务在执行前已被 Worker 仓储拒绝为 ErrInvalidTask。
	Policy agentruntime.InvestigationPolicy
	// Actor 是当前有效用户（Worker 仓储已校验 active + 合法角色）。
	Actor agentruntime.Actor
	// ProfileToolNames 是启动 Epoch 内固定 diagnosis-default Profile 的
	// 实际 Tool 名单快照；不按任务或消息动态变化。
	ProfileToolNames []string
	// ExternalCaseID 是当前工单。
	ExternalCaseID uuid.UUID
	// DataSources 是当前仍 active 且任务绑定的数据源（ceiling 输入）。
	DataSources []DiagnosisCeilingDataSource
	// AttachmentIDs 是当前仍 uploaded 且属于任务的附件（ceiling 输入）。
	AttachmentIDs []uuid.UUID
}

// DiagnosisRunContext 是诊断运行上下文的唯一生成点（深模块）：一次集中产出
//   - 有效 RunAccess（frozen Policy ∩ current ceiling）
//   - 安全、确定性的 task_context JSON 投影（system 指令尾部）
type DiagnosisRunContext struct {
	access      agentruntime.RunAccess
	taskContext string
}

func (c DiagnosisRunContext) Access() agentruntime.RunAccess { return c.access }

func (c DiagnosisRunContext) TaskContext() string { return c.taskContext }

// BuildDiagnosisRunContext 派生一次诊断执行的全部运行上下文。
// 任何校验失败都 fail-closed，绝不放宽 Policy。
func BuildDiagnosisRunContext(input DiagnosisRunContextInput) (DiagnosisRunContext, error) {
	profileTools, err := diagnosisProfileToolSet(input.ProfileToolNames)
	if err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run context: %w", err)
	}
	if input.ExternalCaseID == uuid.Nil {
		return DiagnosisRunContext{}, errors.New("build diagnosis run context: external case id is required")
	}
	ceilingSources, err := validateDiagnosisCeilingSources(input.DataSources)
	if err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run context: %w", err)
	}
	attachmentIDs := append([]uuid.UUID(nil), input.AttachmentIDs...)
	sort.Slice(attachmentIDs, func(i, j int) bool {
		return attachmentIDs[i].String() < attachmentIDs[j].String()
	})
	if _, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		AttachmentIDs: attachmentIDs,
	}); err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run context: %w", err)
	}

	// 1. 授权事实 = 持久化 frozen Policy，直接使用，不存在 legacy fallback。
	policy := input.Policy

	// 2. current AccessCeiling：固定 Profile 实际 Tool、active/read_only 且
	//    角色允许的数据源、仍 uploaded 的附件与当前有效用户状态。
	ceilingPermissions, err := agentruntime.NewPermissionSet(diagnosisCeilingPermissions(profileTools)...)
	if err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run context ceiling: %w", err)
	}
	ceilingGrants, err := diagnosisCeilingGrants(profileTools, input.ExternalCaseID, attachmentIDs, ceilingSources)
	if err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run context ceiling: %w", err)
	}

	// 3. 有效 RunAccess = frozen Policy ∩ current ceiling。
	access, err := agentruntime.DeriveDiagnosisRunAccess(policy, input.Actor, agentruntime.AccessCeiling{
		Permissions: ceilingPermissions,
		Grants:      ceilingGrants,
	})
	if err != nil {
		return DiagnosisRunContext{}, fmt.Errorf("build diagnosis run access: %w", err)
	}

	return DiagnosisRunContext{
		access:      access,
		taskContext: renderDiagnosisTaskContext(policy, access, ceilingSources),
	}, nil
}

func diagnosisProfileToolSet(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, errors.New("diagnosis profile tool names are required")
	}
	result := append([]string(nil), names...)
	sort.Strings(result)
	seen := make(map[string]struct{}, len(result))
	for _, name := range result {
		if !toolNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid diagnosis profile tool name %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate diagnosis profile tool name %q", name)
		}
		seen[name] = struct{}{}
	}
	return result, nil
}

func validateDiagnosisCeilingSources(sources []DiagnosisCeilingDataSource) ([]DiagnosisCeilingDataSource, error) {
	result := make([]DiagnosisCeilingDataSource, 0, len(sources))
	seen := make(map[uuid.UUID]struct{}, len(sources))
	for _, source := range sources {
		if source.ID == uuid.Nil {
			return nil, errors.New("diagnosis data source id is required")
		}
		if _, duplicate := seen[source.ID]; duplicate {
			return nil, fmt.Errorf("duplicate diagnosis data source %s", source.ID)
		}
		seen[source.ID] = struct{}{}
		scoped := ScopedDataSource{ID: source.ID, Role: source.Role, SafetyMode: source.SafetyMode}
		if err := scoped.Validate(); err != nil {
			return nil, fmt.Errorf("invalid diagnosis data source %s: %w", source.ID, err)
		}
		result = append(result, source)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	return result, nil
}

// diagnosisSQLToolRoles 复用 SQL Tool 注册时的角色白名单。
func diagnosisSQLToolRoles() []DataSourceRole {
	return []DataSourceRole{
		DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica,
	}
}

// diagnosisCeilingPermissions 从固定 diagnosis-default Profile 实际存在的
// Tool 派生权限上限；不使用瞬时依赖健康状态。
func diagnosisCeilingPermissions(profileTools []string) []agentruntime.Permission {
	has := func(name string) bool {
		index := sort.SearchStrings(profileTools, name)
		return index < len(profileTools) && profileTools[index] == name
	}
	permissions := make([]agentruntime.Permission, 0, 8)
	if has(ToolReadExternalCase) {
		permissions = append(permissions, agentruntime.PermissionCaseRead)
	}
	if has(ToolSearchKnowledge) {
		permissions = append(permissions, agentruntime.PermissionKnowledgeRead)
	}
	if has(ToolWebSearch) || has(ToolFetchPublicPage) {
		permissions = append(permissions, agentruntime.PermissionWebRead)
	}
	hasGitHub := false
	for _, name := range GitHubReadOnlyTools {
		if has(name) {
			hasGitHub = true
			break
		}
	}
	if hasGitHub {
		permissions = append(permissions, agentruntime.PermissionCodeRead)
	}
	if has(ToolSearchSchemaCatalog) || has(ToolExecuteReadonlyQuery) || has(ToolDatabaseObjectDefinition) {
		permissions = append(permissions, agentruntime.PermissionSQLRead)
	}
	if has(ToolReadAttachment) {
		permissions = append(permissions, agentruntime.PermissionAttachmentRead)
	}
	return permissions
}

// diagnosisCeilingGrants 构造当前资源上限：工单、仍 uploaded 的附件与
// read_only 且角色属于现有 SQL Tool 允许范围的任务绑定数据源。Profile 中
// 没有任何 SQL Tool 时数据源上限为空（"现有 SQL Tool 允许范围"不存在）；
// bounded_lab 永不进入 Grant；Repositories 保持为空。
func diagnosisCeilingGrants(
	profileTools []string,
	caseID uuid.UUID,
	attachmentIDs []uuid.UUID,
	sources []DiagnosisCeilingDataSource,
) (agentruntime.ResourceGrants, error) {
	has := func(name string) bool {
		index := sort.SearchStrings(profileTools, name)
		return index < len(profileTools) && profileTools[index] == name
	}
	hasSQLTool := has(ToolSearchSchemaCatalog) || has(ToolExecuteReadonlyQuery) || has(ToolDatabaseObjectDefinition)
	allowedRoles := diagnosisSQLToolRoles()
	dataSourceIDs := make([]uuid.UUID, 0, len(sources))
	if hasSQLTool {
		for _, source := range sources {
			if source.SafetyMode != DataSourceSafetyReadOnly {
				continue
			}
			if !slices.Contains(allowedRoles, source.Role) {
				continue
			}
			dataSourceIDs = append(dataSourceIDs, source.ID)
		}
	}
	return agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{caseID},
		AttachmentIDs:   attachmentIDs,
		DataSourceIDs:   dataSourceIDs,
	})
}

// diagnosisDataSourceProjection 是 task_context 中当前授权数据源的安全投影。
type diagnosisDataSourceProjection struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	SafetyMode string `json:"safetyMode"`
}

// diagnosisTaskContextProjection 是 task_context 的唯一投影结构：只包含
// Policy 协议版本、有效权限、当前工单 ID 与当前授权数据源的 id/role/
// safetyMode；凭证、连接地址、用户输入、附件正文、依赖错误和 Prompt
// 指令永不进入。encoding/json 默认 SetEscapeHTML，恶意值无法闭合外层
// <task_context> 块。
type diagnosisTaskContextProjection struct {
	PolicySchemaVersion  int                             `json:"policySchemaVersion"`
	EffectivePermissions []string                        `json:"effectivePermissions"`
	ExternalCaseID       string                          `json:"externalCaseId"`
	DataSources          []diagnosisDataSourceProjection `json:"dataSources,omitempty"`
}

func renderDiagnosisTaskContext(
	policy agentruntime.InvestigationPolicy,
	access agentruntime.RunAccess,
	sources []DiagnosisCeilingDataSource,
) string {
	grantedSources := make([]diagnosisDataSourceProjection, 0, len(sources))
	for _, source := range sources {
		if !access.Grants().AllowsDataSource(source.ID) {
			continue
		}
		grantedSources = append(grantedSources, diagnosisDataSourceProjection{
			ID: source.ID.String(), Role: string(source.Role), SafetyMode: string(source.SafetyMode),
		})
	}
	sort.Slice(grantedSources, func(i, j int) bool { return grantedSources[i].ID < grantedSources[j].ID })

	permissionValues := access.Permissions().Values()
	permissions := make([]string, len(permissionValues))
	for index, permission := range permissionValues {
		permissions[index] = string(permission)
	}
	externalCaseID := ""
	if caseIDs := access.Grants().ExternalCaseIDs(); len(caseIDs) > 0 {
		externalCaseID = caseIDs[0].String()
	}
	encoded, err := json.Marshal(diagnosisTaskContextProjection{
		PolicySchemaVersion:  policy.SchemaVersion(),
		EffectivePermissions: permissions,
		ExternalCaseID:       externalCaseID,
		DataSources:          grantedSources,
	})
	if err != nil {
		return ""
	}
	return "<task_context>\n" + string(encoded) + "\n</task_context>"
}

type diagnosisTaskContextKey struct{}

// WithDiagnosisTaskContext 把同一任务内稳定的 task_context 绑定到 Context；
// Runner 把它追加到 system 指令最尾部，同一任务的每轮 Evidence Gate 重试
// 保持一致。
func WithDiagnosisTaskContext(ctx context.Context, taskContext string) context.Context {
	return context.WithValue(ctx, diagnosisTaskContextKey{}, strings.TrimSpace(taskContext))
}

// DiagnosisTaskContextFromContext 读取本轮 task_context；缺失时返回空串。
func DiagnosisTaskContextFromContext(ctx context.Context) string {
	taskContext, _ := ctx.Value(diagnosisTaskContextKey{}).(string)
	return taskContext
}

// appendDiagnosisTaskContext 把 task_context 追加到 system 指令最尾部。
func appendDiagnosisTaskContext(instruction, taskContext string) string {
	taskContext = strings.TrimSpace(taskContext)
	if taskContext == "" {
		return instruction
	}
	return strings.TrimSpace(instruction) + "\n\n" + taskContext
}
