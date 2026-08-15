package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
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
			// 引用变化只改变 RunAccess（case Grant），不改变 Schema。
			name: "selected case",
			ctx: agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
				t, uuid.New(),
				[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead},
				agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{uuid.New()}},
			)),
		},
		{
			name: "task reference",
			ctx: agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
				t, uuid.New(),
				[]agentruntime.Permission{agentruntime.PermissionTaskRead, agentruntime.PermissionKnowledgeRead},
				agentruntime.ResourceGrantsConfig{},
			)),
		},
		{
			name: "attachment",
			ctx: agentruntime.WithRunAccess(context.Background(), mustConversationTestRunAccess(
				t, uuid.New(),
				[]agentruntime.Permission{agentruntime.PermissionAttachmentRead, agentruntime.PermissionKnowledgeRead},
				agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{uuid.New()}},
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

func TestResolveDiagnosisProfileStableAcrossPermissionSets(t *testing.T) {
	catalog := mustDiagnosisConfiguredDefaultCatalogForTest(t)
	base, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile(diagnosis): %v", err)
	}
	wantNames := append([]string(nil), base.ModelVisibleNames...)
	wantToolNames := namesOfToolsForTest(t, base.Tools)

	permissionSets := [][]agentruntime.Permission{
		{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead},
		{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead},
		{agentruntime.PermissionCaseRead, agentruntime.PermissionCodeRead},
		{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
			agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead},
	}
	for _, permissions := range permissionSets {
		ctx := agentruntime.WithRunAccess(context.Background(), mustDiagnosisTestRunAccess(
			t, uuid.New(), permissions, agentruntime.ResourceGrantsConfig{},
		))
		resolved, err := catalog.ResolveProfile(ctx, agentruntime.ToolProfileDiagnosis)
		if err != nil {
			t.Fatalf("ResolveProfile(diagnosis, %v): %v", permissions, err)
		}
		if !slices.Equal(resolved.ModelVisibleNames, wantNames) {
			t.Fatalf("permissions %v changed model visible names: %v vs %v", permissions, resolved.ModelVisibleNames, wantNames)
		}
		if got := namesOfToolsForTest(t, resolved.Tools); !slices.Equal(got, wantToolNames) {
			t.Fatalf("permissions %v changed resolved tools: %v vs %v", permissions, got, wantToolNames)
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
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead},
		},
		ToolRegistration{
			Tool: other, FailurePolicy: resilience.PolicyBestEffort,
			RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead},
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
		RequiredPermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead},
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
