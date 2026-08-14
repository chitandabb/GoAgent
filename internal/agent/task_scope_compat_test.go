package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

func TestRunAccessFromTaskScopeMapsEveryCapability(t *testing.T) {
	cases := []struct {
		capability ToolCapability
		permission agentruntime.Permission
	}{
		{ToolCapabilityCase, agentruntime.PermissionCaseRead},
		{ToolCapabilityCode, agentruntime.PermissionCodeRead},
		{ToolCapabilitySQL, agentruntime.PermissionSQLRead},
		{ToolCapabilityKnowledge, agentruntime.PermissionKnowledgeRead},
		{ToolCapabilityAttachment, agentruntime.PermissionAttachmentRead},
		{ToolCapabilityWebSearch, agentruntime.PermissionWebRead},
		{ToolCapabilityTask, agentruntime.PermissionTaskRead},
		{ToolCapabilityMemory, agentruntime.PermissionMemoryRead},
	}
	for _, current := range cases {
		t.Run(string(current.capability), func(t *testing.T) {
			capabilities := []ToolCapability{ToolCapabilityCase, current.capability}
			if current.capability == ToolCapabilityCase {
				capabilities = []ToolCapability{ToolCapabilityCase}
			}
			scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis,
				[]ScopedDataSource{{
					ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
				}},
				capabilities)
			access, err := runAccessFromTaskScope(scope)
			if err != nil {
				t.Fatalf("runAccessFromTaskScope: %v", err)
			}
			want := []agentruntime.Permission{agentruntime.PermissionCaseRead, current.permission}
			if current.capability == ToolCapabilityCase {
				want = []agentruntime.Permission{agentruntime.PermissionCaseRead}
			}
			slices.Sort(want)
			if got := access.Permissions().Values(); !slices.Equal(got, want) {
				t.Fatalf("permissions = %v, want %v", got, want)
			}
		})
	}
}

func TestRunAccessFromTaskScopeDiagnosisDerivesFromPolicy(t *testing.T) {
	dataSourceID := uuid.New()
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis,
		[]ScopedDataSource{{
			ID: dataSourceID, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
		[]ToolCapability{ToolCapabilityCase, ToolCapabilityKnowledge})
	access, err := runAccessFromTaskScope(scope)
	if err != nil {
		t.Fatalf("runAccessFromTaskScope: %v", err)
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindDiagnosis {
		t.Fatalf("runtime kind = %q, want diagnosis", access.RuntimeKind())
	}
	if !access.Allows(agentruntime.PermissionCaseRead) || !access.Allows(agentruntime.PermissionKnowledgeRead) {
		t.Fatalf("permissions = %v", access.Permissions().Values())
	}
	if !access.Grants().AllowsDataSource(dataSourceID) {
		t.Fatalf("grants = %+v, missing data source %s", access.Grants(), dataSourceID)
	}
	if access.Allows(agentruntime.PermissionDiagnosisCreate) {
		t.Fatal("diagnosis run access must never include diagnosis.create")
	}
}

func TestRunAccessFromTaskScopeConversationGetsDiagnosisCreateWithCase(t *testing.T) {
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeConversation, nil,
		[]ToolCapability{ToolCapabilityCase})
	access, err := runAccessFromTaskScope(scope)
	if err != nil {
		t.Fatalf("runAccessFromTaskScope: %v", err)
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindConversation {
		t.Fatalf("runtime kind = %q, want conversation", access.RuntimeKind())
	}
	if !access.Allows(agentruntime.PermissionCaseRead) || !access.Allows(agentruntime.PermissionDiagnosisCreate) {
		t.Fatalf("permissions = %v", access.Permissions().Values())
	}
}

func TestRunAccessFromTaskScopeConversationWithoutCaseHasNoDiagnosisCreate(t *testing.T) {
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeConversation, nil,
		[]ToolCapability{ToolCapabilityKnowledge})
	access, err := runAccessFromTaskScope(scope)
	if err != nil {
		t.Fatalf("runAccessFromTaskScope: %v", err)
	}
	if access.Allows(agentruntime.PermissionDiagnosisCreate) {
		t.Fatal("conversation without case capability must not create diagnosis tasks")
	}
}

func TestRunAccessFromTaskScopeKnowledgeMapsToConversationRuntime(t *testing.T) {
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeKnowledge, nil,
		[]ToolCapability{ToolCapabilityKnowledge})
	access, err := runAccessFromTaskScope(scope)
	if err != nil {
		t.Fatalf("runAccessFromTaskScope: %v", err)
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindConversation {
		t.Fatalf("runtime kind = %q, want conversation for legacy knowledge tasks", access.RuntimeKind())
	}
	if !access.Allows(agentruntime.PermissionKnowledgeRead) {
		t.Fatalf("permissions = %v", access.Permissions().Values())
	}
}

func TestRunAccessFromTaskScopeRejectsInvalidScope(t *testing.T) {
	if _, err := runAccessFromTaskScope(TaskScope{}); err == nil {
		t.Fatal("runAccessFromTaskScope accepted an invalid zero scope")
	}
}

func TestWithTaskScopeWritesBothTaskScopeAndRunAccess(t *testing.T) {
	scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeConversation, nil,
		[]ToolCapability{ToolCapabilityCase})
	ctx := WithTaskScope(context.Background(), scope)
	if _, ok := TaskScopeFromContext(ctx); !ok {
		t.Fatal("TaskScope was not preserved")
	}
	access, ok := agentruntime.RunAccessFromContext(ctx)
	if !ok {
		t.Fatal("RunAccess was not derived from TaskScope")
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindConversation ||
		!access.Allows(agentruntime.PermissionDiagnosisCreate) {
		t.Fatalf("RunAccess = %+v", access)
	}
}

func TestWithTaskScopeSkipsInvalidScopeForRunAccess(t *testing.T) {
	ctx := WithTaskScope(context.Background(), TaskScope{})
	if _, ok := TaskScopeFromContext(ctx); !ok {
		t.Fatal("TaskScope should still be written for legacy readers")
	}
	if _, ok := agentruntime.RunAccessFromContext(ctx); ok {
		t.Fatal("invalid TaskScope must not produce a RunAccess")
	}
}
