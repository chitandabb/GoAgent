package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/observability"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type ToolRegistration struct {
	Tool                 tool.BaseTool
	FailurePolicy        resilience.Policy
	DegradationObserver  resilience.Observer
	AllowedRoles         []auth.Role
	AllowedTaskTypes     []TaskType
	AllowedDataRoles     []DataSourceRole
	AllowedSafetyModes   []DataSourceSafetyMode
	RequiredCapabilities []ToolCapability
	RequiredDependencies []ToolDependency
	RequiredPermissions  []agentruntime.Permission
}

type catalogEntry struct {
	name                 string
	tool                 tool.BaseTool
	failurePolicy        resilience.Policy
	degradationObserver  resilience.Observer
	allowedRoles         []auth.Role
	allowedTaskTypes     []TaskType
	allowedDataRoles     []DataSourceRole
	allowedSafetyModes   []DataSourceSafetyMode
	requiredCapabilities []ToolCapability
	requiredDependencies []ToolDependency
	requiredPermissions  []agentruntime.Permission
}

// ResolvedToolProfile 是一次 Profile 解析的完整结果：Tools 是已包装
// accessGuardedTool 的 Catalog-owned Tool，ModelVisibleNames 是模型最终应该
// 看到的完整稳定 Schema 名单（包含 Middleware-owned Tool 如 skill）。
type ResolvedToolProfile struct {
	ID                agentruntime.ToolProfileID
	Tools             []tool.BaseTool
	ModelVisibleNames []string
}

// AgentToolProvider 根据固定的部署级 Profile ID 返回一次运行的工具合同。
// 调用方只提供 Profile ID，不需要了解注册表和名称匹配细节。
type AgentToolProvider interface {
	ResolveProfile(ctx context.Context, profileID agentruntime.ToolProfileID) (ResolvedToolProfile, error)
}

// ToolCatalog 在启动时完成注册并保持只读；注册不等于向某次模型调用暴露。
type ToolCatalog struct {
	entries             []catalogEntry
	entriesByName       map[string]catalogEntry
	profile             *agentruntime.ToolProfile
	middlewareOwnedName string
}

func NewToolCatalog(ctx context.Context, registrations ...ToolRegistration) (*ToolCatalog, error) {
	if len(registrations) == 0 {
		return nil, errors.New("at least one tool registration is required")
	}
	entries := make([]catalogEntry, 0, len(registrations))
	seenNames := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		entry, err := newCatalogEntry(ctx, registration)
		if err != nil {
			return nil, err
		}
		if _, exists := seenNames[entry.name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", entry.name)
		}
		seenNames[entry.name] = struct{}{}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	entriesByName := make(map[string]catalogEntry, len(entries))
	for _, entry := range entries {
		entriesByName[entry.name] = entry
	}
	return &ToolCatalog{entries: entries, entriesByName: entriesByName}, nil
}

// BindProfile 把部署级 ToolProfile 绑定到 Catalog，并把 Middleware-owned
// 名称（当前仅 ToolSkill）与注册表区分开。每个生产 Catalog 只绑定它实际
// 负责的 Runtime Profile（Diagnosis Catalog -> diagnosis-default，
// Conversation Catalog -> conversation-default）。Profile 引用的每个名字
// 要么已注册（Catalog-owned），要么已声明为 Middleware-owned；否则启动期
// 失败。Profile 只允许绑定一次，绑定后保持不可变。
func (c *ToolCatalog) BindProfile(profile agentruntime.ToolProfile, middlewareOwned []string) error {
	if c == nil {
		return errors.New("tool catalog is nil")
	}
	if c.profile != nil {
		return fmt.Errorf("tool catalog profile is already bound to %q", c.profile.ID())
	}
	if len(middlewareOwned) != 1 || middlewareOwned[0] != ToolSkill {
		return fmt.Errorf("unsupported middleware-owned tool names %v", middlewareOwned)
	}
	for _, name := range profile.ToolNames() {
		if _, registered := c.entriesByName[name]; registered {
			continue
		}
		if name != ToolSkill {
			return fmt.Errorf("profile %q references unregistered tool %q", profile.ID(), name)
		}
	}
	bound := profile
	c.profile = &bound
	c.middlewareOwnedName = middlewareOwned[0]
	return nil
}

// BoundProfileID 返回本 Catalog 绑定的 Runtime Profile；未绑定返回空值。
func (c *ToolCatalog) BoundProfileID() agentruntime.ToolProfileID {
	if c == nil || c.profile == nil {
		return ""
	}
	return c.profile.ID()
}

// ResolveProfile 返回固定 Profile 的完整模型可见合同。Tools 是 Catalog-owned
// 且已包装 accessGuardedTool 的执行器集合（不含 Middleware-owned 名称）；
// ModelVisibleNames 是稳定 Schema 名单（含 Middleware-owned 名称）。
// 未绑定 Profile 或请求非本 Catalog 负责的 Profile 时失败。
// 返回值全部防御性复制，调用方修改不会影响 Catalog 或后续解析。
func (c *ToolCatalog) ResolveProfile(_ context.Context, profileID agentruntime.ToolProfileID) (ResolvedToolProfile, error) {
	if c == nil {
		return ResolvedToolProfile{}, errors.New("tool catalog is nil")
	}
	if c.profile == nil {
		return ResolvedToolProfile{}, errors.New("tool catalog has no bound deployment profile")
	}
	if c.profile.ID() != profileID {
		return ResolvedToolProfile{}, fmt.Errorf(
			"tool catalog is bound to profile %q, cannot resolve %q", c.profile.ID(), profileID,
		)
	}
	tools := make([]tool.BaseTool, 0, len(c.profile.ToolNames()))
	visibleNames := make([]string, 0, len(c.profile.ToolNames()))
	for _, name := range c.profile.ToolNames() {
		visibleNames = append(visibleNames, name)
		if name == c.middlewareOwnedName {
			continue
		}
		entry, registered := c.entriesByName[name]
		if !registered {
			return ResolvedToolProfile{}, fmt.Errorf("profile %q references unregistered tool %q", profileID, name)
		}
		guarded, err := entry.scopedTool()
		if err != nil {
			return ResolvedToolProfile{}, err
		}
		tools = append(tools, guarded)
	}
	return ResolvedToolProfile{ID: profileID, Tools: tools, ModelVisibleNames: visibleNames}, nil
}

func newCatalogEntry(ctx context.Context, registration ToolRegistration) (catalogEntry, error) {
	if registration.Tool == nil {
		return catalogEntry{}, errors.New("registered tool is nil")
	}
	info, err := registration.Tool.Info(ctx)
	if err != nil {
		return catalogEntry{}, fmt.Errorf("read registered tool info: %w", err)
	}
	if info == nil {
		return catalogEntry{}, errors.New("registered tool info is nil")
	}
	if !toolNamePattern.MatchString(info.Name) {
		return catalogEntry{}, fmt.Errorf("registered tool has invalid name %q", info.Name)
	}
	if err := validatePolicyValues(registration); err != nil {
		return catalogEntry{}, fmt.Errorf("tool %q policy is invalid: %w", info.Name, err)
	}
	if registration.FailurePolicy == resilience.PolicyBestEffort && len(info.Name) > 64 {
		return catalogEntry{}, fmt.Errorf("best-effort tool %q exceeds degradation operation limit", info.Name)
	}
	return catalogEntry{
		name: info.Name, tool: registration.Tool, failurePolicy: registration.FailurePolicy,
		degradationObserver:  registration.DegradationObserver,
		allowedRoles:         append([]auth.Role(nil), registration.AllowedRoles...),
		allowedTaskTypes:     append([]TaskType(nil), registration.AllowedTaskTypes...),
		allowedDataRoles:     append([]DataSourceRole(nil), registration.AllowedDataRoles...),
		allowedSafetyModes:   append([]DataSourceSafetyMode(nil), registration.AllowedSafetyModes...),
		requiredCapabilities: append([]ToolCapability(nil), registration.RequiredCapabilities...),
		requiredDependencies: append([]ToolDependency(nil), registration.RequiredDependencies...),
		requiredPermissions:  append([]agentruntime.Permission(nil), registration.RequiredPermissions...),
	}, nil
}

func validatePolicyValues(registration ToolRegistration) error {
	if registration.FailurePolicy != resilience.PolicyStrict &&
		registration.FailurePolicy != resilience.PolicyBestEffort {
		return errors.New("failure policy must be strict or best_effort")
	}
	if len(registration.AllowedRoles) == 0 {
		return errors.New("allowed roles are required")
	}
	if len(registration.AllowedTaskTypes) == 0 {
		return errors.New("allowed task types are required")
	}
	for _, role := range registration.AllowedRoles {
		if !role.Valid() {
			return fmt.Errorf("invalid role %q", role)
		}
	}
	for _, taskType := range registration.AllowedTaskTypes {
		if !taskType.Valid() {
			return fmt.Errorf("invalid task type %q", taskType)
		}
	}
	for _, role := range registration.AllowedDataRoles {
		if !role.Valid() {
			return fmt.Errorf("invalid data source role %q", role)
		}
	}
	for _, safetyMode := range registration.AllowedSafetyModes {
		if !safetyMode.Valid() {
			return fmt.Errorf("invalid safety mode %q", safetyMode)
		}
	}
	for _, capability := range registration.RequiredCapabilities {
		if !capability.Valid() {
			return fmt.Errorf("invalid capability %q", capability)
		}
	}
	for _, dependency := range registration.RequiredDependencies {
		if !dependency.Valid() {
			return fmt.Errorf("invalid dependency %q", dependency)
		}
	}
	for _, permission := range registration.RequiredPermissions {
		if !permission.Valid() {
			return fmt.Errorf("invalid permission %q", permission)
		}
	}
	if hasDuplicate(registration.AllowedRoles) || hasDuplicate(registration.AllowedTaskTypes) ||
		hasDuplicate(registration.AllowedDataRoles) || hasDuplicate(registration.AllowedSafetyModes) ||
		hasDuplicate(registration.RequiredCapabilities) ||
		hasDuplicate(registration.RequiredDependencies) ||
		hasDuplicate(registration.RequiredPermissions) {
		return errors.New("policy contains duplicate values")
	}
	return nil
}

func hasDuplicate[T comparable](values []T) bool {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func (c *ToolCatalog) ToolsFor(_ context.Context, scope TaskScope) ([]tool.BaseTool, error) {
	if err := validateToolScope(c, scope); err != nil {
		return nil, err
	}
	tools := make([]tool.BaseTool, 0, len(c.entries))
	for _, entry := range c.entries {
		if entry.authorized(scope) {
			guarded, err := entry.scopedTool()
			if err != nil {
				return nil, err
			}
			tools = append(tools, guarded)
		}
	}
	return tools, nil
}

// EvaluationBaselineToolsFor 返回评测 baseline 所需的宽 Tool Schema 集合。
//
// 这个集合只按角色和任务类型筛选，故意不套用本次 TaskScope 的数据源与依赖
// 过滤；它用于和 experiment 的最小运行时 Schema 做 paired evaluation，不能
// 作为生产授权策略使用。Skill reference Tool 也不属于 baseline，因为 baseline
// 不启用 Skill 渐进式读取。
func (c *ToolCatalog) EvaluationBaselineToolsFor(_ context.Context, scope TaskScope) ([]tool.BaseTool, error) {
	if err := validateToolScope(c, scope); err != nil {
		return nil, err
	}
	tools := make([]tool.BaseTool, 0, len(c.entries))
	for _, entry := range c.entries {
		if entry.name == ToolReadSkillReference || !entry.matchesRoleAndTask(scope) {
			continue
		}
		tools = append(tools, entry.tool)
	}
	return tools, nil
}

func validateToolScope(c *ToolCatalog, scope TaskScope) error {
	if c == nil {
		return errors.New("tool catalog is nil")
	}
	if scope.userID == uuid.Nil || !scope.role.Valid() || !scope.taskType.Valid() ||
		(len(scope.allowedCapabilities) == 0 && scope.taskType != TaskTypeConversation) {
		return errors.New("task scope is invalid")
	}
	return nil
}

func (entry catalogEntry) matchesRoleAndTask(scope TaskScope) bool {
	return slices.Contains(entry.allowedRoles, scope.role) &&
		slices.Contains(entry.allowedTaskTypes, scope.taskType)
}

func (entry catalogEntry) authorized(scope TaskScope) bool {
	if !entry.matchesRoleAndTask(scope) ||
		!scope.matchesDataSource(entry.allowedDataRoles, entry.allowedSafetyModes) {
		return false
	}
	for _, capability := range entry.requiredCapabilities {
		if !scope.CapabilityAllowed(capability) {
			return false
		}
	}
	for _, dependency := range entry.requiredDependencies {
		if !scope.DependencyAvailable(dependency) {
			return false
		}
	}
	return true
}

func (entry catalogEntry) scopedTool() (tool.BaseTool, error) {
	invokable, ok := entry.tool.(tool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("registered tool %q is not invokable", entry.name)
	}
	return &accessGuardedTool{inner: invokable, entry: entry}, nil
}

// accessGuardedTool 是执行期授权 Guard。生产 Schema 来自固定 ToolProfile
// （ResolveProfile 按 Profile 名单装配），不再由 ToolsFor 的 v1 过滤决定；
// ToolsFor 只保留给历史评测。执行期它读取 v2 RunAccess 做粗粒度 Permission
// 校验。具体 ResourceGrant 的统一投影与 Tool 内部检查尚未全部迁移完成：
// 当前部分 Tool 继续使用原有的 CommandContext/owner 校验。
type accessGuardedTool struct {
	inner tool.InvokableTool
	entry catalogEntry
}

func (t *accessGuardedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *accessGuardedTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	if !ok {
		return "", ErrRunAccessRequired
	}
	for _, permission := range t.entry.requiredPermissions {
		if !access.Allows(permission) {
			return "", fmt.Errorf("%w: %s", ErrToolNotAllowed, t.entry.name)
		}
	}
	startedAt := time.Now()
	result, err := t.inner.InvokableRun(ctx, arguments, opts...)
	if err == nil || t.entry.failurePolicy == resilience.PolicyStrict || ctx.Err() != nil {
		return result, err
	}
	disposition := resilience.FailureDispositionOf(err)
	if disposition == resilience.FailureStrict {
		return "", err
	}
	identity, ok := resilience.RunIdentityFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("execute best-effort tool %q without run identity: %w", t.entry.name, err)
	}
	if disposition == resilience.FailureRejected {
		return encodeToolFailure(t.entry.name, "tool_call_rejected", "tool_failure_not_retryable", false, nil)
	}
	event := resilience.DegradationEvent{
		Operation: t.entry.name, Policy: resilience.PolicyBestEffort,
		Fallback: "agent_selects_alternative_source", ReasonCode: "tool_execution_failed",
		RunID: identity.RunID, TraceID: identity.TraceID,
		DurationMillis: max(time.Since(startedAt).Milliseconds(), 0),
	}
	if validateErr := event.Validate(); validateErr != nil {
		return "", fmt.Errorf("build best-effort tool degradation: %w", validateErr)
	}
	if t.entry.degradationObserver != nil {
		t.entry.degradationObserver.ObserveDegradation(event)
	}
	observability.RecordDegradation(ctx, event)
	return encodeToolFailure(
		t.entry.name, "tool_unavailable", "tool_execution_failed", true,
		[]resilience.DegradationEvent{event},
	)
}

func encodeToolFailure(
	toolName string,
	errorCode string,
	reasonCode string,
	retryable bool,
	degradations []resilience.DegradationEvent,
) (string, error) {
	encoded, marshalErr := json.Marshal(struct {
		OK           bool                          `json:"ok"`
		Error        string                        `json:"error"`
		Tool         string                        `json:"tool"`
		ReasonCode   string                        `json:"reasonCode"`
		Retryable    bool                          `json:"retryable"`
		Degradations []resilience.DegradationEvent `json:"degradations,omitempty"`
	}{
		Error: errorCode, Tool: toolName, ReasonCode: reasonCode,
		Retryable: retryable, Degradations: degradations,
	})
	if marshalErr != nil {
		return "", fmt.Errorf("encode best-effort tool failure: %w", marshalErr)
	}
	return string(encoded), nil
}
