package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/google/uuid"
)

func mustDiagnosisPolicyForTest(
	t *testing.T,
	permissions []agentruntime.Permission,
	grantsConfig agentruntime.ResourceGrantsConfig,
) agentruntime.InvestigationPolicy {
	t.Helper()
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.NewInvestigationPolicy(1, permissionSet, grants)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func diagnosisProfileNamesForTest(names ...string) []string {
	return names
}

func diagnosisTestActor() agentruntime.Actor {
	return agentruntime.Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}
}

func TestDiagnosisRunContextDerivesAccessFromFrozenPolicyAndCeiling(t *testing.T) {
	caseID := uuid.New()
	sqlSource := uuid.New()
	attachmentID := uuid.New()
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{
			agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
			agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead,
			agentruntime.PermissionWebRead, agentruntime.PermissionAttachmentRead,
		},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID},
			DataSourceIDs:   []uuid.UUID{sqlSource},
			AttachmentIDs:   []uuid.UUID{attachmentID},
		},
	)
	// ceiling：Profile 实际有 case/knowledge/sql/attachment 工具，没有 GitHub/Web 工具。
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: &policy,
		Actor:  diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(
			ToolReadExternalCase, ToolSearchKnowledge, ToolSearchSchemaCatalog,
			ToolExecuteReadonlyQuery, ToolReadAttachment,
		),
		ExternalCaseID: caseID,
		DataSources: []DiagnosisCeilingDataSource{{
			ID: sqlSource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
		AttachmentIDs: []uuid.UUID{attachmentID},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	access := runContext.Access()
	for _, want := range []agentruntime.Permission{
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionSQLRead, agentruntime.PermissionAttachmentRead,
	} {
		if !access.Allows(want) {
			t.Fatalf("effective access missing %q: %v", want, access.Permissions().Values())
		}
	}
	for _, dropped := range []agentruntime.Permission{agentruntime.PermissionCodeRead, agentruntime.PermissionWebRead} {
		if access.Allows(dropped) {
			t.Fatalf("ceiling must drop %q (no profile Tool)", dropped)
		}
	}
	if !access.Grants().AllowsExternalCase(caseID) || !access.Grants().AllowsDataSource(sqlSource) ||
		!access.Grants().AllowsAttachment(attachmentID) {
		t.Fatalf("effective grants = %v", access.Grants())
	}
	assertRunAccessSubsetOfPolicy(t, access, policy)
}

func TestDiagnosisRunContextLegacyDerivationOnlyKeepsFrozenScopeCapabilities(t *testing.T) {
	caseID := uuid.New()
	readOnlySource := uuid.New()
	labSource := uuid.New()
	attachmentID := uuid.New()
	// 旧任务：Policy=NULL，冻结 request_scope 只有 case/sql/knowledge/web_search
	// 能力（附件由旧链路加入 capability）。ceiling Profile 额外有 GitHub 工具，
	// 但 legacy 派生不能读取新部署配置扩大旧任务权限。
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: nil,
		Actor:  diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(
			ToolReadExternalCase, ToolSearchKnowledge, ToolSearchSchemaCatalog,
			ToolExecuteReadonlyQuery, ToolReadAttachment, "search_code",
		),
		ExternalCaseID: caseID,
		DataSources: []DiagnosisCeilingDataSource{
			{ID: readOnlySource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly},
			{ID: labSource, Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab},
		},
		AttachmentIDs:      []uuid.UUID{attachmentID},
		LegacyCapabilities: []ToolCapability{ToolCapabilityCase, ToolCapabilitySQL, ToolCapabilityKnowledge, ToolCapabilityWebSearch, ToolCapabilityAttachment},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	access := runContext.Access()
	if access.Allows(agentruntime.PermissionCodeRead) {
		t.Fatal("legacy derivation expanded into code.read from the new deployment ceiling")
	}
	for _, want := range []agentruntime.Permission{
		agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead,
		agentruntime.PermissionKnowledgeRead, agentruntime.PermissionAttachmentRead,
	} {
		if !access.Allows(want) {
			t.Fatalf("legacy access missing %q", want)
		}
	}
	// web.read 在旧 scope 中存在，但本 ceiling 没有 Web 工具 → 被收窄。
	if access.Allows(agentruntime.PermissionWebRead) {
		t.Fatal("legacy web.read survived a ceiling without Web Tools")
	}
	if !access.Grants().AllowsExternalCase(caseID) || !access.Grants().AllowsAttachment(attachmentID) ||
		!access.Grants().AllowsDataSource(readOnlySource) {
		t.Fatalf("legacy grants = %v", access.Grants())
	}
	if access.Grants().AllowsDataSource(labSource) {
		t.Fatal("bounded_lab data source must never enter the Grant")
	}
}

func TestDiagnosisRunContextPolicySQLWithoutCeilingSQLTools(t *testing.T) {
	caseID := uuid.New()
	sqlSource := uuid.New()
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID}, DataSourceIDs: []uuid.UUID{sqlSource},
		},
	)
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy:           &policy,
		Actor:            diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(ToolReadExternalCase, ToolSearchKnowledge),
		ExternalCaseID:   caseID,
		DataSources: []DiagnosisCeilingDataSource{{
			ID: sqlSource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	access := runContext.Access()
	if access.Allows(agentruntime.PermissionSQLRead) {
		t.Fatal("sql.read survived a ceiling without SQL Tools")
	}
	if len(access.Grants().DataSourceIDs()) != 0 {
		t.Fatalf("data source grants = %v, want none", access.Grants().DataSourceIDs())
	}
	assertRunAccessSubsetOfPolicy(t, access, policy)
}

func TestDiagnosisRunContextRemovesUnsafeAndUnboundResourceGrants(t *testing.T) {
	caseID := uuid.New()
	readOnlySource := uuid.New()
	labSource := uuid.New()
	boundGone := uuid.New()
	firstAttachment, goneAttachment := uuid.New(), uuid.New()
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{
			agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead, agentruntime.PermissionAttachmentRead,
		},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID},
			DataSourceIDs:   []uuid.UUID{readOnlySource, labSource, boundGone},
			AttachmentIDs:   []uuid.UUID{firstAttachment, goneAttachment},
		},
	)
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: &policy, Actor: diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(
			ToolReadExternalCase, ToolSearchSchemaCatalog, ToolReadAttachment,
		),
		ExternalCaseID: caseID,
		DataSources: []DiagnosisCeilingDataSource{
			{ID: readOnlySource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly},
			// bounded_lab：read_only 之外的安全模式，Grant 必须被移除。
			{ID: labSource, Role: DataSourceRoleProductReplica, SafetyMode: DataSourceSafetyBoundedLab},
			// boundGone：不再是任务绑定/active 数据源，不在 ceiling 输入中。
		},
		AttachmentIDs: []uuid.UUID{firstAttachment}, // goneAttachment 已不再 uploaded
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	grants := runContext.Access().Grants()
	if !grants.AllowsDataSource(readOnlySource) || grants.AllowsDataSource(labSource) || grants.AllowsDataSource(boundGone) {
		t.Fatalf("data source grants = %v", grants.DataSourceIDs())
	}
	if !grants.AllowsAttachment(firstAttachment) || grants.AllowsAttachment(goneAttachment) {
		t.Fatalf("attachment grants = %v", grants.AttachmentIDs())
	}
	assertRunAccessSubsetOfPolicy(t, runContext.Access(), policy)
}

func assertRunAccessSubsetOfPolicy(t *testing.T, access agentruntime.RunAccess, policy agentruntime.InvestigationPolicy) {
	t.Helper()
	for _, permission := range access.Permissions().Values() {
		if !policy.Permissions().Has(permission) {
			t.Fatalf("effective permission %q is not a subset of the frozen policy", permission)
		}
	}
	for _, id := range access.Grants().ExternalCaseIDs() {
		if !policy.Grants().AllowsExternalCase(id) {
			t.Fatalf("effective case grant %s is not in the frozen policy", id)
		}
	}
	for _, id := range access.Grants().DataSourceIDs() {
		if !policy.Grants().AllowsDataSource(id) {
			t.Fatalf("effective data source grant %s is not in the frozen policy", id)
		}
	}
	for _, id := range access.Grants().AttachmentIDs() {
		if !policy.Grants().AllowsAttachment(id) {
			t.Fatalf("effective attachment grant %s is not in the frozen policy", id)
		}
	}
	if len(access.Grants().TaskIDs()) != 0 || len(access.Grants().Repositories()) != 0 {
		t.Fatalf("diagnosis run access fabricated task/repository grants: %v", access.Grants())
	}
}

func TestDiagnosisRunContextRejectsInvalidInputs(t *testing.T) {
	validInput := func() DiagnosisRunContextInput {
		return DiagnosisRunContextInput{
			Policy: mustPointerPolicy(t, mustDiagnosisPolicyForTest(t,
				[]agentruntime.Permission{agentruntime.PermissionCaseRead},
				agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{uuid.New()}},
			)),
			Actor:            diagnosisTestActor(),
			ProfileToolNames: diagnosisProfileNamesForTest(ToolReadExternalCase),
			ExternalCaseID:   uuid.New(),
			DataSources: []DiagnosisCeilingDataSource{{
				ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*DiagnosisRunContextInput)
	}{
		{name: "nil external case", mutate: func(in *DiagnosisRunContextInput) { in.ExternalCaseID = uuid.Nil }},
		{name: "nil actor", mutate: func(in *DiagnosisRunContextInput) { in.Actor = agentruntime.Actor{} }},
		{name: "empty profile", mutate: func(in *DiagnosisRunContextInput) { in.ProfileToolNames = nil }},
		{name: "invalid tool name", mutate: func(in *DiagnosisRunContextInput) { in.ProfileToolNames = []string{"not valid!"} }},
		{name: "invalid data source safety", mutate: func(in *DiagnosisRunContextInput) {
			in.DataSources[0].SafetyMode = DataSourceSafetyMode("unsafe")
		}},
		{name: "nil data source id", mutate: func(in *DiagnosisRunContextInput) { in.DataSources[0].ID = uuid.Nil }},
		{name: "nil attachment", mutate: func(in *DiagnosisRunContextInput) { in.AttachmentIDs = []uuid.UUID{uuid.Nil} }},
		{name: "legacy without capabilities", mutate: func(in *DiagnosisRunContextInput) {
			in.Policy = nil
			in.LegacyCapabilities = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validInput()
			test.mutate(&input)
			if _, err := BuildDiagnosisRunContext(input); err == nil {
				t.Fatal("BuildDiagnosisRunContext accepted invalid input")
			}
		})
	}
}

func mustPointerPolicy(t *testing.T, policy agentruntime.InvestigationPolicy) *agentruntime.InvestigationPolicy {
	t.Helper()
	return &policy
}

func TestDiagnosisTaskContextDeterministicSortedAndInjectionSafe(t *testing.T) {
	caseID := uuid.New()
	firstSource, secondSource := uuid.New(), uuid.New()
	if firstSource.String() > secondSource.String() {
		firstSource, secondSource = secondSource, firstSource
	}
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{
			agentruntime.PermissionSQLRead, agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID},
			DataSourceIDs:   []uuid.UUID{firstSource, secondSource},
		},
	)
	input := DiagnosisRunContextInput{
		Policy: &policy, Actor: diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(
			ToolReadExternalCase, ToolSearchKnowledge, ToolSearchSchemaCatalog,
		),
		ExternalCaseID: caseID,
		DataSources: []DiagnosisCeilingDataSource{
			{ID: secondSource, Role: DataSourceRoleProduction, SafetyMode: DataSourceSafetyReadOnly},
			{ID: firstSource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly},
		},
	}
	first, err := BuildDiagnosisRunContext(input)
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	second, err := BuildDiagnosisRunContext(input)
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	if first.TaskContext() != second.TaskContext() {
		t.Fatalf("task_context is not deterministic:\n%s\n---\n%s", first.TaskContext(), second.TaskContext())
	}
	block := first.TaskContext()
	if !strings.HasPrefix(block, "<task_context>\n") || !strings.HasSuffix(block, "\n</task_context>") {
		t.Fatalf("task_context block shape = %q", block)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(block, "<task_context>\n"), "\n</task_context>")
	if !json.Valid([]byte(inner)) {
		t.Fatalf("task_context inner payload is not valid JSON: %s", inner)
	}
	if strings.Contains(inner, "<") || strings.Contains(inner, ">") {
		t.Fatalf("task_context payload leaked raw tag characters: %s", inner)
	}
	var projection struct {
		PolicySchemaVersion  int      `json:"policySchemaVersion"`
		EffectivePermissions []string `json:"effectivePermissions"`
		ExternalCaseID       string   `json:"externalCaseId"`
		DataSources          []struct {
			ID         string `json:"id"`
			Role       string `json:"role"`
			SafetyMode string `json:"safetyMode"`
		} `json:"dataSources"`
	}
	if err := json.Unmarshal([]byte(inner), &projection); err != nil {
		t.Fatalf("decode task_context: %v", err)
	}
	if projection.PolicySchemaVersion != 1 || projection.ExternalCaseID != caseID.String() {
		t.Fatalf("task_context projection = %+v", projection)
	}
	wantPermissions := []string{"case.read", "knowledge.read", "sql.read"}
	if strings.Join(projection.EffectivePermissions, ",") != strings.Join(wantPermissions, ",") {
		t.Fatalf("effective permissions = %v, want %v", projection.EffectivePermissions, wantPermissions)
	}
	if len(projection.DataSources) != 2 ||
		projection.DataSources[0].ID != firstSource.String() || projection.DataSources[1].ID != secondSource.String() {
		t.Fatalf("data source projection order = %+v", projection.DataSources)
	}
	if projection.DataSources[0].Role != "case_source" || projection.DataSources[1].Role != "production" ||
		projection.DataSources[0].SafetyMode != "read_only" {
		t.Fatalf("data source role/safety projection = %+v", projection.DataSources)
	}
}

func TestDiagnosisTaskContextOmitsRevokedSourcesAndSensitiveFields(t *testing.T) {
	caseID := uuid.New()
	grantedSource := uuid.New()
	revokedSource := uuid.New()
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID},
			DataSourceIDs:   []uuid.UUID{grantedSource, revokedSource},
		},
	)
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: &policy, Actor: diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(ToolReadExternalCase, ToolSearchSchemaCatalog),
		ExternalCaseID:   caseID,
		DataSources: []DiagnosisCeilingDataSource{
			{ID: grantedSource, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly},
			// revokedSource 已 disabled（不在 active ceiling 输入中）
		},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	block := runContext.TaskContext()
	if strings.Contains(block, revokedSource.String()) {
		t.Fatalf("task_context leaked a revoked data source: %s", block)
	}
	if !strings.Contains(block, grantedSource.String()) {
		t.Fatalf("task_context missing the authorized data source: %s", block)
	}
	for _, forbidden := range []string{"password", "host", "endpoint", "credential", "minio", "objectKey", "token", "apiKey"} {
		if strings.Contains(strings.ToLower(block), forbidden) {
			t.Fatalf("task_context leaked sensitive field marker %q: %s", forbidden, block)
		}
	}
}

func TestDiagnosisRunContextReverseScopeMatchesEffectiveAuthorization(t *testing.T) {
	caseID := uuid.New()
	sourceID := uuid.New()
	policy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID}, DataSourceIDs: []uuid.UUID{sourceID},
		},
	)
	runContext, err := BuildDiagnosisRunContext(DiagnosisRunContextInput{
		Policy: &policy, Actor: diagnosisTestActor(),
		ProfileToolNames: diagnosisProfileNamesForTest(
			ToolReadExternalCase, ToolSearchSchemaCatalog, ToolReadAttachment,
		),
		ExternalCaseID: caseID,
		DataSources: []DiagnosisCeilingDataSource{{
			ID: sourceID, Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
	})
	if err != nil {
		t.Fatalf("BuildDiagnosisRunContext: %v", err)
	}
	scope := runContext.Scope()
	if scope.TaskType() != TaskTypeDiagnosis || scope.UserID() == uuid.Nil {
		t.Fatalf("reverse scope = %+v", scope)
	}
	if !scope.CapabilityAllowed(ToolCapabilityCase) || !scope.CapabilityAllowed(ToolCapabilitySQL) {
		t.Fatalf("reverse capabilities = %v", scope.AllowedCapabilities())
	}
	if scope.CapabilityAllowed(ToolCapabilityAttachment) {
		t.Fatal("reverse scope fabricated attachment capability without attachment.read")
	}
	if sources := scope.DataSources(); len(sources) != 1 || sources[0].ID != sourceID ||
		sources[0].SafetyMode != DataSourceSafetyReadOnly {
		t.Fatalf("reverse data sources = %v", sources)
	}
	// 反向 TaskScope 与权威 v2 RunAccess 绑定顺序：WithTaskScope 先写入兼容
	// 上下文，WithRunAccess 最后覆盖，兼容转换绝不能改写权威值。
	ctx := WithTaskScope(context.Background(), scope)
	ctx = agentruntime.WithRunAccess(ctx, runContext.Access())
	bound, ok := agentruntime.RunAccessFromContext(ctx)
	if !ok || !bound.Allows(agentruntime.PermissionSQLRead) || !bound.Grants().AllowsExternalCase(caseID) {
		t.Fatalf("authoritative RunAccess was overridden: %+v ok=%t", bound, ok)
	}
}

func TestDiagnosisProfileFingerprintStableAcrossPolicies(t *testing.T) {
	catalog, err := NewDiagnosisDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
	})
	if err != nil {
		t.Fatalf("NewDiagnosisDefaultToolCatalog: %v", err)
	}
	minimalPolicy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{runnerTestCaseID}},
	)
	widePolicy := mustDiagnosisPolicyForTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionSQLRead, agentruntime.PermissionKnowledgeRead},
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{runnerTestCaseID}, DataSourceIDs: []uuid.UUID{uuid.New()},
		},
	)
	resolve := func(policy agentruntime.InvestigationPolicy) string {
		t.Helper()
		access := mustDiagnosisAccessForTest(t, policy)
		resolved, err := catalog.ResolveProfile(
			agentruntime.WithRunAccess(context.Background(), access), agentruntime.ToolProfileDiagnosis,
		)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		fingerprint, err := CanonicalToolContractFingerprint(context.Background(), resolved.Tools)
		if err != nil {
			t.Fatalf("CanonicalToolContractFingerprint: %v", err)
		}
		return fingerprint
	}
	first := resolve(minimalPolicy)
	second := resolve(widePolicy)
	if first != second {
		t.Fatalf("Tool Profile fingerprint changed across policies: %s vs %s", first, second)
	}
}

func mustDiagnosisAccessForTest(t *testing.T, policy agentruntime.InvestigationPolicy) agentruntime.RunAccess {
	t.Helper()
	ceiling, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.DeriveDiagnosisRunAccess(policy, diagnosisTestActor(), agentruntime.AccessCeiling{
		Permissions: policy.Permissions(),
		Grants:      ceiling,
	})
	if err != nil {
		t.Fatal(err)
	}
	return access
}
