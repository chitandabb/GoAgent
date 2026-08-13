package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/chitandabb/GoAgent/internal/semanticcache"

	"github.com/google/uuid"
)

func TestMessageQueryNormalize(t *testing.T) {
	query := MessageQuery{AfterSeq: -1, Limit: 999}
	query.Normalize()
	if query.AfterSeq != 0 || query.Limit != MaxMessageLimit {
		t.Fatalf("normalized query = %+v", query)
	}
}

func TestAgentRunFailureRetryable(t *testing.T) {
	tests := []struct {
		errorType string
		want      bool
	}{
		{errorType: AgentRunErrorTypeContextPreparationFailed, want: true},
		{errorType: " context_preparation_failed ", want: true},
		{errorType: "agent_timeout", want: false},
		{errorType: "agent_execution_failed", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.errorType, func(t *testing.T) {
			if got := AgentRunFailureRetryable(tt.errorType); got != tt.want {
				t.Fatalf("AgentRunFailureRetryable(%q) = %t, want %t", tt.errorType, got, tt.want)
			}
		})
	}
}

func TestTurnRequestFingerprintCanonicalizesReferenceOrder(t *testing.T) {
	conversationID, firstCaseID, secondCaseID := uuid.New(), uuid.New(), uuid.New()
	firstTaskID, secondTaskID := uuid.New(), uuid.New()
	left := AppendMessageInput{
		ConversationID: conversationID, Role: MessageRoleUser, Content: "同一请求",
		CaseReferences: []CaseReference{
			{ExternalCaseID: firstCaseID, Kind: ReferenceKindMentioned},
			{ExternalCaseID: secondCaseID, Kind: ReferenceKindSelected},
		},
		TaskReferences: []TaskReference{
			{TaskID: firstTaskID, Kind: ReferenceKindReferenced},
			{TaskID: secondTaskID, Kind: ReferenceKindCreated},
		},
	}
	right := left
	right.CaseReferences = []CaseReference{left.CaseReferences[1], left.CaseReferences[0]}
	right.TaskReferences = []TaskReference{left.TaskReferences[1], left.TaskReferences[0]}
	leftFingerprint, err := turnRequestFingerprint(left)
	if err != nil {
		t.Fatalf("left fingerprint: %v", err)
	}
	rightFingerprint, err := turnRequestFingerprint(right)
	if err != nil {
		t.Fatalf("right fingerprint: %v", err)
	}
	if leftFingerprint != rightFingerprint || len(leftFingerprint) != 64 {
		t.Fatalf("fingerprints left=%q right=%q", leftFingerprint, rightFingerprint)
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

func TestServiceExecuteTurnRequiresAgent(t *testing.T) {
	repository := &conversationRepositoryStub{}
	service, _ := NewService(repository)
	_, err := service.ExecuteTurn(context.Background(), Actor{UserID: uuid.New()}, uuid.NewString(), AppendMessageInput{
		ConversationID: uuid.New(), Content: "知识库如何更新？",
	})
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("ExecuteTurn() error = %v, want ErrAgentUnavailable", err)
	}
}

func TestServiceExecuteTurnPersistsAssistantAndCreatedTaskReference(t *testing.T) {
	userID, conversationID, caseID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	reportReference := ReportReference{ReferenceID: "report:" + uuid.NewString()}
	agent := &conversationAgentResponderStub{response: AgentResponse{
		Content: "  已创建异步诊断任务。  ", ReportReferences: []ReportReference{reportReference},
	}}
	agent.hook = func(ctx context.Context, request AgentRequest) error {
		commandContext, ok := CommandContextFromContext(ctx)
		if !ok || commandContext.ConversationID != conversationID || commandContext.UserMessageID != request.UserMessage.ID {
			return ErrCommandContextRequired
		}
		return repository.AppendTaskReference(ctx, userID, request.UserMessage.ID, taskID, ReferenceKindCreated, time.Now())
	}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)

	result, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID,
		Content:        "请诊断这个工单",
		CaseReferences: []CaseReference{{ExternalCaseID: caseID, Kind: ReferenceKindSelected}},
	})
	if err != nil {
		t.Fatalf("ExecuteTurn(): %v", err)
	}
	turn := result.Turn
	if turn.UserMessage.Role != MessageRoleUser || turn.AssistantMessage.Role != MessageRoleAssistant ||
		turn.AssistantMessage.Content != "已创建异步诊断任务。" {
		t.Fatalf("turn = %+v", turn)
	}
	if len(turn.AssistantMessage.TaskReferences) != 1 ||
		turn.AssistantMessage.TaskReferences[0] != (TaskReference{TaskID: taskID, Kind: ReferenceKindCreated}) {
		t.Fatalf("assistant task references = %+v", turn.AssistantMessage.TaskReferences)
	}
	if len(turn.AssistantMessage.ReportReferences) != 1 || turn.AssistantMessage.ReportReferences[0] != reportReference {
		t.Fatalf("assistant report references = %+v", turn.AssistantMessage.ReportReferences)
	}
	if len(repository.messages) != 2 || agent.calls != 1 || agent.request.UserMessage.ID != turn.UserMessage.ID {
		t.Fatalf("messages=%d agent calls=%d request=%+v", len(repository.messages), agent.calls, agent.request)
	}
}

func TestAgentResponseRejectsNonUUIDReportReference(t *testing.T) {
	response := AgentResponse{
		Content:          "报告已生成。",
		ReportReferences: []ReportReference{{ReferenceID: "report:invented"}},
	}
	if err := response.Validate(); !errors.Is(err, ErrAgentResponseInvalid) {
		t.Fatalf("Validate() error = %v, want ErrAgentResponseInvalid", err)
	}
}

func TestServiceExecuteTurnReusesUserMessageAfterAgentFailure(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agentFailure := errors.New("model unavailable")
	service, _ := NewService(repository)
	agent := &conversationAgentResponderStub{err: agentFailure}
	service, _ = service.WithAgentResponder(agent)
	idempotencyKey := uuid.NewString()

	first, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, idempotencyKey, AppendMessageInput{
		ConversationID: conversationID, Content: "知识库如何更新？",
	})
	if !errors.Is(err, agentFailure) {
		t.Fatalf("first ExecuteTurn() error = %v, want agent failure", err)
	}
	if first.Turn.UserMessage.ID == uuid.Nil || len(repository.messages) != 1 || repository.failCalls != 1 {
		t.Fatalf("first=%+v messages=%+v failCalls=%d", first, repository.messages, repository.failCalls)
	}
	agent.err = nil
	agent.response = AgentResponse{Content: "采用不可变版本重新发布。"}
	second, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, idempotencyKey, AppendMessageInput{
		ConversationID: conversationID, Content: "知识库如何更新？",
	})
	if err != nil {
		t.Fatalf("second ExecuteTurn(): %v", err)
	}
	if second.Created || second.Turn.UserMessage.ID != first.Turn.UserMessage.ID || len(repository.messages) != 2 {
		t.Fatalf("second=%+v messages=%+v", second, repository.messages)
	}
}

func TestServiceExecuteTurnMarksFailureAfterRequestContextCancellation(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(&conversationAgentResponderStub{err: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.ExecuteTurn(ctx, Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID, Content: "知识库如何更新？",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteTurn() error = %v, want context.Canceled", err)
	}
	if repository.failCalls != 1 || repository.failContextErr != nil {
		t.Fatalf("failCalls=%d failContextErr=%v", repository.failCalls, repository.failContextErr)
	}
}

func TestServiceExecuteTurnReplaysCompletedResponseWithoutCallingAgent(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agent := &conversationAgentResponderStub{response: AgentResponse{Content: "第一次回答"}}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)
	key := uuid.NewString()
	input := AppendMessageInput{ConversationID: conversationID, Content: "同一个问题"}
	first, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, key, input)
	if err != nil {
		t.Fatalf("first ExecuteTurn(): %v", err)
	}
	second, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, key, input)
	if err != nil {
		t.Fatalf("second ExecuteTurn(): %v", err)
	}
	if !first.Created || !second.Replayed || second.Created || agent.calls != 1 ||
		second.Turn.AssistantMessage.ID != first.Turn.AssistantMessage.ID {
		t.Fatalf("first=%+v second=%+v agent calls=%d", first, second, agent.calls)
	}
}

func TestServiceExecuteTurnCommitsExactCacheHitWithoutCallingAgent(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	versionID, chunkID, sourceRunID := uuid.New(), uuid.New(), uuid.New()
	sourceRef := "knowledge:" + versionID.String() + "/" + chunkID.String()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agent := &conversationAgentResponderStub{err: errors.New("agent must not run on cache hit")}
	cache := &semanticAnswerCacheStub{answer: semanticcache.Answer{
		Content: "设备点检周期为 30 天。[source:" + sourceRef + "]",
		Citations: []semanticcache.Source{{
			Position: 0, SourceType: string(CitationSourceKnowledgeChunk),
			SourceRef: sourceRef, ContentSHA256: strings.Repeat("a", 64),
		}},
		RetrievedSources: []semanticcache.Source{{
			SourceType: string(CitationSourceKnowledgeChunk),
			SourceRef:  sourceRef, ContentSHA256: strings.Repeat("a", 64),
		}},
		SourceRunID: sourceRunID, ModelProvider: "stepfun", ModelID: "step-3.5-flash",
		PromptVersion: "conversation-v1", Generation: 7,
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour),
	}, hit: true}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)
	service, _ = service.WithSemanticAnswerCache(cache, SemanticAnswerCacheConfig{
		TTL: time.Hour, LookupTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond,
		MaxAnswerBytes: semanticcache.MaxAnswerBytes, MaxCitations: semanticcache.MaxCitations,
	}, nil)

	result, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID, Content: "设备点检周期规范是什么？",
	})
	if err != nil {
		t.Fatalf("ExecuteTurn(): %v", err)
	}
	if agent.calls != 0 || !cache.lookupCalled {
		t.Fatalf("agent calls=%d cache lookup=%v", agent.calls, cache.lookupCalled)
	}
	if result.Turn.AssistantMessage.Content != cache.answer.Content ||
		len(result.Turn.AssistantMessage.Citations) != 1 {
		t.Fatalf("assistant = %+v", result.Turn.AssistantMessage)
	}
	observation := repository.completedResponse.RunObservation
	if observation == nil || observation.ExecutionPath != AgentRunExecutionSemanticCacheHit ||
		observation.CacheLayer != AgentRunCacheLayerExact || observation.SourceRunID != sourceRunID ||
		observation.Usage.ModelCalls != 0 || observation.ToolCalls != 0 {
		t.Fatalf("cache observation = %+v", observation)
	}
}

func TestServiceExecuteTurnWritesEligibleAnswerAfterCommit(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	versionID, chunkID := uuid.New(), uuid.New()
	sourceRef := "knowledge:" + versionID.String() + "/" + chunkID.String()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	observation := &AgentRunObservation{
		ModelProvider: "stepfun", ModelID: "step-3.5-flash", PromptVersion: "conversation-v1",
		Outcome: AgentRunAnswered, AnswerCacheEligible: true, ToolCalls: 1,
		Usage: AgentRunUsage{ModelCalls: 1, TotalTokens: 20},
		RetrievedSources: []AgentRunSource{{
			SourceType: CitationSourceKnowledgeChunk, SourceRef: sourceRef, ContentSHA256: strings.Repeat("b", 64),
		}},
	}
	agent := &conversationAgentResponderStub{response: AgentResponse{
		Content: "使用新版本发布流程。[source:" + sourceRef + "]",
		Citations: []MessageCitation{{
			Position: 0, SourceType: CitationSourceKnowledgeChunk,
			SourceRef: sourceRef, ContentSHA256: strings.Repeat("b", 64),
		}},
		RunObservation: observation,
	}}
	cache := &semanticAnswerCacheStub{}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)
	service, _ = service.WithSemanticAnswerCache(cache, SemanticAnswerCacheConfig{
		TTL: time.Hour, LookupTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond,
		MaxAnswerBytes: semanticcache.MaxAnswerBytes, MaxCitations: semanticcache.MaxCitations,
	}, nil)

	result, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID, Content: "知识文档如何发布新版本？",
	})
	if err != nil {
		t.Fatalf("ExecuteTurn(): %v", err)
	}
	if agent.calls != 1 || len(cache.puts) != 1 {
		t.Fatalf("agent calls=%d cache puts=%d", agent.calls, len(cache.puts))
	}
	put := cache.puts[0]
	if put.Answer.SourceRunID != result.TurnID || put.Answer.Content != agent.response.Content ||
		put.TTL != time.Hour || put.Answer.Generation != 0 {
		t.Fatalf("cache put = %+v", put)
	}
}

func TestServiceExecuteTurnFallsBackAndObservesCacheLookupFailure(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	agent := &conversationAgentResponderStub{response: AgentResponse{Content: "正常 RAG 回答"}}
	cache := &semanticAnswerCacheStub{lookupErr: errors.New("cache unavailable")}
	var events []resilience.DegradationEvent
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)
	service, _ = service.WithSemanticAnswerCache(cache, SemanticAnswerCacheConfig{
		TTL: time.Hour, LookupTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond,
		MaxAnswerBytes: semanticcache.MaxAnswerBytes, MaxCitations: semanticcache.MaxCitations,
	}, resilience.ObserverFunc(func(event resilience.DegradationEvent) { events = append(events, event) }))

	result, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID, Content: "设备点检周期规范是什么？",
	})
	if err != nil {
		t.Fatalf("ExecuteTurn(): %v", err)
	}
	if agent.calls != 1 || result.Turn.AssistantMessage.Content != "正常 RAG 回答" || len(events) != 1 {
		t.Fatalf("agent calls=%d result=%+v events=%+v", agent.calls, result, events)
	}
	if events[0].Operation != "semantic_cache_lookup" || events[0].Fallback != "normal_rag" ||
		events[0].ReasonCode != "provider_error" {
		t.Fatalf("degradation event = %+v", events[0])
	}
}

func TestServiceExecuteTurnRejectsIdempotencyKeyReuseWithDifferentRequest(t *testing.T) {
	userID, conversationID := uuid.New(), uuid.New()
	repository := &turnRepositoryStub{
		conversation: Conversation{ID: conversationID, UserID: userID, Status: StatusActive},
	}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(&conversationAgentResponderStub{response: AgentResponse{Content: "回答"}})
	key := uuid.NewString()
	if _, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, key, AppendMessageInput{
		ConversationID: conversationID, Content: "问题一",
	}); err != nil {
		t.Fatalf("first ExecuteTurn(): %v", err)
	}
	_, err := service.ExecuteTurn(context.Background(), Actor{UserID: userID}, key, AppendMessageInput{
		ConversationID: conversationID, Content: "问题二",
	})
	if !errors.Is(err, ErrTurnIdempotencyConflict) {
		t.Fatalf("second ExecuteTurn() error = %v, want ErrTurnIdempotencyConflict", err)
	}
}

func TestServiceAcceptTurnQueuesWithoutCallingAgent(t *testing.T) {
	userID, conversationID, turnID, userMessageID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repository := &asyncTurnRepositoryStub{
		turnRepositoryStub: &turnRepositoryStub{conversation: Conversation{
			ID: conversationID, UserID: userID, Status: StatusActive,
		}},
		accepted: BeginTurnResult{
			TurnID: turnID, Status: TurnStatusQueued, Created: true,
			UserMessage: Message{
				ID: userMessageID, ConversationID: conversationID, Seq: 1,
				Role: MessageRoleUser, Content: "诊断这个工单",
			},
		},
	}
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	result, err := service.AcceptTurn(context.Background(), Actor{UserID: userID}, uuid.NewString(), AppendMessageInput{
		ConversationID: conversationID, Content: "诊断这个工单",
	})
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	if result.TurnID != turnID || result.Status != TurnStatusQueued || !result.Created || result.Turn.UserMessage.ID != userMessageID {
		t.Fatalf("result = %+v", result)
	}
	if repository.gotAccept.ExecutionMode != TurnExecutionAsynchronous || repository.gotAccept.CorrelationID == uuid.Nil ||
		!repository.gotAccept.LeaseExpiresAt.IsZero() {
		t.Fatalf("AcceptTurn input = %+v", repository.gotAccept)
	}
}

func TestServiceExecuteAcceptedTurnUsesDurableCommandContextAndCompletesLease(t *testing.T) {
	userID, conversationID, turnID, userMessageID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	userMessage := Message{
		ID: userMessageID, ConversationID: conversationID, Seq: 1,
		Role: MessageRoleUser, Content: "诊断当前工单",
	}
	assistantMessage := Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 2,
		Role: MessageRoleAssistant, Content: "诊断任务已创建。",
	}
	repository := &asyncTurnRepositoryStub{
		turnRepositoryStub: &turnRepositoryStub{conversation: Conversation{
			ID: conversationID, UserID: userID, Status: StatusActive,
		}},
		completed: ConversationTurn{UserMessage: userMessage, AssistantMessage: assistantMessage},
	}
	sourceRef := "https://example.com/diagnosis-guide"
	agent := &conversationAgentResponderStub{response: AgentResponse{
		Content: "  诊断任务已创建。[source:" + sourceRef + "]  ",
		Citations: []MessageCitation{{
			Position: 0, SourceType: CitationSourceWeb,
			SourceRef: sourceRef, ContentSHA256: strings.Repeat("a", 64),
		}},
	}}
	agent.hook = func(ctx context.Context, request AgentRequest) error {
		command, ok := CommandContextFromContext(ctx)
		if !ok || command.ConversationID != conversationID || command.UserMessageID != userMessageID ||
			command.Actor.UserID != userID || request.UserMessage.ID != userMessageID {
			t.Fatalf("command=%+v ok=%v request=%+v", command, ok, request)
		}
		return nil
	}
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	if _, err := service.WithAgentResponder(agent); err != nil {
		t.Fatalf("WithAgentResponder(): %v", err)
	}
	execution := TurnExecution{
		TurnID: turnID, Turn: ConversationTurn{UserMessage: userMessage},
		Conversation: repository.conversation, Actor: Actor{UserID: userID},
		History: []Message{userMessage}, AttemptCount: 1,
	}
	completed, err := service.ExecuteAcceptedTurn(context.Background(), execution, "conversation-worker-test")
	if err != nil {
		t.Fatalf("ExecuteAcceptedTurn(): %v", err)
	}
	if completed.AssistantMessage.ID != assistantMessage.ID ||
		repository.completedContent != "诊断任务已创建。[source:"+sourceRef+"]" ||
		len(repository.completedCitations) != 1 || repository.completedCitations[0].SourceRef != sourceRef ||
		repository.completedWorker != "conversation-worker-test" {
		t.Fatalf("completed=%+v content=%q worker=%q", completed, repository.completedContent, repository.completedWorker)
	}
}

func TestServiceExecuteAcceptedTurnUsesExactCacheBeforeAgent(t *testing.T) {
	userID, conversationID, turnID, userMessageID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	versionID, chunkID, sourceRunID := uuid.New(), uuid.New(), uuid.New()
	sourceRef := "knowledge:" + versionID.String() + "/" + chunkID.String()
	userMessage := Message{
		ID: userMessageID, ConversationID: conversationID, Seq: 1,
		Role: MessageRoleUser, Content: "设备点检周期规范是什么？",
	}
	repository := &asyncTurnRepositoryStub{
		turnRepositoryStub: &turnRepositoryStub{conversation: Conversation{
			ID: conversationID, UserID: userID, Status: StatusActive,
		}},
		completed: ConversationTurn{UserMessage: userMessage, AssistantMessage: Message{
			ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: MessageRoleAssistant,
		}},
	}
	cache := &semanticAnswerCacheStub{hit: true, answer: semanticcache.Answer{
		Content: "设备点检周期为 30 天。[source:" + sourceRef + "]",
		Citations: []semanticcache.Source{{
			Position: 0, SourceType: string(CitationSourceKnowledgeChunk),
			SourceRef: sourceRef, ContentSHA256: strings.Repeat("c", 64),
		}},
		RetrievedSources: []semanticcache.Source{{
			SourceType: string(CitationSourceKnowledgeChunk),
			SourceRef:  sourceRef, ContentSHA256: strings.Repeat("c", 64),
		}},
		SourceRunID: sourceRunID, ModelProvider: "stepfun", ModelID: "step-3.5-flash",
		PromptVersion: "conversation-v1", Generation: 2,
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour),
	}}
	agent := &conversationAgentResponderStub{err: errors.New("agent must not run")}
	service, _ := NewService(repository)
	service, _ = service.WithAgentResponder(agent)
	service, _ = service.WithSemanticAnswerCache(cache, SemanticAnswerCacheConfig{
		TTL: time.Hour, LookupTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond,
		MaxAnswerBytes: semanticcache.MaxAnswerBytes, MaxCitations: semanticcache.MaxCitations,
	}, nil)
	execution := TurnExecution{
		TurnID: turnID, Turn: ConversationTurn{UserMessage: userMessage},
		Conversation: repository.conversation, Actor: Actor{UserID: userID},
		History: []Message{userMessage}, AttemptCount: 1,
	}

	if _, err := service.ExecuteAcceptedTurn(context.Background(), execution, "conversation-worker-test"); err != nil {
		t.Fatalf("ExecuteAcceptedTurn(): %v", err)
	}
	if agent.calls != 0 || repository.completedObservation == nil ||
		repository.completedObservation.ExecutionPath != AgentRunExecutionSemanticCacheHit {
		t.Fatalf("agent calls=%d observation=%+v", agent.calls, repository.completedObservation)
	}
}

func TestResolveAnswerCitationsRejectsUnknownAndDeduplicatesRepeatedMarkers(t *testing.T) {
	knowledgeRef := "knowledge:" + uuid.NewString() + "/" + uuid.NewString()
	attachmentRef := "attachment:" + uuid.NewString()
	available := []MessageCitation{
		{SourceType: CitationSourceKnowledgeChunk, SourceRef: knowledgeRef, ContentSHA256: strings.Repeat("a", 64)},
		{SourceType: CitationSourceAttachment, SourceRef: attachmentRef, ContentSHA256: strings.Repeat("b", 64)},
	}
	resolved, err := ResolveAnswerCitations(
		"先看知识 [source:"+knowledgeRef+"]，附件也一致 [source:"+attachmentRef+"]，再次引用 [source:"+knowledgeRef+"]。",
		available,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0].Position != 0 || resolved[0].SourceRef != knowledgeRef ||
		resolved[1].Position != 1 || resolved[1].SourceRef != attachmentRef {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, err := ResolveAnswerCitations("伪造 [source:https://example.com/unknown]", available); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unknown source error = %v", err)
	}
	if _, err := ResolveAnswerCitations("损坏 [source:"+knowledgeRef, available); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("malformed source error = %v", err)
	}
}

func TestFormatAnswerCitationMarkerUsesValidatedIdentity(t *testing.T) {
	citation := MessageCitation{
		SourceType:    CitationSourceKnowledgeChunk,
		SourceRef:     "knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222",
		ContentSHA256: strings.Repeat("a", 64),
	}
	marker, err := FormatAnswerCitationMarker(citation)
	if err != nil {
		t.Fatal(err)
	}
	if marker != "[source:"+citation.SourceRef+"]" {
		t.Fatalf("marker = %q", marker)
	}
	citation.SourceRef = "knowledge:invalid"
	if _, err := FormatAnswerCitationMarker(citation); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("invalid citation error = %v", err)
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
	conversation      Conversation
	messages          []Message
	turns             map[string]*turnRepositoryState
	completedResponse AgentResponse
	failCalls         int
	failContextErr    error
}

type turnRepositoryState struct {
	id          uuid.UUID
	fingerprint string
	userMessage Message
	assistant   *Message
	running     bool
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
		CaseReferences:   append([]CaseReference(nil), input.CaseReferences...),
		TaskReferences:   append([]TaskReference(nil), input.TaskReferences...),
		ReportReferences: append([]ReportReference(nil), input.ReportReferences...),
		Citations:        append([]MessageCitation(nil), input.Citations...), CreatedAt: createdAt,
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

func (s *turnRepositoryStub) BeginTurn(_ context.Context, userID uuid.UUID, input BeginTurnInput) (BeginTurnResult, error) {
	if s.conversation.UserID != userID || s.conversation.ID != input.Message.ConversationID {
		return BeginTurnResult{}, repository.ErrNotFound
	}
	if s.turns == nil {
		s.turns = make(map[string]*turnRepositoryState)
	}
	if current, ok := s.turns[input.IdempotencyKey]; ok {
		if current.fingerprint != input.RequestFingerprint {
			return BeginTurnResult{}, ErrTurnIdempotencyConflict
		}
		if current.assistant != nil {
			assistant := *current.assistant
			return BeginTurnResult{
				TurnID: current.id, UserMessage: current.userMessage, AssistantMessage: &assistant,
			}, nil
		}
		if current.running {
			return BeginTurnResult{}, ErrTurnInProgress
		}
		current.running = true
		return BeginTurnResult{TurnID: current.id, UserMessage: current.userMessage}, nil
	}
	message, err := s.AppendMessage(context.Background(), userID, input.Message, input.StartedAt)
	if err != nil {
		return BeginTurnResult{}, err
	}
	state := &turnRepositoryState{
		id: uuid.New(), fingerprint: input.RequestFingerprint, userMessage: message, running: true,
	}
	s.turns[input.IdempotencyKey] = state
	return BeginTurnResult{TurnID: state.id, UserMessage: message, Created: true}, nil
}

func (s *turnRepositoryStub) CompleteTurn(_ context.Context, userID, turnID uuid.UUID, response AgentResponse, completedAt time.Time) (ConversationTurn, error) {
	s.completedResponse = response
	for _, state := range s.turns {
		if state.id != turnID {
			continue
		}
		if state.assistant != nil {
			return ConversationTurn{UserMessage: state.userMessage, AssistantMessage: *state.assistant}, nil
		}
		if !state.running {
			return ConversationTurn{}, ErrTurnLeaseLost
		}
		userMessage, err := s.GetMessage(context.Background(), userID, s.conversation.ID, state.userMessage.ID)
		if err != nil {
			return ConversationTurn{}, err
		}
		created := make([]TaskReference, 0, len(userMessage.TaskReferences))
		for _, reference := range userMessage.TaskReferences {
			if reference.Kind == ReferenceKindCreated {
				created = append(created, reference)
			}
		}
		assistant, err := s.AppendMessage(context.Background(), userID, AppendMessageInput{
			ConversationID: s.conversation.ID, Role: MessageRoleAssistant,
			Content: response.Content, TaskReferences: created,
			ReportReferences: response.ReportReferences, Citations: response.Citations,
		}, completedAt)
		if err != nil {
			return ConversationTurn{}, err
		}
		state.assistant = &assistant
		state.running = false
		return ConversationTurn{UserMessage: userMessage, AssistantMessage: assistant}, nil
	}
	return ConversationTurn{}, repository.ErrNotFound
}

type semanticAnswerCacheStub struct {
	answer       semanticcache.Answer
	hit          bool
	lookupErr    error
	putErr       error
	lookupCalled bool
	puts         []semanticcache.PutInput
}

func (s *semanticAnswerCacheStub) Lookup(_ context.Context, _ semanticcache.LookupInput) (semanticcache.Answer, bool, error) {
	s.lookupCalled = true
	return s.answer, s.hit, s.lookupErr
}

func (s *semanticAnswerCacheStub) Put(_ context.Context, input semanticcache.PutInput) error {
	s.puts = append(s.puts, input)
	return s.putErr
}

func (s *turnRepositoryStub) FailTurn(ctx context.Context, _ uuid.UUID, turnID uuid.UUID, _ time.Time) error {
	for _, state := range s.turns {
		if state.id == turnID {
			state.running = false
			s.failCalls++
			s.failContextErr = ctx.Err()
			return nil
		}
	}
	return repository.ErrNotFound
}

func (s *turnRepositoryStub) GetTurn(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (TurnDetail, error) {
	return TurnDetail{}, nil
}

func (s *turnRepositoryStub) ListTurnEvents(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, int) (TurnEventPage, error) {
	return TurnEventPage{}, nil
}

type asyncTurnRepositoryStub struct {
	*turnRepositoryStub
	accepted             BeginTurnResult
	gotAccept            BeginTurnInput
	completed            ConversationTurn
	completedContent     string
	completedCitations   []MessageCitation
	completedObservation *AgentRunObservation
	completedWorker      string
}

func (s *asyncTurnRepositoryStub) AcceptTurn(_ context.Context, _ uuid.UUID, input BeginTurnInput) (BeginTurnResult, error) {
	s.gotAccept = input
	return s.accepted, nil
}

func (s *asyncTurnRepositoryStub) ClaimTurn(context.Context, uuid.UUID, string, time.Time, time.Time) (TurnExecution, error) {
	return TurnExecution{}, nil
}

func (s *asyncTurnRepositoryStub) RenewTurnExecution(context.Context, uuid.UUID, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (s *asyncTurnRepositoryStub) QueueTurnRetry(context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time) error {
	return nil
}

func (s *asyncTurnRepositoryStub) CompleteTurnExecution(_ context.Context, _ uuid.UUID, _ uuid.UUID, workerID string, response AgentResponse, _ time.Time) (ConversationTurn, error) {
	s.completedWorker = workerID
	s.completedContent = response.Content
	s.completedCitations = append([]MessageCitation(nil), response.Citations...)
	s.completedObservation = response.RunObservation
	return s.completed, nil
}

func (s *asyncTurnRepositoryStub) FailTurnExecution(context.Context, uuid.UUID, uuid.UUID, string, *AgentRunFailureRecord, time.Time) error {
	return nil
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

func (s *conversationRepositoryStub) BeginTurn(context.Context, uuid.UUID, BeginTurnInput) (BeginTurnResult, error) {
	return BeginTurnResult{}, nil
}

func (s *conversationRepositoryStub) CompleteTurn(context.Context, uuid.UUID, uuid.UUID, AgentResponse, time.Time) (ConversationTurn, error) {
	return ConversationTurn{}, nil
}

func (s *conversationRepositoryStub) FailTurn(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}

func (s *conversationRepositoryStub) GetTurn(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (TurnDetail, error) {
	return TurnDetail{}, nil
}

func (s *conversationRepositoryStub) ListTurnEvents(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, int) (TurnEventPage, error) {
	return TurnEventPage{}, nil
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
	userID, conversationID, messageID, caseID, taskID, attachmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := Message{
		ID: messageID, ConversationID: conversationID, Role: MessageRoleUser,
		Content: "请诊断这个工单", CaseReferences: []CaseReference{{ExternalCaseID: caseID, Kind: ReferenceKindSelected}},
		Attachments: []MessageAttachment{{AttachmentID: attachmentID, Purpose: "log_file", OriginalName: "error.log"}},
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
	if len(taskCreator.input.Attachments) != 1 || taskCreator.input.Attachments[0].AttachmentID != attachmentID ||
		taskCreator.input.Attachments[0].Purpose != "log_file" || taskCreator.input.AttachmentSource == nil ||
		taskCreator.input.AttachmentSource.ConversationID != conversationID || taskCreator.input.AttachmentSource.MessageID != messageID {
		t.Fatalf("task attachment input=%+v source=%+v", taskCreator.input.Attachments, taskCreator.input.AttachmentSource)
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

func TestGetDiagnosisTaskStatusRequiresLatestReferencedTask(t *testing.T) {
	userID, conversationID, messageID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := Message{
		ID: messageID, ConversationID: conversationID, Role: MessageRoleUser, Content: "这个任务进度如何",
		TaskReferences: []TaskReference{{TaskID: taskID, Kind: ReferenceKindReferenced}},
	}
	repositoryStub := &commandRepositoryStub{message: message, latest: message}
	taskReader := &commandTaskReaderStub{task: diagnosis.DiagnosisTask{ID: taskID, CreatedBy: userID, Status: diagnosis.TaskRunning}}
	service := newCommandService(
		t, repositoryStub, &commandTaskCreatorStub{}, taskReader, &commandCaseReaderStub{},
	)
	ctx := WithCommandContext(context.Background(), CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: Actor{UserID: userID},
	})

	result, err := service.GetDiagnosisTaskStatus(ctx, taskID)
	if err != nil {
		t.Fatalf("GetDiagnosisTaskStatus(): %v", err)
	}
	if result.Task.ID != taskID || taskReader.calls != 1 || taskReader.actor.UserID != userID {
		t.Fatalf("result=%+v reader=%+v", result, taskReader)
	}
}

func TestGetDiagnosisTaskStatusRejectsUnreferencedTask(t *testing.T) {
	userID, conversationID, messageID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message := Message{ID: messageID, ConversationID: conversationID, Role: MessageRoleUser, Content: "查一下进度"}
	repositoryStub := &commandRepositoryStub{message: message, latest: message}
	taskReader := &commandTaskReaderStub{}
	service := newCommandService(
		t, repositoryStub, &commandTaskCreatorStub{}, taskReader, &commandCaseReaderStub{},
	)
	ctx := WithCommandContext(context.Background(), CommandContext{
		ConversationID: conversationID, UserMessageID: messageID, Actor: Actor{UserID: userID},
	})

	_, err := service.GetDiagnosisTaskStatus(ctx, taskID)
	if !errors.Is(err, ErrTaskReferenceRequired) {
		t.Fatalf("GetDiagnosisTaskStatus() error = %v, want ErrTaskReferenceRequired", err)
	}
	if taskReader.calls != 0 {
		t.Fatalf("task reader calls = %d, want 0", taskReader.calls)
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
func (s *commandRepositoryStub) BeginTurn(context.Context, uuid.UUID, BeginTurnInput) (BeginTurnResult, error) {
	return BeginTurnResult{}, nil
}
func (s *commandRepositoryStub) CompleteTurn(context.Context, uuid.UUID, uuid.UUID, AgentResponse, time.Time) (ConversationTurn, error) {
	return ConversationTurn{}, nil
}
func (s *commandRepositoryStub) FailTurn(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (s *commandRepositoryStub) GetTurn(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (TurnDetail, error) {
	return TurnDetail{}, nil
}
func (s *commandRepositoryStub) ListTurnEvents(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, int) (TurnEventPage, error) {
	return TurnEventPage{}, nil
}

type commandTaskCreatorStub struct {
	result diagnosis.TaskCreateResult
	input  diagnosis.CreateTaskInput
}

func (s *commandTaskCreatorStub) Create(_ context.Context, _ diagnosis.TaskActor, input diagnosis.CreateTaskInput) (diagnosis.TaskCreateResult, error) {
	s.input = input
	return s.result, nil
}

type commandTaskReaderStub struct {
	task   diagnosis.DiagnosisTask
	actor  diagnosis.TaskActor
	taskID uuid.UUID
	calls  int
}

func (s *commandTaskReaderStub) Get(_ context.Context, actor diagnosis.TaskActor, taskID uuid.UUID) (diagnosis.DiagnosisTask, error) {
	s.calls++
	s.actor = actor
	s.taskID = taskID
	return s.task, nil
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
