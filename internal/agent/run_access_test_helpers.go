package agent

import (
	"context"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

// mustDiagnosisTestRunAccess 构造测试用诊断 RunAccess：授权事实直接来自冻结
// Policy 与 ceiling，不再经过旧 TaskScope/能力声明。
func mustDiagnosisTestRunAccess(
	t *testing.T,
	userID uuid.UUID,
	permissions []agentruntime.Permission,
	grantsConfig agentruntime.ResourceGrantsConfig,
) agentruntime.RunAccess {
	t.Helper()
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.NewInvestigationPolicy(diagnosis.InvestigationPolicySchemaVersion, permissionSet, grants)
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.DeriveDiagnosisRunAccess(
		policy,
		agentruntime.Actor{UserID: userID, Role: auth.RoleAnalyst},
		agentruntime.AccessCeiling{Permissions: permissionSet, Grants: grants},
	)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

// mustConversationTestRunAccess 构造测试用会话 RunAccess。
func mustConversationTestRunAccess(
	t *testing.T,
	userID uuid.UUID,
	permissions []agentruntime.Permission,
	grantsConfig agentruntime.ResourceGrantsConfig,
) agentruntime.RunAccess {
	t.Helper()
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.NewConversationRunAccess(
		agentruntime.Actor{UserID: userID, Role: auth.RoleAnalyst},
		permissionSet,
		grants,
	)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func withTestRunAccess(ctx context.Context, access agentruntime.RunAccess) context.Context {
	return agentruntime.WithRunAccess(ctx, access)
}
