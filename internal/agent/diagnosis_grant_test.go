package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
)

// diagnosis_grant_test.go 验证 Diagnosis 运行时 read_external_case 与
// read_attachment 在底层 getter/reader 之前执行具体资源 Grant 校验；
// 未授权时底层零调用。

func mustDiagnosisGrantAccess(
	t *testing.T,
	permissions []agentruntime.Permission,
	grantsConfig agentruntime.ResourceGrantsConfig,
) agentruntime.RunAccess {
	t.Helper()
	policy := mustDiagnosisPolicyForTest(t, permissions, grantsConfig)
	ceilingGrants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.DeriveDiagnosisRunAccess(policy, diagnosisTestActor(), agentruntime.AccessCeiling{
		Permissions: policy.Permissions(),
		Grants:      ceilingGrants,
	})
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func TestReadExternalCaseToolDiagnosisGrantEnforcedBeforeGetter(t *testing.T) {
	caseID := runnerTestCaseID
	getter := &countingExternalCaseGetter{item: &externalcase.ExternalCase{ID: caseID, ExternalCaseKey: "TKT-1"}}
	current, err := NewReadExternalCaseTool(getter)
	if err != nil {
		t.Fatal(err)
	}
	// 1. 无 RunAccess：fail-closed，getter 零调用。
	if _, err := current.InvokableRun(context.Background(), `{"externalCaseId":"`+caseID.String()+`"}`); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("missing RunAccess error = %v, want ErrRunAccessRequired", err)
	}
	if getter.calls != 0 {
		t.Fatalf("getter calls = %d, want 0", getter.calls)
	}
	// 2. Diagnosis 越权：case.read 已授予但该 case 不在 Grant，getter 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(), mustDiagnosisGrantAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`"}`); !errors.Is(err, ErrResourceNotGranted) {
		t.Fatalf("unauthorized diagnosis error = %v, want ErrResourceNotGranted", err)
	}
	if getter.calls != 0 {
		t.Fatalf("getter calls = %d, want 0", getter.calls)
	}
	// 3. case 在 Grant：getter 恰好一次。
	ctx = agentruntime.WithRunAccess(context.Background(), mustDiagnosisGrantAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`"}`); err != nil {
		t.Fatalf("authorized diagnosis run failed: %v", err)
	}
	if getter.calls != 1 {
		t.Fatalf("getter calls = %d, want 1", getter.calls)
	}
}

func TestReadAttachmentToolDiagnosisGrantEnforcedBeforeReader(t *testing.T) {
	userID, taskID, attachmentID := uuid.New(), uuid.New(), uuid.New()
	reader := &attachmentReaderStub{result: attachment.ReadResult{Attachment: attachment.Attachment{
		ID: attachmentID, OwnerUserID: userID, Scope: attachment.ScopeSession, Status: attachment.StatusUploaded,
	}}}
	current, err := NewReadAttachmentTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: userID, Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources:           []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly}},
		AllowedCapabilities:   []ToolCapability{ToolCapabilityCase, ToolCapabilityAttachment},
		AvailableDependencies: []ToolDependency{ToolDependencyAttachment},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 绑定顺序与生产一致：WithTaskScope 先写兼容上下文，WithRunAccess
	// 最后覆盖为权威 v2 RunAccess。
	bind := func(access agentruntime.RunAccess) context.Context {
		ctx := WithDiagnosisAttachmentContext(context.Background(), taskID)
		ctx = WithTaskScope(ctx, scope)
		return agentruntime.WithRunAccess(ctx, access)
	}
	// 1. Diagnosis 越权：attachment.read 已授予但附件不在 Grant，reader 零调用。
	ctx := bind(mustDiagnosisGrantAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionAttachmentRead},
		agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`); !errors.Is(err, ErrResourceNotGranted) {
		t.Fatalf("unauthorized diagnosis error = %v, want ErrResourceNotGranted", err)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
	// 2. 附件在 Grant：reader 恰好一次，仍走任务归属边界。
	ctx = bind(mustDiagnosisGrantAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionAttachmentRead},
		agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{attachmentID}}))
	if _, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`); err != nil {
		t.Fatalf("authorized diagnosis run failed: %v", err)
	}
	if reader.calls != 1 || reader.attachmentID != attachmentID || reader.messageID != taskID {
		t.Fatalf("reader calls/attachment/task = %d/%s/%s", reader.calls, reader.attachmentID, reader.messageID)
	}
}
