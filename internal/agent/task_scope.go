package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

type TaskType string

const (
	TaskTypeDiagnosis TaskType = "diagnosis"
	TaskTypeKnowledge TaskType = "knowledge_qa"
)

func (t TaskType) Valid() bool {
	return t == TaskTypeDiagnosis || t == TaskTypeKnowledge
}

type DataSourceRole string

const (
	DataSourceRoleCaseSource     DataSourceRole = "case_source"
	DataSourceRoleProduction     DataSourceRole = "production"
	DataSourceRoleProductReplica DataSourceRole = "product_replica"
)

func (r DataSourceRole) Valid() bool {
	switch r {
	case DataSourceRoleCaseSource, DataSourceRoleProduction, DataSourceRoleProductReplica:
		return true
	}
	return false
}

type DataSourceSafetyMode string

const (
	DataSourceSafetyReadOnly   DataSourceSafetyMode = "read_only"
	DataSourceSafetyBoundedLab DataSourceSafetyMode = "bounded_lab"
)

func (m DataSourceSafetyMode) Valid() bool {
	return m == DataSourceSafetyReadOnly || m == DataSourceSafetyBoundedLab
}

type ToolDependency string

const (
	ToolDependencyExternalCase ToolDependency = "external_case"
	ToolDependencyGitHubMCP    ToolDependency = "github_mcp"
	ToolDependencySQLServer    ToolDependency = "sql_server"
	ToolDependencyKnowledge    ToolDependency = "knowledge"
	ToolDependencyAttachment   ToolDependency = "attachment"
	ToolDependencyWebSearch    ToolDependency = "web_search"
)

func (d ToolDependency) Valid() bool {
	switch d {
	case ToolDependencyExternalCase, ToolDependencyGitHubMCP, ToolDependencySQLServer,
		ToolDependencyKnowledge, ToolDependencyAttachment, ToolDependencyWebSearch:
		return true
	}
	return false
}

// ScopedDataSource 是一次 Agent Run 已获授权的数据源，不包含连接信息或凭证。
type ScopedDataSource struct {
	ID         uuid.UUID
	Role       DataSourceRole
	SafetyMode DataSourceSafetyMode
}

func (s ScopedDataSource) Validate() error {
	if s.ID == uuid.Nil {
		return errors.New("data source id is required")
	}
	if !s.Role.Valid() {
		return fmt.Errorf("invalid data source role %q", s.Role)
	}
	if !s.SafetyMode.Valid() {
		return fmt.Errorf("invalid data source safety mode %q", s.SafetyMode)
	}
	if s.Role != DataSourceRoleProductReplica && s.SafetyMode == DataSourceSafetyBoundedLab {
		return fmt.Errorf("data source role %q cannot use bounded_lab", s.Role)
	}
	return nil
}

type TaskScopeConfig struct {
	UserID                uuid.UUID
	Role                  auth.Role
	TaskType              TaskType
	DataSources           []ScopedDataSource
	AvailableDependencies []ToolDependency
}

// TaskScope 只能通过 NewTaskScope 创建，内部切片不向调用方暴露，避免并发 Run 共享可变授权状态。
type TaskScope struct {
	userID                uuid.UUID
	role                  auth.Role
	taskType              TaskType
	dataSources           []ScopedDataSource
	availableDependencies []ToolDependency
}

func NewTaskScope(cfg TaskScopeConfig) (TaskScope, error) {
	if cfg.UserID == uuid.Nil {
		return TaskScope{}, errors.New("task scope user id is required")
	}
	if !cfg.Role.Valid() {
		return TaskScope{}, fmt.Errorf("task scope role %q is invalid", cfg.Role)
	}
	if !cfg.TaskType.Valid() {
		return TaskScope{}, fmt.Errorf("task scope type %q is invalid", cfg.TaskType)
	}
	if cfg.TaskType == TaskTypeDiagnosis && len(cfg.DataSources) == 0 {
		return TaskScope{}, errors.New("diagnosis task scope requires at least one data source")
	}
	if cfg.TaskType == TaskTypeKnowledge && len(cfg.DataSources) != 0 {
		return TaskScope{}, errors.New("knowledge task scope cannot bind diagnosis data sources")
	}

	dataSources := append([]ScopedDataSource(nil), cfg.DataSources...)
	seenDataSources := make(map[uuid.UUID]struct{}, len(dataSources))
	for _, dataSource := range dataSources {
		if err := dataSource.Validate(); err != nil {
			return TaskScope{}, fmt.Errorf("invalid task scope data source: %w", err)
		}
		if _, exists := seenDataSources[dataSource.ID]; exists {
			return TaskScope{}, fmt.Errorf("duplicate task scope data source %s", dataSource.ID)
		}
		seenDataSources[dataSource.ID] = struct{}{}
	}

	dependencies := append([]ToolDependency(nil), cfg.AvailableDependencies...)
	seenDependencies := make(map[ToolDependency]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if !dependency.Valid() {
			return TaskScope{}, fmt.Errorf("invalid task scope dependency %q", dependency)
		}
		if _, exists := seenDependencies[dependency]; exists {
			return TaskScope{}, fmt.Errorf("duplicate task scope dependency %q", dependency)
		}
		seenDependencies[dependency] = struct{}{}
	}

	return TaskScope{
		userID: cfg.UserID, role: cfg.Role, taskType: cfg.TaskType,
		dataSources: dataSources, availableDependencies: dependencies,
	}, nil
}

func (s TaskScope) UserID() uuid.UUID { return s.userID }

func (s TaskScope) Role() auth.Role { return s.role }

func (s TaskScope) TaskType() TaskType { return s.taskType }

func (s TaskScope) DataSources() []ScopedDataSource {
	return append([]ScopedDataSource(nil), s.dataSources...)
}

func (s TaskScope) DependencyAvailable(dependency ToolDependency) bool {
	return slices.Contains(s.availableDependencies, dependency)
}

func (s TaskScope) matchesDataSource(roles []DataSourceRole, safetyModes []DataSourceSafetyMode) bool {
	if len(roles) == 0 && len(safetyModes) == 0 {
		return true
	}
	for _, dataSource := range s.dataSources {
		roleAllowed := len(roles) == 0 || slices.Contains(roles, dataSource.Role)
		safetyAllowed := len(safetyModes) == 0 || slices.Contains(safetyModes, dataSource.SafetyMode)
		if roleAllowed && safetyAllowed {
			return true
		}
	}
	return false
}

type taskScopeContextKey struct{}

// WithTaskScope 把不可变授权快照绑定到本次 Run 的 Context，不修改共享 Agent 实例。
func WithTaskScope(ctx context.Context, scope TaskScope) context.Context {
	return context.WithValue(ctx, taskScopeContextKey{}, scope)
}

func TaskScopeFromContext(ctx context.Context) (TaskScope, bool) {
	scope, ok := ctx.Value(taskScopeContextKey{}).(TaskScope)
	return scope, ok
}
