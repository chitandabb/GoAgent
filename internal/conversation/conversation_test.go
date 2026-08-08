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

func TestServiceRespondToUserMessageRequiresAgent(t *testing.T) {
	service, _ := NewService(&conversationRepositoryStub{})
	_, err := service.RespondToUserMessage(context.Background(), Actor{UserID: uuid.New()}, uuid.New(), uuid.New())
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("RespondToUserMessage() error = %v, want ErrAgentUnavailable", err)
	}
}

func TestServiceRespondToUserMessageRejectsStaleMessage(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	stale := Message{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: MessageRoleUser, Content: "诊断第一个工单"}
	latest := Message{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: MessageRoleUser, Content: "改为诊断第二个工单"}
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
		messages:     []Message{stale, latest},
	}
	agent := &conversationAgentResponderStub{response: AgentResponse{Content: "不应执行"}}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)

	_, err := service.RespondToUserMessage(context.Background(), Actor{UserID: userID}, conversationID, stale.ID)
	if !errors.Is(err, ErrCommandNotLatest) {
		t.Fatalf("RespondToUserMessage() error = %v, want ErrCommandNotLatest", err)
	}
	if agent.calls != 0 {
		t.Fatalf("agent calls = %d, want 0", agent.calls)
	}
}

func TestServiceAppendUserMessageAndRespondPersistsAssistantAndCreatedTaskReference(t *testing.T) {
	userID, conversationID, caseID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agent := &conversationAgentResponderStub{response: AgentResponse{Content: "  已创建异步诊断任务。  "}}
	agent.hook = func(ctx context.Context, request AgentRequest) error {
		commandContext, ok := CommandContextFromContext(ctx)
		if !ok || commandContext.ConversationID != conversationID || commandContext.UserMessageID != request.UserMessage.ID {
			return ErrCommandContextRequired
		}
		return repository.AppendTaskReference(ctx, userID, request.UserMessage.ID, taskID, ReferenceKindCreated, time.Now())
	}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)

	turn, err := service.AppendUserMessageAndRespond(context.Background(), Actor{UserID: userID}, AppendMessageInput{
		ConversationID: conversationID,
		Content:        "请诊断这个工单",
		CaseReferences: []CaseReference{{ExternalCaseID: caseID, Kind: ReferenceKindSelected}},
	})
	if err != nil {
		t.Fatalf("AppendUserMessageAndRespond(): %v", err)
	}
	if turn.UserMessage.Role != MessageRoleUser || turn.AssistantMessage.Role != MessageRoleAssistant ||
		turn.AssistantMessage.Content != "已创建异步诊断任务。" {
		t.Fatalf("turn = %+v", turn)
	}
	if len(turn.AssistantMessage.TaskReferences) != 1 ||
		turn.AssistantMessage.TaskReferences[0] != (TaskReference{TaskID: taskID, Kind: ReferenceKindCreated}) {
		t.Fatalf("assistant task references = %+v", turn.AssistantMessage.TaskReferences)
	}
	if len(repository.messages) != 2 || agent.calls != 1 || agent.request.UserMessage.ID != turn.UserMessage.ID {
		t.Fatalf("messages=%d agent calls=%d request=%+v", len(repository.messages), agent.calls, agent.request)
	}
}

func TestServiceAppendUserMessageAndRespondKeepsUserMessageWhenAgentFails(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agentFailure := errors.New("model unavailable")
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(&conversationAgentResponderStub{err: agentFailure})

	turn, err := service.AppendUserMessageAndRespond(context.Background(), Actor{UserID: userID}, AppendMessageInput{
		ConversationID: conversationID, Content: "知识库如何更新？",
	})
	if !errors.Is(err, agentFailure) {
		t.Fatalf("AppendUserMessageAndRespond() error = %v, want agent failure", err)
	}
	if turn.UserMessage.ID == uuid.Nil || len(repository.messages) != 1 || repository.messages[0].Role != MessageRoleUser {
		t.Fatalf("turn=%+v messages=%+v", turn, repository.messages)
	}
}

type conversationRepositoryStub struct {
	message     Message
	gotInput    AppendMessageInput
	appendCalls int
}

type conversationAgentResponderStub struct {
	response AgentResponse
	err      error
	hook     func(context.Context, AgentRequest) error
	calls    int
	request  AgentRequest
}

func (s *conversationAgentResponderStub) Respond(ctx context.Context, request AgentRequest) (AgentResponse, error) {
	s.calls++
	s.request = request
	if s.hook != nil {
		if err := s.hook(ctx, request); err != nil {
			return AgentResponse{}, err
		}
	}
	return s.response, s.err
}

type turnRepositoryStub struct {
	conversation Conversation
	messages     []Message
}

func (s *turnRepositoryStub) Create(context.Context, uuid.UUID, CreateInput, time.Time) (Conversation, error) {
	return s.conversation, nil
}

func (s *turnRepositoryStub) Get(_ context.Context, userID, conversationID uuid.UUID) (Conversation, error) {
	if s.conversation.UserID != userID || s.conversation.ID != conversationID {
		return Conversation{}, repository.ErrNotFound
	}
	return s.conversation, nil
}

func (s *turnRepositoryStub) List(context.Context, uuid.UUID, ListQuery) (ListResult, error) {
	return ListResult{}, nil
}

func (s *turnRepositoryStub) ListMessages(_ context.Context, userID, conversationID uuid.UUID, query MessageQuery) (MessagePage, error) {
	if s.conversation.UserID != userID || s.conversation.ID != conversationID {
		return MessagePage{}, repository.ErrNotFound
	}
	query.Normalize()
	items := make([]Message, 0, query.Limit)
	for _, message := range s.messages {
		if message.Seq > query.AfterSeq && len(items) < query.Limit {
			items = append(items, message)
		}
	}
	return MessagePage{Items: items, AfterSeq: query.AfterSeq}, nil
}

func (s *turnRepositoryStub) AppendMessage(_ context.Context, userID uuid.UUID, input AppendMessageInput, createdAt time.Time) (Message, error) {
	if s.conversation.UserID != userID || s.conversation.ID != input.ConversationID {
		return Message{}, repository.ErrNotFound
	}
	message := Message{
		ID: uuid.New(), ConversationID: input.ConversationID, Seq: int64(len(s.messages) + 1),
		Role: input.Role, Content: input.Content, ContentSchemaVersion: 1,
		CaseReferences: append([]CaseReference(nil), input.CaseReferences...),
		TaskReferences: append([]TaskReference(nil), input.TaskReferences...), CreatedAt: createdAt,
	}
	s.messages = append(s.messages, message)
	return message, nil
}

func (s *turnRepositoryStub) GetMessage(_ context.Context, userID, conversationID, messageID uuid.UUID) (Message, error) {
	if s.conversation.UserID != userID || s.conversation.ID != conversationID {
		return Message{}, repository.ErrNotFound
	}
	for _, message := range s.messages {
		if message.ID == messageID {
			return message, nil
		}
	}
	return Message{}, repository.ErrNotFound
}

func (s *turnRepositoryStub) GetLatestMessage(_ context.Context, userID, conversationID uuid.UUID) (Message, error) {
	if s.conversation.UserID != userID || s.conversation.ID != conversationID || len(s.messages) == 0 {
		return Message{}, repository.ErrNotFound
	}
	return s.messages[len(s.messages)-1], nil
}

func (s *turnRepositoryStub) AppendTaskReference(_ context.Context, userID, messageID, taskID uuid.UUID, kind ReferenceKind, _ time.Time) error {
	if s.conversation.UserID != userID {
		return repository.ErrNotFound
	}
	for index := range s.messages {
		if s.messages[index].ID == messageID {
			s.messages[index].TaskReferences = append(s.messages[index].TaskReferences, TaskReference{TaskID: taskID, Kind: kind})
			return nil
		}
	}
	return repository.ErrNotFound
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
