package agent

import (
	"context"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

func TestNewTaskScopeCopiesAuthorizationState(t *testing.T) {
	dataSourceID := uuid.New()
	dataSources := []ScopedDataSource{{
		ID: dataSourceID, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly,
	}}
	dependencies := []ToolDependency{ToolDependencySQLServer}
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources: dataSources, AvailableDependencies: dependencies,
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}

	dataSources[0].Role = DataSourceRoleProductReplica
	dependencies[0] = ToolDependencyGitHubMCP
	returned := scope.DataSources()
	returned[0].Role = DataSourceRoleCaseSource

	if scope.DataSources()[0].Role != DataSourceRoleProduction {
		t.Fatalf("scope data sources were mutated: %+v", scope.DataSources())
	}
	if !scope.DependencyAvailable(ToolDependencySQLServer) || scope.DependencyAvailable(ToolDependencyGitHubMCP) {
		t.Fatalf("scope dependencies were mutated")
	}
	ctx := WithTaskScope(context.Background(), scope)
	got, ok := TaskScopeFromContext(ctx)
	if !ok || got.UserID() != scope.UserID() || got.Role() != auth.RoleAnalyst {
		t.Fatalf("TaskScopeFromContext() = %+v, %t", got, ok)
	}
}

func TestNewTaskScopeRejectsInvalidBoundaries(t *testing.T) {
	validSource := ScopedDataSource{
		ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
	}
	tests := []struct {
		name   string
		config TaskScopeConfig
	}{
		{name: "missing user", config: TaskScopeConfig{Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}}},
		{name: "invalid role", config: TaskScopeConfig{UserID: uuid.New(), Role: "owner", TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}}},
		{name: "diagnosis without source", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis}},
		{name: "knowledge with diagnosis source", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge, DataSources: []ScopedDataSource{validSource}}},
		{name: "production bounded lab", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAdmin, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyBoundedLab}}}},
		{name: "duplicate dependency", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AvailableDependencies: []ToolDependency{ToolDependencyExternalCase, ToolDependencyExternalCase}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTaskScope(tt.config); err == nil {
				t.Fatal("NewTaskScope accepted invalid config")
			}
		})
	}
}
