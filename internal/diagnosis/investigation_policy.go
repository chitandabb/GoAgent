package diagnosis

import (
	"errors"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/agentruntime"

	"github.com/google/uuid"
)

// InvestigationPolicySchemaVersion 是当前冻结 Policy JSON 的持久化协议版本
// （Diagnosis InvestigationPolicy JSON Schema v1）。与 request_scope_schema_version
// 相互独立：request_scope 只为旧 API 与 RequestedSkill 兼容保留，Policy 才是
// 新任务的授权事实。Unified Agent Runtime 的架构版本是 v2，与本协议版本无关。
const InvestigationPolicySchemaVersion = 1

// InvestigationPolicyMode 标识 diagnosis_tasks 行的 Policy 来源：
//   - legacy：migration 00034 之前的旧任务（Policy 两列必须同时为 NULL），
//     Worker 只能从冻结 request_scope 与任务资源做 legacy 派生；
//   - frozen：新任务创建时冻结的 Policy（两列必须同时非 NULL），Worker
//     直接使用持久化 Policy。
//
// mode 与两列的配对关系由 migration 00034 的数据库 CHECK 约束强制，
// Repository 与 Worker 解码侧再各自 fail-closed 校验。
type InvestigationPolicyMode string

const (
	InvestigationPolicyModeLegacy InvestigationPolicyMode = "legacy"
	InvestigationPolicyModeFrozen InvestigationPolicyMode = "frozen"
)

// Valid 大小写敏感：数据库约束同样只接受这两个精确值。
func (m InvestigationPolicyMode) Valid() bool {
	return m == InvestigationPolicyModeLegacy || m == InvestigationPolicyModeFrozen
}

// InvestigationPolicyBuilder 在任务创建事务内冻结 InvestigationPolicy。
// diagnosis 包只定义纯领域输入/输出，不依赖 platform/config：部署配置
// （case/knowledge/web/code/sql 上限与允许调查的数据源）由 bootstrap
// 转换成 Builder 构造参数注入 DiagnosisTaskService。
type InvestigationPolicyBuilder interface {
	Build(input InvestigationPolicyInput) (agentruntime.InvestigationPolicy, error)
}

// InvestigationPolicyInput 是单个任务冻结 Policy 所需的纯领域事实。
type InvestigationPolicyInput struct {
	// ExternalCaseID 是当前工单，always 进入 Grant。
	ExternalCaseID uuid.UUID
	// DataSourceIDs 是任务绑定的候选数据源（工单源 + 证据源）；Builder 只
	// 保留部署允许调查的数据源，其余一律丢弃。
	DataSourceIDs []uuid.UUID
	// AttachmentIDs 是任务实际冻结的附件；非空时额外授予 attachment.read。
	AttachmentIDs []uuid.UUID
}

// InvestigationPolicyConfig 是部署级 Builder 参数：权限上限由 bootstrap
// 从部署配置推导，数据源上限只包含部署允许调查的只读源。
type InvestigationPolicyConfig struct {
	BasePermissions      []agentruntime.Permission
	AllowedDataSourceIDs []uuid.UUID
}

type staticInvestigationPolicyBuilder struct {
	permissions      agentruntime.PermissionSet
	dataSourceIDs    []uuid.UUID
	attachmentNeeded bool
}

// NewInvestigationPolicyBuilder 校验部署级 Policy 参数并返回纯函数 Builder。
// 校验规则：必须包含 case.read（诊断基础上限）、禁止 task.read/memory.read/
// diagnosis.create、允许调查的数据源必须是非空 UUID 且不重复。
func NewInvestigationPolicyBuilder(cfg InvestigationPolicyConfig) (InvestigationPolicyBuilder, error) {
	if len(cfg.BasePermissions) == 0 {
		return nil, errors.New("investigation policy base permissions are required")
	}
	seen := make(map[agentruntime.Permission]struct{}, len(cfg.BasePermissions))
	for _, permission := range cfg.BasePermissions {
		if !permission.Valid() {
			return nil, fmt.Errorf("invalid investigation policy permission %q", permission)
		}
		if _, duplicate := seen[permission]; duplicate {
			return nil, fmt.Errorf("duplicate investigation policy permission %q", permission)
		}
		seen[permission] = struct{}{}
		switch permission {
		case agentruntime.PermissionTaskRead, agentruntime.PermissionMemoryRead, agentruntime.PermissionDiagnosisCreate:
			return nil, fmt.Errorf("investigation policy must not grant %q", permission)
		}
	}
	if _, ok := seen[agentruntime.PermissionCaseRead]; !ok {
		return nil, errors.New("investigation policy base permissions must include case.read")
	}
	permissions, err := agentruntime.NewPermissionSet(cfg.BasePermissions...)
	if err != nil {
		return nil, fmt.Errorf("build investigation policy permissions: %w", err)
	}
	dataSourceIDs := uniqueSortedUUIDs(cfg.AllowedDataSourceIDs)
	if len(dataSourceIDs) != len(cfg.AllowedDataSourceIDs) {
		return nil, errors.New("investigation policy data sources contain duplicates")
	}
	return &staticInvestigationPolicyBuilder{
		permissions:   permissions,
		dataSourceIDs: dataSourceIDs,
	}, nil
}

func (b *staticInvestigationPolicyBuilder) Build(input InvestigationPolicyInput) (agentruntime.InvestigationPolicy, error) {
	if b == nil {
		return agentruntime.InvestigationPolicy{}, errors.New("investigation policy builder is unavailable")
	}
	if input.ExternalCaseID == uuid.Nil {
		return agentruntime.InvestigationPolicy{}, errors.New("investigation policy external case id is required")
	}
	attachmentIDs := uniqueSortedUUIDs(input.AttachmentIDs)
	if len(attachmentIDs) != len(input.AttachmentIDs) {
		return agentruntime.InvestigationPolicy{}, errors.New("investigation policy attachments contain duplicates")
	}
	permissions := b.permissions.Values()
	if len(attachmentIDs) > 0 && !b.permissions.Has(agentruntime.PermissionAttachmentRead) {
		permissions = append(permissions, agentruntime.PermissionAttachmentRead)
	}
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		return agentruntime.InvestigationPolicy{}, fmt.Errorf("build investigation policy permissions: %w", err)
	}

	// 数据源 Grant = 任务绑定 ∩ 部署允许调查；Repositories 保持为空，
	// GitHub 仓库边界继续由 Token/App 权限与只读参数策略承担。
	allowed := make(map[uuid.UUID]struct{}, len(b.dataSourceIDs))
	for _, id := range b.dataSourceIDs {
		allowed[id] = struct{}{}
	}
	grantedDataSources := make([]uuid.UUID, 0, min(len(input.DataSourceIDs), len(b.dataSourceIDs)))
	for _, candidate := range uniqueSortedUUIDs(input.DataSourceIDs) {
		if _, ok := allowed[candidate]; ok {
			grantedDataSources = append(grantedDataSources, candidate)
		}
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{input.ExternalCaseID},
		AttachmentIDs:   attachmentIDs,
		DataSourceIDs:   grantedDataSources,
	})
	if err != nil {
		return agentruntime.InvestigationPolicy{}, fmt.Errorf("build investigation policy grants: %w", err)
	}
	return agentruntime.NewInvestigationPolicy(InvestigationPolicySchemaVersion, permissionSet, grants)
}

// attachmentIDsFromTaskAttachments 提取任务冻结附件的 UUID（已由
// normalizeCreateTaskInput 去重排序）。
func attachmentIDsFromTaskAttachments(attachments []TaskAttachment) []uuid.UUID {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]uuid.UUID, 0, len(attachments))
	for _, attachment := range attachments {
		result = append(result, attachment.AttachmentID)
	}
	return uniqueSortedUUIDs(result)
}
