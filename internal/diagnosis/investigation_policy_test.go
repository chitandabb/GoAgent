package diagnosis

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
)

func TestInvestigationPolicyModeContractValues(t *testing.T) {
	if InvestigationPolicyModeLegacy != "legacy" || InvestigationPolicyModeFrozen != "frozen" {
		t.Fatalf("policy mode constants drifted: legacy=%q frozen=%q",
			InvestigationPolicyModeLegacy, InvestigationPolicyModeFrozen)
	}
	if !InvestigationPolicyModeLegacy.Valid() || !InvestigationPolicyModeFrozen.Valid() {
		t.Fatal("legacy/frozen modes must be valid")
	}
	for _, invalid := range []InvestigationPolicyMode{"", "FROZEN", "Legacy", "froze", "unknown"} {
		if invalid.Valid() {
			t.Fatalf("invalid policy mode %q reported valid", invalid)
		}
	}
}

func mustInvestigationPolicyBuilder(
	t *testing.T,
	basePermissions []agentruntime.Permission,
	allowedDataSourceIDs []uuid.UUID,
) InvestigationPolicyBuilder {
	t.Helper()
	builder, err := NewInvestigationPolicyBuilder(InvestigationPolicyConfig{
		BasePermissions:      basePermissions,
		AllowedDataSourceIDs: allowedDataSourceIDs,
	})
	if err != nil {
		t.Fatalf("NewInvestigationPolicyBuilder: %v", err)
	}
	return builder
}

func TestInvestigationPolicyBuilderFreezesDeploymentPermissionsAndTaskGrants(t *testing.T) {
	caseID := uuid.New()
	sqlSourceID := uuid.New()
	unrelatedSourceID := uuid.New()
	firstAttachment, secondAttachment := uuid.New(), uuid.New()
	builder := mustInvestigationPolicyBuilder(t,
		[]agentruntime.Permission{
			agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
			agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead,
		},
		[]uuid.UUID{sqlSourceID},
	)
	policy, err := builder.Build(InvestigationPolicyInput{
		ExternalCaseID: caseID,
		// 工单源 + 用户证据源中只有部署允许的 sqlSourceID 进入 Grant。
		DataSourceIDs: []uuid.UUID{sqlSourceID, unrelatedSourceID},
		AttachmentIDs: []uuid.UUID{secondAttachment, firstAttachment},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if policy.SchemaVersion() != InvestigationPolicySchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", policy.SchemaVersion(), InvestigationPolicySchemaVersion)
	}
	for _, want := range []agentruntime.Permission{
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead, agentruntime.PermissionAttachmentRead,
	} {
		if !policy.Permissions().Has(want) {
			t.Fatalf("policy missing permission %q: %v", want, policy.Permissions().Values())
		}
	}
	for _, forbidden := range []agentruntime.Permission{
		agentruntime.PermissionTaskRead, agentruntime.PermissionMemoryRead, agentruntime.PermissionDiagnosisCreate,
	} {
		if policy.Permissions().Has(forbidden) {
			t.Fatalf("policy must never grant %q", forbidden)
		}
	}
	if !policy.Grants().AllowsExternalCase(caseID) {
		t.Fatalf("policy missing external case grant: %v", policy.Grants())
	}
	if !policy.Grants().AllowsDataSource(sqlSourceID) || policy.Grants().AllowsDataSource(unrelatedSourceID) {
		t.Fatalf("data source grants = %v", policy.Grants().DataSourceIDs())
	}
	if !policy.Grants().AllowsAttachment(firstAttachment) || !policy.Grants().AllowsAttachment(secondAttachment) {
		t.Fatalf("attachment grants = %v", policy.Grants().AttachmentIDs())
	}
	if len(policy.Grants().Repositories()) != 0 || len(policy.Grants().TaskIDs()) != 0 {
		t.Fatalf("policy fabricated repository/task grants: %v", policy.Grants())
	}
}

func TestInvestigationPolicyBuilderOmitsAttachmentPermissionWithoutAttachments(t *testing.T) {
	builder := mustInvestigationPolicyBuilder(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead}, nil,
	)
	policy, err := builder.Build(InvestigationPolicyInput{ExternalCaseID: uuid.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if policy.Permissions().Has(agentruntime.PermissionAttachmentRead) {
		t.Fatal("attachment.read granted without frozen attachments")
	}
}

func TestInvestigationPolicyBuilderRejectsForbiddenBasePermissions(t *testing.T) {
	for _, forbidden := range []agentruntime.Permission{
		agentruntime.PermissionTaskRead, agentruntime.PermissionMemoryRead, agentruntime.PermissionDiagnosisCreate,
	} {
		if _, err := NewInvestigationPolicyBuilder(InvestigationPolicyConfig{
			BasePermissions: []agentruntime.Permission{agentruntime.PermissionCaseRead, forbidden},
		}); err == nil {
			t.Fatalf("builder accepted forbidden permission %q", forbidden)
		}
	}
	if _, err := NewInvestigationPolicyBuilder(InvestigationPolicyConfig{
		BasePermissions: []agentruntime.Permission{agentruntime.PermissionKnowledgeRead},
	}); err == nil {
		t.Fatal("builder accepted a base permission set without case.read")
	}
	if _, err := NewInvestigationPolicyBuilder(InvestigationPolicyConfig{}); err == nil {
		t.Fatal("builder accepted an empty permission set")
	}
	if _, err := NewInvestigationPolicyBuilder(InvestigationPolicyConfig{
		BasePermissions:      []agentruntime.Permission{agentruntime.PermissionCaseRead},
		AllowedDataSourceIDs: []uuid.UUID{uuid.Nil},
	}); err == nil {
		t.Fatal("builder accepted a nil data source id")
	}
}

func TestInvestigationPolicyBuilderRejectsInvalidTaskInput(t *testing.T) {
	builder := mustInvestigationPolicyBuilder(t, []agentruntime.Permission{agentruntime.PermissionCaseRead}, nil)
	if _, err := builder.Build(InvestigationPolicyInput{}); err == nil {
		t.Fatal("builder accepted a nil external case id")
	}
	if _, err := builder.Build(InvestigationPolicyInput{
		ExternalCaseID: uuid.New(), AttachmentIDs: []uuid.UUID{uuid.Nil},
	}); err == nil {
		t.Fatal("builder accepted a nil attachment id")
	}
	duplicate := uuid.New()
	if _, err := builder.Build(InvestigationPolicyInput{
		ExternalCaseID: uuid.New(), AttachmentIDs: []uuid.UUID{duplicate, duplicate},
	}); err == nil {
		t.Fatal("builder accepted duplicate attachment ids")
	}
}

func TestDiagnosisTaskServiceFreezesInvestigationPolicyIntoCreateRecord(t *testing.T) {
	caseID := uuid.New()
	sqlSourceID := uuid.New()
	ownerID := uuid.New()
	repo := &taskRepositoryStub{}
	reader := &taskCaseReaderStub{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: sqlSourceID, SourceFingerprint: "sha256:source",
		ReportedAt: mustTestTime(), SourceUpdatedAt: mustTestTime(),
	}}
	builder := mustInvestigationPolicyBuilder(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead, agentruntime.PermissionSQLRead},
		[]uuid.UUID{sqlSourceID},
	)
	service, err := NewDiagnosisTaskService(repo, reader, builder)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService: %v", err)
	}
	if _, err := service.Create(context.Background(), TaskActor{UserID: ownerID}, CreateTaskInput{
		ExternalCaseID: caseID, ExpectedSourceFingerprint: "sha256:source",
		RequestText: "检查状态", IdempotencyKey: uuid.NewString(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.createCalls != 1 {
		t.Fatalf("repository create calls = %d, want 1", repo.createCalls)
	}
	if repo.createInput.InvestigationPolicySchemaVersion != InvestigationPolicySchemaVersion {
		t.Fatalf("policy schema version = %d", repo.createInput.InvestigationPolicySchemaVersion)
	}
	if repo.createInput.InvestigationPolicyMode != InvestigationPolicyModeFrozen {
		t.Fatalf("policy mode = %q, want %q", repo.createInput.InvestigationPolicyMode, InvestigationPolicyModeFrozen)
	}
	policy, err := agentruntime.UnmarshalInvestigationPolicy(repo.createInput.InvestigationPolicy)
	if err != nil {
		t.Fatalf("decode frozen policy: %v", err)
	}
	if !policy.Permissions().Has(agentruntime.PermissionSQLRead) || !policy.Grants().AllowsDataSource(sqlSourceID) ||
		!policy.Grants().AllowsExternalCase(caseID) {
		t.Fatalf("frozen policy = %v", policy)
	}
}

func TestDiagnosisTaskServiceFingerprintIgnoresDeploymentPolicyChanges(t *testing.T) {
	caseID, sourceID, ownerID := uuid.New(), uuid.New(), uuid.New()
	reader := &taskCaseReaderStub{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: sourceID, SourceFingerprint: "sha256:source",
		ReportedAt: mustTestTime(), SourceUpdatedAt: mustTestTime(),
	}}
	firstRepo := &taskRepositoryStub{}
	firstBuilder := mustInvestigationPolicyBuilder(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead, agentruntime.PermissionSQLRead},
		[]uuid.UUID{sourceID},
	)
	firstService, err := NewDiagnosisTaskService(firstRepo, reader, firstBuilder)
	if err != nil {
		t.Fatalf("first NewDiagnosisTaskService: %v", err)
	}
	// 同一幂等命令在部署配置变化后（SQL 关闭）重建 Service：Policy 不同，
	// 但 request fingerprint 必须逐字节一致，否则幂等回放会误报冲突。
	secondRepo := &taskRepositoryStub{}
	secondBuilder := mustInvestigationPolicyBuilder(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead},
		nil,
	)
	secondService, err := NewDiagnosisTaskService(secondRepo, reader, secondBuilder)
	if err != nil {
		t.Fatalf("second NewDiagnosisTaskService: %v", err)
	}
	input := CreateTaskInput{
		ExternalCaseID: caseID, ExpectedSourceFingerprint: "sha256:source",
		RequestText: "检查状态", IdempotencyKey: "same-key", CorrelationID: uuid.New(),
	}
	if _, err := firstService.Create(context.Background(), TaskActor{UserID: ownerID}, input); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := secondService.Create(context.Background(), TaskActor{UserID: ownerID}, input); err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if firstRepo.createInput.RequestFingerprint != secondRepo.createInput.RequestFingerprint {
		t.Fatalf("fingerprint drifted across deployment configs: %q != %q",
			firstRepo.createInput.RequestFingerprint, secondRepo.createInput.RequestFingerprint)
	}
	if bytes.Equal(firstRepo.createInput.InvestigationPolicy, secondRepo.createInput.InvestigationPolicy) {
		t.Fatal("policies should differ between configs while the fingerprint stays stable")
	}
}

func TestDiagnosisTaskServiceFailsClosedWhenPolicyBuilderFails(t *testing.T) {
	repo := &taskRepositoryStub{}
	reader := &taskCaseReaderStub{item: &externalcase.ExternalCase{
		ID: uuid.New(), SourceFingerprint: "sha256:source",
		ReportedAt: mustTestTime(), SourceUpdatedAt: mustTestTime(),
	}}
	service, err := NewDiagnosisTaskService(repo, reader, investigationPolicyBuilderFunc(func(InvestigationPolicyInput) (agentruntime.InvestigationPolicy, error) {
		return agentruntime.InvestigationPolicy{}, errors.New("policy builder unavailable")
	}))
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService: %v", err)
	}
	if _, err := service.Create(context.Background(), TaskActor{UserID: uuid.New()}, CreateTaskInput{
		ExternalCaseID: reader.item.ID, ExpectedSourceFingerprint: "sha256:source",
		RequestText: "检查", IdempotencyKey: uuid.NewString(),
	}); err == nil {
		t.Fatal("Create accepted a failed policy builder")
	}
	if repo.createCalls != 0 {
		t.Fatalf("repository create calls = %d, want 0", repo.createCalls)
	}
}

func TestNewDiagnosisTaskServiceRequiresPolicyBuilder(t *testing.T) {
	if _, err := NewDiagnosisTaskService(&taskRepositoryStub{}, &taskCaseReaderStub{}, nil); err == nil {
		t.Fatal("NewDiagnosisTaskService accepted a nil policy builder")
	}
}

type investigationPolicyBuilderFunc func(InvestigationPolicyInput) (agentruntime.InvestigationPolicy, error)

func (f investigationPolicyBuilderFunc) Build(input InvestigationPolicyInput) (agentruntime.InvestigationPolicy, error) {
	return f(input)
}

func mustTestTime() time.Time { return time.Now().UTC() }
