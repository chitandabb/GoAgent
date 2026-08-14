package agentruntime

import (
	"context"
	"testing"

	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

func TestDeriveDiagnosisRunAccessIntersectsFrozenPolicyWithCurrentCeiling(t *testing.T) {
	policyDataSourceID := uuid.New()
	policyAttachmentID := uuid.New()
	policy, err := NewInvestigationPolicy(1,
		mustPermissionSet(t, PermissionKnowledgeRead, PermissionSQLRead, PermissionCodeRead),
		mustResourceGrants(t, ResourceGrantsConfig{
			DataSourceIDs: []uuid.UUID{policyDataSourceID},
			AttachmentIDs: []uuid.UUID{policyAttachmentID},
			Repositories:  []string{"chitandabb/mesguard-csharp-demo"},
		}),
	)
	if err != nil {
		t.Fatalf("NewInvestigationPolicy: %v", err)
	}
	ceiling := AccessCeiling{
		Permissions: mustPermissionSet(t, PermissionKnowledgeRead, PermissionSQLRead, PermissionWebRead),
		Grants: mustResourceGrants(t, ResourceGrantsConfig{
			DataSourceIDs: []uuid.UUID{policyDataSourceID, uuid.New()},
			AttachmentIDs: []uuid.UUID{uuid.New()},
			Repositories:  []string{"chitandabb/mesguard-csharp-demo", "chitandabb/GoChat"},
		}),
	}
	actor := Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}

	access, err := DeriveDiagnosisRunAccess(policy, actor, ceiling)
	if err != nil {
		t.Fatalf("DeriveDiagnosisRunAccess: %v", err)
	}
	if access.RuntimeKind() != RuntimeKindDiagnosis || access.Actor() != actor {
		t.Fatalf("run identity = kind %q actor %+v", access.RuntimeKind(), access.Actor())
	}
	if !access.Allows(PermissionKnowledgeRead) || !access.Allows(PermissionSQLRead) {
		t.Fatalf("expected intersected permissions, got %v", access.Permissions().Values())
	}
	if access.Allows(PermissionCodeRead) || access.Allows(PermissionWebRead) {
		t.Fatalf("run access expanded outside the intersection: %v", access.Permissions().Values())
	}
	grants := access.Grants()
	if !grants.AllowsDataSource(policyDataSourceID) || grants.AllowsAttachment(policyAttachmentID) ||
		!grants.AllowsRepository("chitandabb/mesguard-csharp-demo") || grants.AllowsRepository("chitandabb/GoChat") {
		t.Fatalf("run grants are not the policy/ceiling intersection: %+v", grants)
	}
}

func TestAccessValuesAreImmutableCopies(t *testing.T) {
	dataSourceID := uuid.New()
	inputPermissions := []Permission{PermissionCaseRead, PermissionSQLRead}
	permissions, err := NewPermissionSet(inputPermissions...)
	if err != nil {
		t.Fatalf("NewPermissionSet: %v", err)
	}
	inputDataSources := []uuid.UUID{dataSourceID}
	grants, err := NewResourceGrants(ResourceGrantsConfig{DataSourceIDs: inputDataSources})
	if err != nil {
		t.Fatalf("NewResourceGrants: %v", err)
	}

	inputPermissions[0] = PermissionWebRead
	inputDataSources[0] = uuid.New()
	returnedPermissions := permissions.Values()
	returnedPermissions[0] = PermissionWebRead
	returnedDataSources := grants.DataSourceIDs()
	returnedDataSources[0] = uuid.New()

	if !permissions.Has(PermissionCaseRead) || permissions.Has(PermissionWebRead) {
		t.Fatalf("permission set was mutated: %v", permissions.Values())
	}
	if !grants.AllowsDataSource(dataSourceID) {
		t.Fatalf("resource grants were mutated: %v", grants.DataSourceIDs())
	}
}

func TestAccessContractsRejectInvalidOrDuplicateValues(t *testing.T) {
	if _, err := NewPermissionSet(PermissionSQLRead, PermissionSQLRead); err == nil {
		t.Fatal("NewPermissionSet accepted a duplicate permission")
	}
	if _, err := NewPermissionSet(Permission("sql.write")); err == nil {
		t.Fatal("NewPermissionSet accepted an unknown permission")
	}
	if _, err := NewResourceGrants(ResourceGrantsConfig{DataSourceIDs: []uuid.UUID{uuid.Nil}}); err == nil {
		t.Fatal("NewResourceGrants accepted a nil data source id")
	}
	if _, err := NewResourceGrants(ResourceGrantsConfig{Repositories: []string{"owner/repo", "owner/repo"}}); err == nil {
		t.Fatal("NewResourceGrants accepted a duplicate repository")
	}
	if _, err := NewInvestigationPolicy(0, mustPermissionSet(t, PermissionCaseRead), ResourceGrants{}); err == nil {
		t.Fatal("NewInvestigationPolicy accepted schema version zero")
	}
	validPolicy, err := NewInvestigationPolicy(1, mustPermissionSet(t, PermissionCaseRead), ResourceGrants{})
	if err != nil {
		t.Fatalf("NewInvestigationPolicy: %v", err)
	}
	if _, err := DeriveDiagnosisRunAccess(validPolicy, Actor{}, AccessCeiling{
		Permissions: mustPermissionSet(t, PermissionCaseRead),
	}); err == nil {
		t.Fatal("DeriveDiagnosisRunAccess accepted an invalid actor")
	}
}

func mustPermissionSet(t *testing.T, values ...Permission) PermissionSet {
	t.Helper()
	set, err := NewPermissionSet(values...)
	if err != nil {
		t.Fatalf("NewPermissionSet: %v", err)
	}
	return set
}

func mustResourceGrants(t *testing.T, config ResourceGrantsConfig) ResourceGrants {
	t.Helper()
	grants, err := NewResourceGrants(config)
	if err != nil {
		t.Fatalf("NewResourceGrants: %v", err)
	}
	return grants
}

func TestNewConversationRunAccessRejectsInvalidInputs(t *testing.T) {
	actor := Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}
	permissions := mustPermissionSet(t, PermissionKnowledgeRead)
	grants := mustResourceGrants(t, ResourceGrantsConfig{})
	if _, err := NewConversationRunAccess(Actor{}, permissions, grants); err == nil {
		t.Fatal("NewConversationRunAccess accepted an invalid actor")
	}
	invalidPermissions := PermissionSet{values: []Permission{"sql.write"}}
	if _, err := NewConversationRunAccess(actor, invalidPermissions, grants); err == nil {
		t.Fatal("NewConversationRunAccess accepted an invalid permission set")
	}
	invalidGrants := ResourceGrants{dataSourceIDs: []uuid.UUID{uuid.Nil}}
	if _, err := NewConversationRunAccess(actor, permissions, invalidGrants); err == nil {
		t.Fatal("NewConversationRunAccess accepted invalid resource grants")
	}
}

func TestNewConversationRunAccessFixesRuntimeKindAndCopiesInputs(t *testing.T) {
	actor := Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}
	permissions := mustPermissionSet(t, PermissionCaseRead, PermissionMemoryRead)
	grants := mustResourceGrants(t, ResourceGrantsConfig{})
	access, err := NewConversationRunAccess(actor, permissions, grants)
	if err != nil {
		t.Fatalf("NewConversationRunAccess: %v", err)
	}
	if access.RuntimeKind() != RuntimeKindConversation {
		t.Fatalf("runtime kind = %q, want conversation", access.RuntimeKind())
	}
	if !access.Allows(PermissionCaseRead) || !access.Allows(PermissionMemoryRead) {
		t.Fatalf("conversation permissions = %v", access.Permissions().Values())
	}
	permissions.values[0] = PermissionWebRead
	if !access.Allows(PermissionCaseRead) || access.Allows(PermissionWebRead) {
		t.Fatal("RunAccess leaked input permission mutations")
	}
}

func TestRunAccessValidate(t *testing.T) {
	actor := Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}
	access, err := NewConversationRunAccess(
		actor, mustPermissionSet(t, PermissionKnowledgeRead), mustResourceGrants(t, ResourceGrantsConfig{}),
	)
	if err != nil {
		t.Fatalf("NewConversationRunAccess: %v", err)
	}
	if err := access.Validate(); err != nil {
		t.Fatalf("valid RunAccess.Validate() = %v", err)
	}
	invalid := RunAccess{
		actor: actor, runtimeKind: RuntimeKindConversation,
		permissions: PermissionSet{values: []Permission{"sql.write"}},
		grants:      mustResourceGrants(t, ResourceGrantsConfig{}),
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid RunAccess.Validate() accepted an invalid permission set")
	}
}

func TestRunAccessContextRoundTripAndFailClosed(t *testing.T) {
	if _, ok := RunAccessFromContext(context.Background()); ok {
		t.Fatal("missing RunAccess reported as present")
	}
	actor := Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}
	access, err := NewConversationRunAccess(
		actor, mustPermissionSet(t, PermissionKnowledgeRead), mustResourceGrants(t, ResourceGrantsConfig{}),
	)
	if err != nil {
		t.Fatalf("NewConversationRunAccess: %v", err)
	}
	bound := WithRunAccess(context.Background(), access)
	got, ok := RunAccessFromContext(bound)
	if !ok || got.Actor() != actor || !got.Allows(PermissionKnowledgeRead) {
		t.Fatalf("round trip RunAccess = %+v, %v", got, ok)
	}
	invalidBound := WithRunAccess(context.Background(), RunAccess{
		runtimeKind: RuntimeKindConversation,
		permissions: PermissionSet{values: []Permission{"sql.write"}},
	})
	if _, ok := RunAccessFromContext(invalidBound); ok {
		t.Fatal("invalid RunAccess was reported as present")
	}
}
