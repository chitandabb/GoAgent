package conversationworker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

func TestWorkerAcknowledgesCompletedDuplicate(t *testing.T) {
	repository := &workerRepositoryStub{claimErr: conversation.ErrTurnAlreadyCompleted}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error) {
		t.Fatal("executor was called for completed turn")
		return conversation.ConversationTurn{}, nil
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, uuid.New()))
	if outcome.Action != ActionAck {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestWorkerRetriesFailedExecutionAfterReleasingLease(t *testing.T) {
	execution := testExecution(1)
	repository := &workerRepositoryStub{execution: execution, renewOwned: true}
	failure := testAgentRunFailure(t, errors.New("temporary model failure"))
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error) {
		return conversation.ConversationTurn{}, failure
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, execution.TurnID))
	if outcome.Action != ActionRetry || outcome.RetryDelay != 30*time.Second || repository.queueCalls != 1 ||
		repository.failure != nil {
		t.Fatalf("outcome = %+v, queueCalls = %d, failure = %+v", outcome, repository.queueCalls, repository.failure)
	}
}

func TestWorkerDeadLettersAfterRetryBudget(t *testing.T) {
	execution := testExecution(4)
	repository := &workerRepositoryStub{execution: execution, renewOwned: true}
	failure := testAgentRunFailure(t, errors.New("permanent model failure"))
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error) {
		return conversation.ConversationTurn{}, failure
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, execution.TurnID))
	if outcome.Action != ActionDeadLetter || repository.failCalls != 1 || repository.failure == nil ||
		repository.failure.ErrorType != "agent_execution_failed" {
		t.Fatalf("outcome = %+v, failCalls = %d, failure = %+v", outcome, repository.failCalls, repository.failure)
	}
}

func TestWorkerDoesNotFabricateObservationForInfrastructureFailure(t *testing.T) {
	execution := testExecution(4)
	repository := &workerRepositoryStub{execution: execution, renewOwned: true}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error) {
		return conversation.ConversationTurn{}, errors.New("database commit failed")
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, execution.TurnID))
	if outcome.Action != ActionDeadLetter || repository.failCalls != 1 || repository.failure != nil {
		t.Fatalf("outcome = %+v, failCalls = %d, failure = %+v", outcome, repository.failCalls, repository.failure)
	}
}

func TestWorkerAcknowledgesCommittedAssistantMessage(t *testing.T) {
	execution := testExecution(1)
	repository := &workerRepositoryStub{execution: execution, renewOwned: true}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error) {
		return conversation.ConversationTurn{AssistantMessage: conversation.Message{ID: uuid.New()}}, nil
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, execution.TurnID))
	if outcome.Action != ActionAck || repository.failCalls != 0 {
		t.Fatalf("outcome = %+v, failCalls = %d", outcome, repository.failCalls)
	}
}

func newTestWorker(t *testing.T, repository Repository, executor Executor) *Worker {
	t.Helper()
	worker, err := New(repository, executor, Config{
		WorkerID: "conversation-worker-test", LeaseDuration: time.Minute,
		RenewInterval: 10 * time.Second, MaxAttempts: 4,
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return worker
}

func testExecution(attempt int) conversation.TurnExecution {
	return conversation.TurnExecution{
		TurnID: uuid.New(), Actor: conversation.Actor{UserID: uuid.New()}, AttemptCount: attempt,
		Turn: conversation.ConversationTurn{UserMessage: conversation.Message{ID: uuid.New()}},
	}
}

func validIncomingMessage(t *testing.T, turnID uuid.UUID) IncomingMessage {
	t.Helper()
	messageID, correlationID := uuid.New(), uuid.New()
	body, err := json.Marshal(messageEnvelope{
		MessageID: messageID.String(), MessageType: MessageType, SchemaVersion: SchemaVersion,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationID: correlationID.String(),
		Payload: json.RawMessage(`{"turnId":"` + turnID.String() + `"}`),
	})
	if err != nil {
		t.Fatalf("marshal incoming message: %v", err)
	}
	return IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	}
}

func testAgentRunFailure(t *testing.T, cause error) error {
	t.Helper()
	record := conversation.AgentRunFailureRecord{
		Observation: conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
			Outcome: conversation.AgentRunFailed, DurationMillis: 25,
		},
		ErrorType: "agent_execution_failed",
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("failure record: %v", err)
	}
	return conversation.NewAgentRunFailure(cause, record)
}

type executorFunc func(context.Context, conversation.TurnExecution, string) (conversation.ConversationTurn, error)

func (f executorFunc) ExecuteAcceptedTurn(ctx context.Context, execution conversation.TurnExecution, workerID string) (conversation.ConversationTurn, error) {
	return f(ctx, execution, workerID)
}

type workerRepositoryStub struct {
	execution  conversation.TurnExecution
	claimErr   error
	renewOwned bool
	failErr    error
	failure    *conversation.AgentRunFailureRecord
	failCalls  int
	queueCalls int
}

func (r *workerRepositoryStub) ClaimTurn(context.Context, uuid.UUID, string, time.Time, time.Time) (conversation.TurnExecution, error) {
	return r.execution, r.claimErr
}

func (r *workerRepositoryStub) RenewTurnExecution(context.Context, uuid.UUID, string, time.Time, time.Time) (bool, error) {
	return r.renewOwned, nil
}

func (r *workerRepositoryStub) QueueTurnRetry(context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time) error {
	r.queueCalls++
	return r.failErr
}

func (r *workerRepositoryStub) FailTurnExecution(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string, failure *conversation.AgentRunFailureRecord, _ time.Time) error {
	r.failCalls++
	r.failure = failure
	return r.failErr
}
