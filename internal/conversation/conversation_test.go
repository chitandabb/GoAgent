package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

func TestMessageQueryNormalize(t *testing.T) {
	query := MessageQuery{AfterSeq: -1, Limit: 999}
	query.Normalize()
	if query.AfterSeq != 0 || query.Limit != MaxMessageLimit {
		t.Fatalf("normalized query = %+v", query)
	}
}

func TestServiceAppendUserMessageValidatesStructuredReferences(t *testing.T) {
	repository := &conversationRepositoryStub{}
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	_, err = service.AppendUserMessage(context.Background(), Actor{UserID: uuid.New()}, AppendMessageInput{
		ConversationID: uuid.New(), Content: "诊断这个工单",
		CaseReferences: []CaseReference{{ExternalCaseID: uuid.New(), Kind: ReferenceKindCreated}},
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("AppendUserMessage() error = %v, want ErrInvalidMessage", err)
	}
	if repository.appendCalls != 0 {
		t.Fatalf("repository append calls = %d, want 0", repository.appendCalls)
	}
}

func TestServiceAppendUserMessageTrimsContentAndUsesUserRole(t *testing.T) {
	repository := &conversationRepositoryStub{message: Message{ID: uuid.New()}}
	service, _ := NewService(repository)
	message, err := service.AppendUserMessage(context.Background(), Actor{UserID: uuid.New()}, AppendMessageInput{
		ConversationID: uuid.New(), Content: "  请查看错误码  ",
		CaseReferences: []CaseReference{{ExternalCaseID: uuid.New(), Kind: ReferenceKindSelected}},
	})
	if err != nil {
		t.Fatalf("AppendUserMessage(): %v", err)
	}
	if repository.gotInput.Role != MessageRoleUser || repository.gotInput.Content != "请查看错误码" {
		t.Fatalf("repository input = %+v", repository.gotInput)
	}
	if message.ID == uuid.Nil {
		t.Fatal("message id is empty")
	}
}

type conversationRepositoryStub struct {
	message     Message
	gotInput    AppendMessageInput
	appendCalls int
}

func (s *conversationRepositoryStub) Create(context.Context, uuid.UUID, CreateInput, time.Time) (Conversation, error) {
	return Conversation{}, nil
}

func (s *conversationRepositoryStub) Get(context.Context, uuid.UUID, uuid.UUID) (Conversation, error) {
	return Conversation{}, nil
}

func (s *conversationRepositoryStub) List(context.Context, uuid.UUID, ListQuery) (ListResult, error) {
	return ListResult{}, nil
}

func (s *conversationRepositoryStub) ListMessages(context.Context, uuid.UUID, uuid.UUID, MessageQuery) (MessagePage, error) {
	return MessagePage{}, nil
}

func (s *conversationRepositoryStub) AppendMessage(_ context.Context, _ uuid.UUID, input AppendMessageInput, _ time.Time) (Message, error) {
	s.appendCalls++
	s.gotInput = input
	return s.message, nil
}

func (s *conversationRepositoryStub) GetMessage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Message, error) {
	return s.message, nil
}

func (s *conversationRepositoryStub) GetLatestMessage(context.Context, uuid.UUID, uuid.UUID) (Message, error) {
	return s.message, nil
}

func (s *conversationRepositoryStub) AppendTaskReference(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ReferenceKind, time.Time) error {
	return nil
}

func TestCreateDiagnosisTaskRequiresLatestDirectDiagnosisMessage(t *testing.T) {
	userID, conversationID, messageID, caseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repositoryStub := &commandRepositoryStub{
		message: Message{ID: messageID, ConversationID: conversationID, Role: MessageRoleUser, Content: "请看看这个工单"},
		latest:  Message{ID: messageID, ConversationID: conversationID, Role: MessageRoleUser, Content: "请看看这个工单"},
	}
	service := newCommandService(t, repositoryStub, &commandTaskCreatorStub{}, &commandTaskReaderStub{}, &commandCaseReaderStub{item: &externalcase.ExternalCase{ID: caseID, SourceFingerprint: "sha256:case"}})
	ctx := WithCommandContext(context.Background(), CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: Actor{UserID: userID},
	})
	_, err := service.CreateDiagnosisTask(ctx, CreateDiagnosisInput{ExternalCaseID: caseID, DiagnosisGoal: "检查这个工单"})
	if !errors.Is(err, ErrDiagnosisIntentRequired) {
		t.Fatalf("CreateDiagnosisTask() error = %v, want ErrDiagnosisIntentRequired", err)
	}
}

func TestCreateDiagnosisTaskUsesSelectedReferenceAndServerIdempotency(t *testing.T) {
	userID, conversationID, messageID, caseID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := Message{
		ID: messageID, ConversationID: conversationID, Role: MessageRoleUser,
		Content: "请诊断这个工单", CaseReferences: []CaseReference{{ExternalCaseID: caseID, Kind: ReferenceKindSelected}},
	}
	repositoryStub := &commandRepositoryStub{message: message, latest: message}
	taskCreator := &commandTaskCreatorStub{result: diagnosis.TaskCreateResult{Task: diagnosis.DiagnosisTask{ID: taskID, Status: diagnosis.TaskPending}}}
	service := newCommandService(t, repositoryStub, taskCreator, &commandTaskReaderStub{}, &commandCaseReaderStub{item: &externalcase.ExternalCase{ID: caseID, SourceFingerprint: "sha256:case"}})
	ctx := WithCommandContext(context.Background(), CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: Actor{UserID: userID},
	})
	result, err := service.CreateDiagnosisTask(ctx, CreateDiagnosisInput{ExternalCaseID: caseID, DiagnosisGoal: "诊断这个工单"})
	if err != nil {
		t.Fatalf("CreateDiagnosisTask(): %v", err)
	}
	if result.Task.ID != taskID || taskCreator.input.IdempotencyKey == "" || repositoryStub.referenceCalls != 1 {
		t.Fatalf("result=%+v task input=%+v reference calls=%d", result, taskCreator.input, repositoryStub.referenceCalls)
	}
	if taskCreator.input.ExpectedSourceFingerprint != "sha256:case" || taskCreator.input.ExternalCaseID != caseID {
		t.Fatalf("task input leaked incorrect source facts: %+v", taskCreator.input)
	}
}

func TestCreateDiagnosisTaskRejectsCaseNotMatchingSelectedReference(t *testing.T) {
	userID, conversationID, messageID, selectedCaseID, requestedCaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := Message{
		ID: messageID, ConversationID: conversationID, Role: MessageRoleUser, Content: "请诊断这个工单",
		CaseReferences: []CaseReference{{ExternalCaseID: selectedCaseID, Kind: ReferenceKindSelected}},
	}
	repositoryStub := &commandRepositoryStub{message: message, latest: message}
	service := newCommandService(t, repositoryStub, &commandTaskCreatorStub{}, &commandTaskReaderStub{}, &commandCaseReaderStub{})
	ctx := WithCommandContext(context.Background(), CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: Actor{UserID: userID},
	})
	_, err := service.CreateDiagnosisTask(ctx, CreateDiagnosisInput{ExternalCaseID: requestedCaseID, DiagnosisGoal: "诊断这个工单"})
	if !errors.Is(err, ErrCaseReferenceRequired) {
		t.Fatalf("CreateDiagnosisTask() error = %v, want ErrCaseReferenceRequired", err)
	}
}

func newCommandService(t *testing.T, repository Repository, creator DiagnosisTaskCreator, reader DiagnosisTaskReader, cases ExternalCaseReader) *Service {
	t.Helper()
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	service, err = service.WithDiagnosisCommandDependencies(creator, reader, cases)
	if err != nil {
		t.Fatalf("WithDiagnosisCommandDependencies(): %v", err)
	}
	return service
}

type commandRepositoryStub struct {
	message        Message
	latest         Message
	referenceCalls int
}

func (s *commandRepositoryStub) Create(context.Context, uuid.UUID, CreateInput, time.Time) (Conversation, error) {
	return Conversation{}, nil
}
func (s *commandRepositoryStub) Get(context.Context, uuid.UUID, uuid.UUID) (Conversation, error) {
	return Conversation{}, nil
}
func (s *commandRepositoryStub) List(context.Context, uuid.UUID, ListQuery) (ListResult, error) {
	return ListResult{}, nil
}
func (s *commandRepositoryStub) ListMessages(context.Context, uuid.UUID, uuid.UUID, MessageQuery) (MessagePage, error) {
	return MessagePage{}, nil
}
func (s *commandRepositoryStub) AppendMessage(context.Context, uuid.UUID, AppendMessageInput, time.Time) (Message, error) {
	return Message{}, nil
}
func (s *commandRepositoryStub) GetMessage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Message, error) {
	return s.message, nil
}
func (s *commandRepositoryStub) GetLatestMessage(context.Context, uuid.UUID, uuid.UUID) (Message, error) {
	return s.latest, nil
}
func (s *commandRepositoryStub) AppendTaskReference(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ReferenceKind, time.Time) error {
	s.referenceCalls++
	return nil
}

type commandTaskCreatorStub struct {
	result diagnosis.TaskCreateResult
	input  diagnosis.CreateTaskInput
}

func (s *commandTaskCreatorStub) Create(_ context.Context, _ diagnosis.TaskActor, input diagnosis.CreateTaskInput) (diagnosis.TaskCreateResult, error) {
	s.input = input
	return s.result, nil
}

type commandTaskReaderStub struct{}

func (*commandTaskReaderStub) Get(context.Context, diagnosis.TaskActor, uuid.UUID) (diagnosis.DiagnosisTask, error) {
	return diagnosis.DiagnosisTask{}, nil
}

type commandCaseReaderStub struct {
	item *externalcase.ExternalCase
}

func (s *commandCaseReaderStub) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	if s.item == nil {
		return nil, repository.ErrNotFound
	}
	return s.item, nil
}
