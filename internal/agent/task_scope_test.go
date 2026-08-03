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
	capabilities := []ToolCapability{ToolCapabilityCase, ToolCapabilitySQL}
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources: dataSources, AllowedCapabilities: capabilities, AvailableDependencies: dependencies,
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}

	dataSources[0].Role = DataSourceRoleProductReplica
	dependencies[0] = ToolDependencyGitHubMCP
	capabilities[1] = ToolCapabilityCode
	returned := scope.DataSources()
	returned[0].Role = DataSourceRoleCaseSource

	if scope.DataSources()[0].Role != DataSourceRoleProduction {
		t.Fatalf("scope data sources were mutated: %+v", scope.DataSources())
	}
	if !scope.DependencyAvailable(ToolDependencySQLServer) || scope.DependencyAvailable(ToolDependencyGitHubMCP) {
		t.Fatalf("scope dependencies were mutated")
	}
	if !scope.CapabilityAllowed(ToolCapabilitySQL) || scope.CapabilityAllowed(ToolCapabilityCode) {
		t.Fatalf("scope capabilities were mutated")
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
		{name: "missing user", config: TaskScopeConfig{Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilityCase}}},
		{name: "invalid role", config: TaskScopeConfig{UserID: uuid.New(), Role: "owner", TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilityCase}}},
		{name: "diagnosis without source", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, AllowedCapabilities: []ToolCapability{ToolCapabilityCase}}},
		{name: "missing capability", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}}},
		{name: "diagnosis without case capability", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilitySQL}}},
		{name: "duplicate capability", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilityCase, ToolCapabilityCase}}},
		{name: "knowledge with diagnosis source", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilityKnowledge}}},
		{name: "knowledge with code capability", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeKnowledge, AllowedCapabilities: []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityCode}}},
		{name: "production bounded lab", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAdmin, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyBoundedLab}}, AllowedCapabilities: []ToolCapability{ToolCapabilityCase}}},
		{name: "duplicate dependency", config: TaskScopeConfig{UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis, DataSources: []ScopedDataSource{validSource}, AllowedCapabilities: []ToolCapability{ToolCapabilityCase}, AvailableDependencies: []ToolDependency{ToolDependencyExternalCase, ToolDependencyExternalCase}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTaskScope(tt.config); err == nil {
				t.Fatal("NewTaskScope accepted invalid config")
			}
		})
	}
}
