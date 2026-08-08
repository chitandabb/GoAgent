package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestConversationRoutesCreateInjectsAuthenticatedUser(t *testing.T) {
	userID := uuid.New()
	conversationID := uuid.New()
	useCase := &conversationUseCaseStub{created: conversation.Conversation{
		ID: conversationID, UserID: userID, Status: conversation.StatusActive,
	}}
	routes, err := NewConversationRoutes(useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
	if err != nil {
		t.Fatalf("NewConversationRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", strings.NewReader(`{"title":"工单讨论"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Header().Get("Location") != "/api/v1/conversations/"+conversationID.String() {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if useCase.gotActor.UserID != userID || useCase.gotCreate.Title != "工单讨论" {
		t.Fatalf("use case args actor=%+v input=%+v", useCase.gotActor, useCase.gotCreate)
	}
}

func TestConversationRoutesAppendMessageParsesReferencesAndReturnsPageShape(t *testing.T) {
	userID := uuid.New()
	conversationID := uuid.New()
	caseID := uuid.New()
	taskID := uuid.New()
	useCase := &conversationUseCaseStub{message: conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleUser,
		Content: "诊断这个工单", CaseReferences: []conversation.CaseReference{{ExternalCaseID: caseID, Kind: conversation.ReferenceKindSelected}},
		TaskReferences: []conversation.TaskReference{{TaskID: taskID, Kind: conversation.ReferenceKindReferenced}},
	}}
	routes, _ := NewConversationRoutes(useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversationID.String()+"/messages", strings.NewReader(`{
"content":"诊断这个工单",
"caseReferences":[{"externalCaseId":"`+caseID.String()+`"}],
"taskReferences":[{"taskId":"`+taskID.String()+`","kind":"referenced"}]
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"seq":4`) ||
		!strings.Contains(response.Body.String(), `"kind":"selected"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.gotActor.UserID != userID || useCase.gotInput.ConversationID != conversationID ||
		len(useCase.gotInput.CaseReferences) != 1 || useCase.gotInput.CaseReferences[0].ExternalCaseID != caseID ||
		len(useCase.gotInput.TaskReferences) != 1 || useCase.gotInput.TaskReferences[0].TaskID != taskID {
		t.Fatalf("use case args actor=%+v input=%+v", useCase.gotActor, useCase.gotInput)
	}
}

func TestConversationRoutesRejectsInvalidReferenceBeforeUseCase(t *testing.T) {
	useCase := &conversationUseCaseStub{}
	routes, _ := NewConversationRoutes(useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+uuid.NewString()+"/messages", strings.NewReader(`{
"content":"诊断",
"caseReferences":[{"externalCaseId":"not-a-uuid"}]
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || useCase.appendCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, useCase.appendCalls, response.Body.String())
	}
}

type conversationUseCaseStub struct {
	created     conversation.Conversation
	message     conversation.Message
	createErr   error
	appendErr   error
	gotActor    conversation.Actor
	gotCreate   conversation.CreateInput
	gotInput    conversation.AppendMessageInput
	appendCalls int
}

func (s *conversationUseCaseStub) Create(_ context.Context, actor conversation.Actor, input conversation.CreateInput) (conversation.Conversation, error) {
	s.gotActor, s.gotCreate = actor, input
	return s.created, s.createErr
}

func (s *conversationUseCaseStub) Get(context.Context, conversation.Actor, uuid.UUID) (conversation.Conversation, error) {
	return conversation.Conversation{}, nil
}

func (s *conversationUseCaseStub) List(context.Context, conversation.Actor, conversation.ListQuery) (conversation.ListResult, error) {
	return conversation.ListResult{}, nil
}

func (s *conversationUseCaseStub) ListMessages(context.Context, conversation.Actor, uuid.UUID, conversation.MessageQuery) (conversation.MessagePage, error) {
	return conversation.MessagePage{}, nil
}

func (s *conversationUseCaseStub) AppendUserMessage(_ context.Context, actor conversation.Actor, input conversation.AppendMessageInput) (conversation.Message, error) {
	s.appendCalls++
	s.gotActor, s.gotInput = actor, input
	return s.message, s.appendErr
}
