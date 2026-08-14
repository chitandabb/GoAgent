package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

// RuntimeKind distinguishes the two execution modes without assigning business
// capabilities to either mode.
type RuntimeKind string

const (
	RuntimeKindConversation RuntimeKind = "conversation"
	RuntimeKindDiagnosis    RuntimeKind = "diagnosis"
)

func (kind RuntimeKind) Valid() bool {
	return kind == RuntimeKindConversation || kind == RuntimeKindDiagnosis
}

// Permission is an executable operation granted to one Agent run.
type Permission string

const (
	PermissionKnowledgeRead   Permission = "knowledge.read"
	PermissionSQLRead         Permission = "sql.read"
	PermissionCodeRead        Permission = "code.read"
	PermissionCaseRead        Permission = "case.read"
	PermissionAttachmentRead  Permission = "attachment.read"
	PermissionTaskRead        Permission = "task.read"
	PermissionWebRead         Permission = "web.read"
	PermissionMemoryRead      Permission = "memory.read"
	PermissionDiagnosisCreate Permission = "diagnosis.create"
)

func (permission Permission) Valid() bool {
	switch permission {
	case PermissionKnowledgeRead, PermissionSQLRead, PermissionCodeRead, PermissionCaseRead,
		PermissionAttachmentRead, PermissionTaskRead, PermissionWebRead, PermissionMemoryRead,
		PermissionDiagnosisCreate:
		return true
	default:
		return false
	}
}

// PermissionSet is an immutable, normalized set.
type PermissionSet struct {
	values []Permission
}

func NewPermissionSet(values ...Permission) (PermissionSet, error) {
	copyValues := append([]Permission(nil), values...)
	seen := make(map[Permission]struct{}, len(copyValues))
	for _, value := range copyValues {
		if !value.Valid() {
			return PermissionSet{}, fmt.Errorf("invalid permission %q", value)
		}
		if _, exists := seen[value]; exists {
			return PermissionSet{}, fmt.Errorf("duplicate permission %q", value)
		}
		seen[value] = struct{}{}
	}
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	return PermissionSet{values: copyValues}, nil
}

func (set PermissionSet) Values() []Permission {
	return append([]Permission(nil), set.values...)
}

func (set PermissionSet) Has(permission Permission) bool {
	index := sort.Search(len(set.values), func(index int) bool { return set.values[index] >= permission })
	return index < len(set.values) && set.values[index] == permission
}

func (set PermissionSet) validate() error {
	_, err := NewPermissionSet(set.values...)
	return err
}

func (set PermissionSet) intersect(other PermissionSet) PermissionSet {
	values := make([]Permission, 0, min(len(set.values), len(other.values)))
	for _, value := range set.values {
		if other.Has(value) {
			values = append(values, value)
		}
	}
	return PermissionSet{values: values}
}

type ResourceGrantsConfig struct {
	DataSourceIDs   []uuid.UUID
	ExternalCaseIDs []uuid.UUID
	AttachmentIDs   []uuid.UUID
	TaskIDs         []uuid.UUID
	Repositories    []string
}

// ResourceGrants identifies the concrete resources a run may access. Empty
// collections grant nothing; they never mean unrestricted access.
type ResourceGrants struct {
	dataSourceIDs   []uuid.UUID
	externalCaseIDs []uuid.UUID
	attachmentIDs   []uuid.UUID
	taskIDs         []uuid.UUID
	repositories    []string
}

func NewResourceGrants(config ResourceGrantsConfig) (ResourceGrants, error) {
	dataSourceIDs, err := normalizeUUIDs("data source", config.DataSourceIDs)
	if err != nil {
		return ResourceGrants{}, err
	}
	externalCaseIDs, err := normalizeUUIDs("external case", config.ExternalCaseIDs)
	if err != nil {
		return ResourceGrants{}, err
	}
	attachmentIDs, err := normalizeUUIDs("attachment", config.AttachmentIDs)
	if err != nil {
		return ResourceGrants{}, err
	}
	taskIDs, err := normalizeUUIDs("task", config.TaskIDs)
	if err != nil {
		return ResourceGrants{}, err
	}
	repositories, err := normalizeRepositories(config.Repositories)
	if err != nil {
		return ResourceGrants{}, err
	}
	return ResourceGrants{
		dataSourceIDs: dataSourceIDs, externalCaseIDs: externalCaseIDs,
		attachmentIDs: attachmentIDs, taskIDs: taskIDs, repositories: repositories,
	}, nil
}

func (grants ResourceGrants) DataSourceIDs() []uuid.UUID {
	return append([]uuid.UUID(nil), grants.dataSourceIDs...)
}

func (grants ResourceGrants) ExternalCaseIDs() []uuid.UUID {
	return append([]uuid.UUID(nil), grants.externalCaseIDs...)
}

func (grants ResourceGrants) AttachmentIDs() []uuid.UUID {
	return append([]uuid.UUID(nil), grants.attachmentIDs...)
}

func (grants ResourceGrants) TaskIDs() []uuid.UUID {
	return append([]uuid.UUID(nil), grants.taskIDs...)
}

func (grants ResourceGrants) Repositories() []string {
	return append([]string(nil), grants.repositories...)
}

func (grants ResourceGrants) AllowsDataSource(id uuid.UUID) bool {
	return containsUUID(grants.dataSourceIDs, id)
}

func (grants ResourceGrants) AllowsExternalCase(id uuid.UUID) bool {
	return containsUUID(grants.externalCaseIDs, id)
}

func (grants ResourceGrants) AllowsAttachment(id uuid.UUID) bool {
	return containsUUID(grants.attachmentIDs, id)
}

func (grants ResourceGrants) AllowsTask(id uuid.UUID) bool {
	return containsUUID(grants.taskIDs, id)
}

func (grants ResourceGrants) AllowsRepository(repository string) bool {
	repository = strings.TrimSpace(repository)
	index := sort.SearchStrings(grants.repositories, repository)
	return index < len(grants.repositories) && grants.repositories[index] == repository
}

func (grants ResourceGrants) validate() error {
	_, err := NewResourceGrants(ResourceGrantsConfig{
		DataSourceIDs: grants.dataSourceIDs, ExternalCaseIDs: grants.externalCaseIDs,
		AttachmentIDs: grants.attachmentIDs, TaskIDs: grants.taskIDs, Repositories: grants.repositories,
	})
	return err
}

func (grants ResourceGrants) intersect(other ResourceGrants) ResourceGrants {
	return ResourceGrants{
		dataSourceIDs:   intersectUUIDs(grants.dataSourceIDs, other.dataSourceIDs),
		externalCaseIDs: intersectUUIDs(grants.externalCaseIDs, other.externalCaseIDs),
		attachmentIDs:   intersectUUIDs(grants.attachmentIDs, other.attachmentIDs),
		taskIDs:         intersectUUIDs(grants.taskIDs, other.taskIDs),
		repositories:    intersectStrings(grants.repositories, other.repositories),
	}
}

type Actor struct {
	UserID uuid.UUID
	Role   auth.Role
}

func (actor Actor) Validate() error {
	if actor.UserID == uuid.Nil {
		return errors.New("actor user id is required")
	}
	if !actor.Role.Valid() {
		return fmt.Errorf("actor role %q is invalid", actor.Role)
	}
	return nil
}

type InvestigationPolicy struct {
	schemaVersion int
	permissions   PermissionSet
	grants        ResourceGrants
}

func NewInvestigationPolicy(
	schemaVersion int,
	permissions PermissionSet,
	grants ResourceGrants,
) (InvestigationPolicy, error) {
	if schemaVersion < 1 {
		return InvestigationPolicy{}, errors.New("investigation policy schema version must be positive")
	}
	if err := permissions.validate(); err != nil {
		return InvestigationPolicy{}, fmt.Errorf("invalid investigation policy permissions: %w", err)
	}
	if len(permissions.values) == 0 {
		return InvestigationPolicy{}, errors.New("investigation policy requires at least one permission")
	}
	if err := grants.validate(); err != nil {
		return InvestigationPolicy{}, fmt.Errorf("invalid investigation policy grants: %w", err)
	}
	return InvestigationPolicy{
		schemaVersion: schemaVersion,
		permissions:   PermissionSet{values: permissions.Values()},
		grants:        grants.copy(),
	}, nil
}

func (policy InvestigationPolicy) SchemaVersion() int { return policy.schemaVersion }

func (policy InvestigationPolicy) Permissions() PermissionSet {
	return PermissionSet{values: policy.permissions.Values()}
}

func (policy InvestigationPolicy) Grants() ResourceGrants { return policy.grants.copy() }

func (policy InvestigationPolicy) validate() error {
	_, err := NewInvestigationPolicy(policy.schemaVersion, policy.permissions, policy.grants)
	return err
}

// AccessCeiling is the current upper bound after emergency revocation and
// resource disablement. Derivation intersects it with the frozen task policy.
type AccessCeiling struct {
	Permissions PermissionSet
	Grants      ResourceGrants
}

func (ceiling AccessCeiling) validate() error {
	if err := ceiling.Permissions.validate(); err != nil {
		return fmt.Errorf("invalid access ceiling permissions: %w", err)
	}
	if err := ceiling.Grants.validate(); err != nil {
		return fmt.Errorf("invalid access ceiling grants: %w", err)
	}
	return nil
}

type RunAccess struct {
	actor       Actor
	runtimeKind RuntimeKind
	permissions PermissionSet
	grants      ResourceGrants
}

func DeriveDiagnosisRunAccess(
	policy InvestigationPolicy,
	actor Actor,
	ceiling AccessCeiling,
) (RunAccess, error) {
	if err := policy.validate(); err != nil {
		return RunAccess{}, fmt.Errorf("derive diagnosis run access: %w", err)
	}
	if err := actor.Validate(); err != nil {
		return RunAccess{}, fmt.Errorf("derive diagnosis run access: %w", err)
	}
	if err := ceiling.validate(); err != nil {
		return RunAccess{}, fmt.Errorf("derive diagnosis run access: %w", err)
	}
	return RunAccess{
		actor:       actor,
		runtimeKind: RuntimeKindDiagnosis,
		permissions: policy.permissions.intersect(ceiling.Permissions),
		grants:      policy.grants.intersect(ceiling.Grants),
	}, nil
}

func (access RunAccess) Actor() Actor { return access.actor }

func (access RunAccess) RuntimeKind() RuntimeKind { return access.runtimeKind }

func (access RunAccess) Permissions() PermissionSet {
	return PermissionSet{values: access.permissions.Values()}
}

func (access RunAccess) Grants() ResourceGrants { return access.grants.copy() }

func (access RunAccess) Allows(permission Permission) bool {
	return access.permissions.Has(permission)
}

// NewConversationRunAccess 是 Conversation RunAccess 的唯一公开构造入口：
// RuntimeKind 固定为 conversation，输入均防御性复制，禁止通过该入口构造
// Diagnosis RunAccess（Diagnosis 必须经 DeriveDiagnosisRunAccess 从 Policy 派生）。
func NewConversationRunAccess(
	actor Actor,
	permissions PermissionSet,
	grants ResourceGrants,
) (RunAccess, error) {
	if err := actor.Validate(); err != nil {
		return RunAccess{}, fmt.Errorf("new conversation run access: %w", err)
	}
	if err := permissions.validate(); err != nil {
		return RunAccess{}, fmt.Errorf("new conversation run access: %w", err)
	}
	if err := grants.validate(); err != nil {
		return RunAccess{}, fmt.Errorf("new conversation run access: %w", err)
	}
	return RunAccess{
		actor:       actor,
		runtimeKind: RuntimeKindConversation,
		permissions: PermissionSet{values: permissions.Values()},
		grants:      grants.copy(),
	}, nil
}

// Validate 校验 RunAccess 的完整结构；Context 读取与执行期 Guard 都依赖它做 fail-closed 判定。
func (access RunAccess) Validate() error {
	if err := access.actor.Validate(); err != nil {
		return fmt.Errorf("run access: %w", err)
	}
	if access.runtimeKind != RuntimeKindConversation && access.runtimeKind != RuntimeKindDiagnosis {
		return fmt.Errorf("run access: invalid runtime kind %q", access.runtimeKind)
	}
	if err := access.permissions.validate(); err != nil {
		return fmt.Errorf("run access: %w", err)
	}
	if err := access.grants.validate(); err != nil {
		return fmt.Errorf("run access: %w", err)
	}
	return nil
}

type runAccessContextKey struct{}

// WithRunAccess 把一次运行的 RunAccess 快照绑定到 Context；值本身不可变，
// 调用方无法通过返回的切片反向修改 Context 中的值。
func WithRunAccess(ctx context.Context, access RunAccess) context.Context {
	return context.WithValue(ctx, runAccessContextKey{}, access)
}

// RunAccessFromContext 读取本轮 RunAccess；不存在或非法（Validate 失败）时
// 返回 false，调用方必须 fail-closed。
func RunAccessFromContext(ctx context.Context) (RunAccess, bool) {
	access, ok := ctx.Value(runAccessContextKey{}).(RunAccess)
	if !ok {
		return RunAccess{}, false
	}
	if err := access.Validate(); err != nil {
		return RunAccess{}, false
	}
	return access, true
}

func (grants ResourceGrants) copy() ResourceGrants {
	return ResourceGrants{
		dataSourceIDs: grants.DataSourceIDs(), externalCaseIDs: grants.ExternalCaseIDs(),
		attachmentIDs: grants.AttachmentIDs(), taskIDs: grants.TaskIDs(), repositories: grants.Repositories(),
	}
}

func normalizeUUIDs(label string, values []uuid.UUID) ([]uuid.UUID, error) {
	result := append([]uuid.UUID(nil), values...)
	seen := make(map[uuid.UUID]struct{}, len(result))
	for _, value := range result {
		if value == uuid.Nil {
			return nil, fmt.Errorf("%s id is required", label)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("duplicate %s id %s", label, value)
		}
		seen[value] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}

func normalizeRepositories(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for index, value := range result {
		value = strings.TrimSpace(value)
		parts := strings.Split(value, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(value, "\\\r\n\t ") {
			return nil, fmt.Errorf("invalid repository %q", value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("duplicate repository %q", value)
		}
		seen[value] = struct{}{}
		result[index] = value
	}
	sort.Strings(result)
	return result, nil
}

func containsUUID(values []uuid.UUID, candidate uuid.UUID) bool {
	index := sort.Search(len(values), func(index int) bool {
		return values[index].String() >= candidate.String()
	})
	return index < len(values) && values[index] == candidate
}

func intersectUUIDs(left, right []uuid.UUID) []uuid.UUID {
	result := make([]uuid.UUID, 0, min(len(left), len(right)))
	for _, value := range left {
		if containsUUID(right, value) {
			result = append(result, value)
		}
	}
	return result
}

func intersectStrings(left, right []string) []string {
	result := make([]string, 0, min(len(left), len(right)))
	for _, value := range left {
		index := sort.SearchStrings(right, value)
		if index < len(right) && right[index] == value {
			result = append(result, value)
		}
	}
	return result
}
