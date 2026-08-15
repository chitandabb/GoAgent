package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
)

// mustProfileBoundDefaultCatalogForTest 构造完整注册的默认 Catalog 并验证
// 它已经绑定部署级 Profile（NewDefaultToolCatalog 内部完成）。
func mustProfileBoundDefaultCatalogForTest(t *testing.T) *ToolCatalog {
	t.Helper()
	catalog := mustFullyConfiguredDefaultToolCatalog(t)
	if catalog.profile == nil {
		t.Fatal("default catalog was not bound to a deployment profile")
	}
	return catalog
}

func mustToolProfileForTest(t *testing.T, id agentruntime.ToolProfileID, names []string) agentruntime.ToolProfile {
	t.Helper()
	profile, err := agentruntime.NewToolProfile(id, names)
	if err != nil {
		t.Fatalf("NewToolProfile(%s): %v", id, err)
	}
	return profile
}

func TestResolveConversationProfileStableAcrossRunContexts(t *testing.T) {
	catalog := mustProfileBoundDefaultCatalogForTest(t)
	base, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile(conversation): %v", err)
	}
	wantNames := append([]string(nil), base.ModelVisibleNames...)
	wantToolNames := namesOfToolsForTest(t, base.Tools)
	if slices.Contains(wantNames, ToolSkill) || slices.Contains(wantNames, ToolReadSkillReference) {
		t.Fatalf("conversation profile must not expose skill or read_skill_reference: %v", wantNames)
	}

	contexts := []struct {
		name  string
		ctx   context.Context
	}{
		{name: "no references"},
		{
			name: "selected case",
			ctx: WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
				t, auth.RoleAnalyst, TaskTypeConversation, nil,
				[]ToolCapability{ToolCapabilityCase, ToolCapabilityKnowledge},
				ToolDependencyExternalCase, ToolDependencyKnowledge,
			)),
		},
		{
			name: "task reference",
			ctx: WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
				t, auth.RoleAnalyst, TaskTypeConversation, nil,
				[]ToolCapability{ToolCapabilityTask, ToolCapabilityKnowledge},
				ToolDependencyKnowledge,
			)),
		},
		{
			name: "attachment",
			ctx: WithTaskScope(context.Background(), mustTaskScopeWithCapabilities(
				t, auth.RoleAnalyst, TaskTypeConversation, nil,
				[]ToolCapability{ToolCapabilityAttachment, ToolCapabilityKnowledge},
				ToolDependencyAttachment, ToolDependencyKnowledge,
			)),
		},
	}
	for _, current := range contexts {
		t.Run(current.name, func(t *testing.T) {
			if current.ctx == nil {
				current.ctx = context.Background()
			}
			resolved, err := catalog.ResolveProfile(current.ctx, agentruntime.ToolProfileConversation)
			if err != nil {
				t.Fatalf("ResolveProfile: %v", err)
			}
			if !slices.Equal(resolved.ModelVisibleNames, wantNames) {
				t.Fatalf("model visible names = %v, want stable %v", resolved.ModelVisibleNames, wantNames)
			}
			if got := namesOfToolsForTest(t, resolved.Tools); !slices.Equal(got, wantToolNames) {
				t.Fatalf("resolved tool names = %v, want stable %v", got, wantToolNames)
			}
		})
	}
}

func TestResolveDiagnosisProfileStableAcrossCapabilityScopes(t *testing.T) {
	catalog := mustDiagnosisConfiguredDefaultCatalogForTest(t)
	base, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile(diagnosis): %v", err)
	}
	wantNames := append([]string(nil), base.ModelVisibleNames...)
	wantToolNames := namesOfToolsForTest(t, base.Tools)

	capabilitySets := [][]ToolCapability{
		{ToolCapabilityCase, ToolCapabilityKnowledge},
		{ToolCapabilityCase, ToolCapabilitySQL},
		{ToolCapabilityCase, ToolCapabilityCode},
		{ToolCapabilityCase, ToolCapabilityKnowledge, ToolCapabilitySQL, ToolCapabilityCode},
	}
	for _, capabilities := range capabilitySets {
		scope := mustTaskScopeWithCapabilities(t, auth.RoleAnalyst, TaskTypeDiagnosis,
			[]ScopedDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}}, capabilities,
			ToolDependencyExternalCase, ToolDependencySQLServer, ToolDependencyGitHubMCP, ToolDependencyKnowledge)
		resolved, err := catalog.ResolveProfile(WithTaskScope(context.Background(), scope), agentruntime.ToolProfileDiagnosis)
		if err != nil {
			t.Fatalf("ResolveProfile(diagnosis, %v): %v", capabilities, err)
		}
		if !slices.Equal(resolved.ModelVisibleNames, wantNames) {
			t.Fatalf("capabilities %v changed model visible names: %v vs %v", capabilities, resolved.ModelVisibleNames, wantNames)
		}
		if got := namesOfToolsForTest(t, resolved.Tools); !slices.Equal(got, wantToolNames) {
			t.Fatalf("capabilities %v changed resolved tools: %v vs %v", capabilities, got, wantToolNames)
		}
	}
}

func TestResolveProfileRejectsUnknownProfileID(t *testing.T) {
	catalog := mustProfileBoundDefaultCatalogForTest(t)
	if _, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileID("unknown-profile")); err == nil {
		t.Fatal("ResolveProfile accepted an unknown Profile ID")
	}
}

func TestBindProfileRejectsReferenceToUnregisteredTool(t *testing.T) {
	registered := newNamedToolForTest(t, "test_registered_tool")
	other := newNamedToolForTest(t, "test_other_registered")
	catalog, err := NewToolCatalog(context.Background(),
		ToolRegistration{
			Tool: registered, FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
		},
		ToolRegistration{
			Tool: other, FailurePolicy: resilience.PolicyBestEffort,
			AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
		},
	)
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	// 其余条件合法：两个名字都已注册，只有 "ghost_tool" 未注册。
	profile := mustToolProfileForTest(t, agentruntime.ToolProfileDiagnosis,
		[]string{"test_registered_tool", "test_other_registered", "ghost_tool"})
	err = catalog.BindProfile(profile, []string{ToolSkill})
	if err == nil {
		t.Fatal("BindProfile accepted a Profile referencing an unregistered Tool")
	}
	if !strings.Contains(err.Error(), string(agentruntime.ToolProfileDiagnosis)) ||
		!strings.Contains(err.Error(), "ghost_tool") {
		t.Fatalf("BindProfile error must name the Profile ID and the unregistered Tool: %v", err)
	}
}

func TestBindProfileRejectsUndeclaredMiddlewareOwnedName(t *testing.T) {
	registered := newNamedToolForTest(t, "test_registered_tool")
	catalog, err := NewToolCatalog(context.Background(), ToolRegistration{
		Tool: registered, FailurePolicy: resilience.PolicyBestEffort,
		AllowedRoles: []auth.Role{auth.RoleAnalyst}, AllowedTaskTypes: []TaskType{TaskTypeDiagnosis},
	})
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	// Profile 内容合法（已注册工具），只有 middlewareOwned 名单非法。
	profile := mustToolProfileForTest(t, agentruntime.ToolProfileDiagnosis, []string{"test_registered_tool"})
	err = catalog.BindProfile(profile, []string{ToolSkill, "some_future_middleware_tool"})
	if err == nil {
		t.Fatal("BindProfile accepted an unknown Middleware-owned Tool name")
	}
	if !strings.Contains(err.Error(), "some_future_middleware_tool") {
		t.Fatalf("BindProfile error must name the offending Middleware-owned Tool: %v", err)
	}
}

func TestResolveProfileReturnsDefensiveCopies(t *testing.T) {
	catalog := mustProfileBoundDefaultCatalogForTest(t)
	first, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	second, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(first.Tools) > 0 {
		first.Tools[0] = nil
	}
	if len(first.ModelVisibleNames) > 0 {
		first.ModelVisibleNames[0] = "mutated"
	}
	if len(second.Tools) > 0 && second.Tools[0] == nil {
		t.Fatal("ResolvedProfile.Tools leaked mutation to the next resolution")
	}
	if slices.Contains(second.ModelVisibleNames, "mutated") {
		t.Fatal("ResolvedProfile.ModelVisibleNames leaked mutation to the next resolution")
	}
}

func TestDiagnosisProfileSkillOwnership(t *testing.T) {
	catalog := mustDiagnosisConfiguredDefaultCatalogForTest(t)
	diagnosis, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile(diagnosis): %v", err)
	}
	skillCount := 0
	for _, name := range diagnosis.ModelVisibleNames {
		if name == ToolSkill {
			skillCount++
		}
	}
	if skillCount != 1 {
		t.Fatalf("diagnosis model visible names contain skill %d times, want exactly 1: %v", skillCount, diagnosis.ModelVisibleNames)
	}
	if names := namesOfToolsForTest(t, diagnosis.Tools); slices.Contains(names, ToolSkill) {
		t.Fatalf("catalog resolved a fake skill Tool: %v", names)
	}
	if !slices.Contains(diagnosis.ModelVisibleNames, ToolReadSkillReference) {
		t.Fatalf("read_skill_reference missing from diagnosis profile: %v", diagnosis.ModelVisibleNames)
	}

	conversation, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err == nil {
		t.Fatal("diagnosis catalog must not resolve the conversation profile")
	}
	_ = conversation
}

func namesOfToolsForTest(t *testing.T, tools []tool.BaseTool) []string {
	t.Helper()
	return toolNamesForTest(t, tools)
}
