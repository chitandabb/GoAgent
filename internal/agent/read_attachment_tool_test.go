package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

func TestReadAttachmentToolInjectsCurrentMessageScope(t *testing.T) {
	userID, conversationID, messageID, attachmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	reader := &attachmentReaderStub{result: attachment.ReadResult{
		Attachment: attachment.Attachment{
			ID: attachmentID, OwnerUserID: userID, Scope: attachment.ScopeSession,
			ConversationID: &conversationID, Status: attachment.StatusUploaded,
			Ref: objectstore.ObjectRef{
				Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object", ETag: "etag",
				SizeBytes: 12, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				MediaType: "text/plain", OriginalName: "error.log",
			},
			UploadedAt: time.Now().UTC(),
		},
		ParserVersion: "plain-text-elements-v1",
		Elements:      []attachment.Element{{Index: 0, ElementType: "text", ContentText: "timeout"}},
	}}
	current, err := NewReadAttachmentTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentruntime.WithRunAccess(
		conversation.WithCommandContext(context.Background(), conversation.CommandContext{
			ConversationID: conversationID, UserMessageID: messageID,
			Actor: conversation.Actor{UserID: userID},
		}),
		mustConversationAccess(t, []agentruntime.Permission{agentruntime.PermissionAttachmentRead},
			agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{attachmentID}}),
	)
	raw, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`)
	if err != nil {
		t.Fatalf("InvokableRun(): %v", err)
	}
	if reader.userID != userID || reader.conversationID != conversationID ||
		reader.messageID != messageID || reader.attachmentID != attachmentID {
		t.Fatalf("reader scope = %+v", reader)
	}
	var response readAttachmentResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.SourceRef != "attachment:"+attachmentID.String() || response.SourceType != "attachment" ||
		response.AttachmentID != attachmentID.String() || response.ContentSHA256 == "" ||
		len(response.Elements) != 1 || response.Elements[0].ContentText != "timeout" {
		t.Fatalf("response=%+v", response)
	}
	evidence, ok := newToolEvidenceItem(ToolReadAttachment, raw, false)
	if !ok || evidence.SourceType != EvidenceSourceAttachment || evidence.Location != response.SourceRef {
		t.Fatalf("attachment evidence=%+v ok=%t", evidence, ok)
	}
}

func TestReadAttachmentToolInjectsDiagnosisTaskScope(t *testing.T) {
	userID, taskID, attachmentID := uuid.New(), uuid.New(), uuid.New()
	reader := &attachmentReaderStub{result: attachment.ReadResult{Attachment: attachment.Attachment{
		ID: attachmentID, OwnerUserID: userID, Scope: attachment.ScopeSession, Status: attachment.StatusUploaded,
		Ref: objectstore.ObjectRef{
			Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/object", ETag: "etag",
			SizeBytes: 12, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MediaType: "text/plain", OriginalName: "error.log",
		},
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
	// 绑定顺序与生产一致：WithTaskScope 先写兼容上下文，权威 v2 RunAccess
	// 最后覆盖，并携带本任务的附件 Grant。
	ctx := WithDiagnosisAttachmentContext(context.Background(), taskID)
	ctx = WithTaskScope(ctx, scope)
	ctx = agentruntime.WithRunAccess(ctx, mustDiagnosisGrantAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionAttachmentRead},
		agentruntime.ResourceGrantsConfig{AttachmentIDs: []uuid.UUID{attachmentID}}))
	if _, err := current.InvokableRun(ctx, `{"attachmentId":"`+attachmentID.String()+`"}`); err != nil {
		t.Fatalf("InvokableRun(): %v", err)
	}
	if reader.userID != userID || reader.messageID != taskID || reader.attachmentID != attachmentID {
		t.Fatalf("reader task scope=%+v", reader)
	}
}

type attachmentReaderStub struct {
	result         attachment.ReadResult
	userID         uuid.UUID
	conversationID uuid.UUID
	messageID      uuid.UUID
	attachmentID   uuid.UUID
	calls          int
}

func (s *attachmentReaderStub) ReadForMessage(
	_ context.Context, userID, conversationID, messageID, attachmentID uuid.UUID, _ int,
) (attachment.ReadResult, error) {
	s.calls++
	s.userID, s.conversationID = userID, conversationID
	s.messageID, s.attachmentID = messageID, attachmentID
	return s.result, nil
}

func (s *attachmentReaderStub) ReadForTask(
	_ context.Context, userID, taskID, attachmentID uuid.UUID, _ int,
) (attachment.ReadResult, error) {
	s.calls++
	s.userID, s.conversationID = userID, uuid.Nil
	s.messageID, s.attachmentID = taskID, attachmentID
	return s.result, nil
}
