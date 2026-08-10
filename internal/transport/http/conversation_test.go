package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	routes, err := NewConversationRoutes(context.Background(), useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
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
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
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
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
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

func TestConversationRoutesAppendTurnReturnsAcceptedQueueState(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	turnID, userMessageID := uuid.New(), uuid.New()
	idempotencyKey := uuid.NewString()
	useCase := &conversationUseCaseStub{turnResult: conversation.ConversationTurnResult{
		TurnID: turnID, Status: conversation.TurnStatusQueued, Created: true,
		Turn: conversation.ConversationTurn{UserMessage: conversation.Message{
			ID: userMessageID, ConversationID: conversationID, Seq: 5,
			Role: conversation.MessageRoleUser, Content: "MESGuard 的知识库怎么更新？",
		}},
	}}
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversationID.String()+"/turns", strings.NewReader(`{
"content":"MESGuard 的知识库怎么更新？"
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted ||
		!strings.Contains(response.Body.String(), `"turnId":"`+turnID.String()) ||
		!strings.Contains(response.Body.String(), `"status":"queued"`) ||
		!strings.Contains(response.Body.String(), `"userMessage":{"id":"`+userMessageID.String()) ||
		strings.Contains(response.Body.String(), `"assistantMessage"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if useCase.turnCalls != 1 || useCase.gotInput.ConversationID != conversationID ||
		useCase.gotIdempotencyKey != idempotencyKey {
		t.Fatalf("turn calls=%d key=%q input=%+v", useCase.turnCalls, useCase.gotIdempotencyKey, useCase.gotInput)
	}
}

func TestConversationRoutesAppendTurnRequiresIdempotencyKey(t *testing.T) {
	useCase := &conversationUseCaseStub{}
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+uuid.NewString()+"/turns", strings.NewReader(`{
"content":"知识库如何更新？"
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || useCase.turnCalls != 0 ||
		!strings.Contains(response.Body.String(), `"field":"Idempotency-Key"`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, useCase.turnCalls, response.Body.String())
	}
}

func TestConversationRoutesAppendTurnReturnsOKForReplay(t *testing.T) {
	conversationID := uuid.New()
	turnID := uuid.New()
	citationRef := "https://docs.example.com/runbook"
	useCase := &conversationUseCaseStub{turnResult: conversation.ConversationTurnResult{
		TurnID: turnID, Status: conversation.TurnStatusCompleted, Replayed: true,
		Turn: conversation.ConversationTurn{
			UserMessage: conversation.Message{
				ID: uuid.New(), ConversationID: conversationID, Seq: 1,
				Role: conversation.MessageRoleUser, Content: "问题",
			},
			AssistantMessage: conversation.Message{
				ID: uuid.New(), ConversationID: conversationID, Seq: 2,
				Role: conversation.MessageRoleAssistant, Content: "回答[source:" + citationRef + "]",
				Citations: []conversation.MessageCitation{{
					Position: 0, SourceType: conversation.CitationSourceWeb,
					SourceRef: citationRef, ContentSHA256: strings.Repeat("a", 64),
				}},
			},
		},
	}}
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(uuid.New(), false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/"+conversationID.String()+"/turns", strings.NewReader(`{"content":"问题"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"replayed":true`) ||
		!strings.Contains(response.Body.String(), `"citations":[{"position":0,"sourceType":"web","sourceRef":"`+citationRef+`"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationRoutesGetsTurnStatusWithoutExecutionInternals(t *testing.T) {
	userID, conversationID, turnID := uuid.New(), uuid.New(), uuid.New()
	userMessageID, assistantMessageID := uuid.New(), uuid.New()
	createdAt := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	useCase := &conversationUseCaseStub{turnDetail: conversation.TurnDetail{
		ID: turnID, ConversationID: conversationID, Status: conversation.TurnStatusCompleted,
		UserMessageID: userMessageID, AssistantMessageID: &assistantMessageID, AttemptCount: 2,
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute), CompletedAt: timePointer(createdAt.Add(time.Minute)),
	}}
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/turns/"+turnID.String(), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"status":"completed"`) ||
		!strings.Contains(body, `"assistantMessageId":"`+assistantMessageID.String()) ||
		strings.Contains(body, "leaseOwner") || strings.Contains(body, "requestFingerprint") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if useCase.gotActor.UserID != userID || useCase.gotConversationID != conversationID || useCase.gotTurnID != turnID {
		t.Fatalf("actor=%+v conversationID=%s turnID=%s", useCase.gotActor, useCase.gotConversationID, useCase.gotTurnID)
	}
}

func TestConversationRoutesStreamsTurnEventsAndHonorsLastEventID(t *testing.T) {
	userID, conversationID, turnID := uuid.New(), uuid.New(), uuid.New()
	stream := &conversationTurnEventStreamStub{
		initialStatus: conversation.TurnStatusCompleted,
		pages: []conversation.TurnEventPage{{
			Items: []conversation.TurnEvent{{
				TurnID: turnID, ConversationID: conversationID, Seq: 4,
				EventType: conversation.TurnEventCompleted,
				Payload:   map[string]any{"assistantMessageId": uuid.NewString()}, PayloadSchemaVersion: 1,
				CreatedAt: time.Date(2026, 8, 8, 8, 30, 0, 0, time.UTC),
			}},
			AfterSeq: 3, NextAfterSeq: 4,
		}},
	}
	useCase := &conversationUseCaseStub{eventStream: stream}
	routes, _ := NewConversationRoutes(context.Background(), useCase, identityMiddleware(userID, false), func(c *gin.Context) { c.Next() })
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/turns/"+turnID.String()+"/events?afterSeq=1&limit=25", nil)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "3")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	for _, expected := range []string{
		"retry: 3000\n\n", "id: 4\n", "event: turn_completed\n",
		`"seq":4`, `"eventType":"turn_completed"`, `"createdAt":"2026-08-08T08:30:00Z"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || len(stream.gotAfterSeq) != 1 || stream.gotAfterSeq[0] != 3 || stream.gotLimit[0] != 25 {
		t.Fatalf("status=%d cursors=%v limits=%v body=%s", response.Code, stream.gotAfterSeq, stream.gotLimit, body)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

type conversationUseCaseStub struct {
	created           conversation.Conversation
	message           conversation.Message
	turnResult        conversation.ConversationTurnResult
	createErr         error
	appendErr         error
	gotActor          conversation.Actor
	gotCreate         conversation.CreateInput
	gotInput          conversation.AppendMessageInput
	gotIdempotencyKey string
	appendCalls       int
	turnCalls         int
	turnDetail        conversation.TurnDetail
	eventPage         conversation.TurnEventPage
	eventStream       conversation.TurnEventStream
	gotConversationID uuid.UUID
	gotTurnID         uuid.UUID
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

func (s *conversationUseCaseStub) AcceptTurn(_ context.Context, actor conversation.Actor, idempotencyKey string, input conversation.AppendMessageInput) (conversation.ConversationTurnResult, error) {
	s.turnCalls++
	s.gotActor, s.gotInput = actor, input
	s.gotIdempotencyKey = idempotencyKey
	return s.turnResult, s.appendErr
}

func (s *conversationUseCaseStub) GetTurn(_ context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID) (conversation.TurnDetail, error) {
	s.gotActor, s.gotConversationID, s.gotTurnID = actor, conversationID, turnID
	return s.turnDetail, nil
}

func (s *conversationUseCaseStub) ListTurnEvents(_ context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID, afterSeq int64, limit int) (conversation.TurnEventPage, error) {
	s.gotActor, s.gotConversationID, s.gotTurnID = actor, conversationID, turnID
	return s.eventPage, nil
}

func (s *conversationUseCaseStub) OpenTurnEventStream(_ context.Context, actor conversation.Actor, conversationID, turnID uuid.UUID) (conversation.TurnEventStream, error) {
	s.gotActor, s.gotConversationID, s.gotTurnID = actor, conversationID, turnID
	return s.eventStream, nil
}

type conversationTurnEventStreamStub struct {
	initialStatus conversation.TurnStatus
	pages         []conversation.TurnEventPage
	gotAfterSeq   []int64
	gotLimit      []int
}

func (s *conversationTurnEventStreamStub) InitialStatus() conversation.TurnStatus {
	return s.initialStatus
}

func (s *conversationTurnEventStreamStub) Next(_ context.Context, afterSeq int64, limit int) (conversation.TurnEventPage, error) {
	s.gotAfterSeq = append(s.gotAfterSeq, afterSeq)
	s.gotLimit = append(s.gotLimit, limit)
	if len(s.pages) == 0 {
		return conversation.TurnEventPage{AfterSeq: afterSeq, NextAfterSeq: afterSeq}, nil
	}
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}
