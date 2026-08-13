package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
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
}

// AgentToolProvider 根据一次任务的授权快照返回模型实际可见的 Tool。
type AgentToolProvider interface {
	ToolsFor(ctx context.Context, scope TaskScope) ([]tool.BaseTool, error)
}

// ToolCatalog 在启动时完成注册并保持只读；注册不等于向某次模型调用暴露。
type ToolCatalog struct {
	entries []catalogEntry
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
	return &ToolCatalog{entries: entries}, nil
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
	if hasDuplicate(registration.AllowedRoles) || hasDuplicate(registration.AllowedTaskTypes) ||
		hasDuplicate(registration.AllowedDataRoles) || hasDuplicate(registration.AllowedSafetyModes) ||
		hasDuplicate(registration.RequiredCapabilities) ||
		hasDuplicate(registration.RequiredDependencies) {
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
	return &scopeGuardedTool{inner: invokable, entry: entry}, nil
}

type scopeGuardedTool struct {
	inner tool.InvokableTool
	entry catalogEntry
}

func (t *scopeGuardedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *scopeGuardedTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	scope, ok := TaskScopeFromContext(ctx)
	if !ok {
		return "", ErrTaskScopeRequired
	}
	if !t.entry.authorized(scope) {
		return "", fmt.Errorf("%w: %s", ErrToolNotAllowed, t.entry.name)
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
