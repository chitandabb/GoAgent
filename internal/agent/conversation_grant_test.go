package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/objectstore"

	"github.com/google/uuid"
)

func attachmentReadResultForTest(attachmentID, userID uuid.UUID) attachment.ReadResult {
	return attachment.ReadResult{
		Attachment: attachment.Attachment{
			ID: attachmentID, OwnerUserID: userID, Scope: attachment.ScopeSession,
			Status: attachment.StatusUploaded,
			Ref: objectstore.ObjectRef{
				Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object", ETag: "etag",
				SizeBytes: 12, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				MediaType: "text/plain", OriginalName: "error.log",
			},
		},
		ParserVersion: "plain-text-elements-v1",
		Elements:      []attachment.Element{{Index: 0, ElementType: "text", ContentText: "timeout"}},
	}
}

// conversation_grant_test.go 验证 Conversation 运行时每个 Tool 在底层
// getter/reader/creator 之前执行 RunAccess 资源 Grant 校验。Diagnosis 兼容
// 链路（InvestigationPolicy 尚未持久化）不受该 Conversation 边界约束，
// RuntimeKind 区分两种执行模式。

func mustConversationAccess(
	t *testing.T,
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
		agentruntime.Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}, permissionSet, grants,
	)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

type countingExternalCaseGetter struct {
	calls int
	item  *externalcase.ExternalCase
}

func (s *countingExternalCaseGetter) Get(_ context.Context, _ uuid.UUID) (*externalcase.ExternalCase, error) {
	s.calls++
	return s.item, nil
}

func TestReadExternalCaseToolConversationGrantEnforcedBeforeGetter(t *testing.T) {
	caseID := runnerTestCaseID
	getter := &countingExternalCaseGetter{item: &externalcase.ExternalCase{ID: caseID, ExternalCaseKey: "TKT-1"}}
	current, err := NewReadExternalCaseTool(getter)
	if err != nil {
		t.Fatal(err)
	}
	// case.read 已授予但该 case 不在 ExternalCaseIDs Grant：getter 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`"}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized error = %v, want ErrConversationResourceNotGranted", err)
	}
	if getter.calls != 0 {
		t.Fatalf("getter calls = %d, want 0", getter.calls)
	}
	// case 在 Grant：getter 恰好一次。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`"}`); err != nil {
		t.Fatalf("authorized run failed: %v", err)
	}
	if getter.calls != 1 {
		t.Fatalf("getter calls = %d, want 1", getter.calls)
	}
}

func TestReadExternalCaseToolDiagnosisAccessFailsClosedWithoutGrant(t *testing.T) {
	getter := &countingExternalCaseGetter{item: &externalcase.ExternalCase{ID: runnerTestCaseID, ExternalCaseKey: "TKT-1"}}
	current, err := NewReadExternalCaseTool(getter)
	if err != nil {
		t.Fatal(err)
	}
	// Diagnosis RunAccess 只带 sql.read/数据源 Grant、不带 ExternalCaseIDs：
	// v2 通用资源 Guard 必须 fail-closed，getter 零调用。旧 TaskScope 兼容
	// 适配器已硬切删除，不存在绕过 Grant 的路径。
	access := mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{DataSourceIDs: []uuid.UUID{uuid.New()}},
	)
	if _, err := current.InvokableRun(
		withTestRunAccess(context.Background(), access), `{"externalCaseId":"`+runnerTestCaseID.String()+`"}`,
	); !errors.Is(err, ErrResourceNotGranted) {
		t.Fatalf("diagnosis access error = %v, want ErrResourceNotGranted", err)
	}
	if getter.calls != 0 {
		t.Fatalf("getter calls = %d, want 0", getter.calls)
	}
}

func TestReadAttachmentToolConversationGrantEnforcedBeforeReader(t *testing.T) {
	userID, conversationID, messageID, attachmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	reader := &attachmentReaderStub{result: attachmentReadResultForTest(attachmentID, userID)}
	current, err := NewReadAttachmentTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	commandCtx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	// attachment.read 已授予但该附件不在 Grant：reader 零调用。
	ctx := agentruntime.WithRunAccess(commandCtx, mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionAttachmentRead},
		agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized error = %v, want ErrConversationResourceNotGranted", err)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
	// 附件在 Grant：reader 恰好一次，且仍走 CommandContext 四元校验。
	ctx = agentruntime.WithRunAccess(commandCtx, mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionAttachmentRead},
		agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{attachmentID}}))
	if _, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`); err != nil {
		t.Fatalf("authorized run failed: %v", err)
	}
	if reader.calls != 1 || reader.attachmentID != attachmentID {
		t.Fatalf("reader calls/attachment = %d/%s", reader.calls, reader.attachmentID)
	}
}

func TestGetDiagnosisTaskStatusToolConversationGrantEnforcedBeforeReader(t *testing.T) {
	taskID := uuid.New()
	reader := &diagnosisTaskStatusReaderStub{}
	current, err := NewGetDiagnosisTaskStatusTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	// task.read 已授予但该任务不在 Grant：reader 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionTaskRead},
		agentruntime.ResourceGrantsConfig{TaskIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"taskId":"`+taskID.String()+`"}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized error = %v, want ErrConversationResourceNotGranted", err)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
	// 任务在 Grant：reader 恰好一次。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionTaskRead},
		agentruntime.ResourceGrantsConfig{TaskIDs: []uuid.UUID{taskID}}))
	if _, err := current.InvokableRun(ctx, `{"taskId":"`+taskID.String()+`"}`); err != nil {
		t.Fatalf("authorized run failed: %v", err)
	}
	if reader.calls != 1 || reader.taskID != taskID {
		t.Fatalf("reader calls/task = %d/%s", reader.calls, reader.taskID)
	}
}

func TestCreateDiagnosisTaskToolConversationGrantsEnforcedBeforeCreator(t *testing.T) {
	caseID, attachmentID, parentTaskID := uuid.New(), uuid.New(), uuid.New()
	creator := &diagnosisToolCreatorStub{}
	current, err := NewCreateDiagnosisTaskTool(creator)
	if err != nil {
		t.Fatal(err)
	}
	diagnosisCreate := []agentruntime.Permission{agentruntime.PermissionDiagnosisCreate}

	// 1. case 不在 ExternalCaseIDs Grant：creator 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t, diagnosisCreate,
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`","diagnosisGoal":"诊断"}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized case error = %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0", creator.calls)
	}

	// 2. 显式 attachmentId 不在 AttachmentIDs Grant：creator 零调用。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t, diagnosisCreate,
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID},
			AttachmentIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`","diagnosisGoal":"诊断","attachmentIds":["`+attachmentID.String()+`"]}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized attachment error = %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0 after attachment rejection", creator.calls)
	}

	// 3. parentTaskId 不在 TaskIDs Grant：creator 零调用。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t, diagnosisCreate,
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID},
			TaskIDs: []uuid.UUID{uuid.New()}}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`","diagnosisGoal":"诊断","parentTaskId":"`+parentTaskID.String()+`"}`); !errors.Is(err, ErrConversationResourceNotGranted) {
		t.Fatalf("unauthorized parent error = %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0 after parent rejection", creator.calls)
	}

	// 4. case + attachment + parentTaskId 全部在 Grant：creator 恰好一次。
	ctx = agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t, diagnosisCreate,
		agentruntime.ResourceGrantsConfig{
			ExternalCaseIDs: []uuid.UUID{caseID}, AttachmentIDs: []uuid.UUID{attachmentID},
			TaskIDs: []uuid.UUID{parentTaskID},
		}))
	if _, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`","diagnosisGoal":"诊断","attachmentIds":["`+attachmentID.String()+`"],"parentTaskId":"`+parentTaskID.String()+`"}`); err != nil {
		t.Fatalf("authorized run failed: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls = %d, want 1", creator.calls)
	}
}
