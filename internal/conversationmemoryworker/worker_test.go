package conversationmemoryworker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
)

func TestWorkerCompletesSoftCompactionAndAcknowledgesMessage(t *testing.T) {
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	jobID, conversationID, snapshotID := uuid.New(), uuid.New(), uuid.New()
	lease := Lease{
		JobID: jobID, ConversationID: conversationID, ClaimOwner: "memory-worker-test",
		AttemptCount: 1, MaxAttempts: 3, FencingToken: 7, LeaseUntil: now.Add(time.Minute),
	}
	repository := &workerRepositoryStub{
		claim: ClaimResult{Disposition: ClaimAcquired, Status: JobRunning, Lease: &lease},
		task: Task{
			JobID: jobID, ConversationID: conversationID, RequestedThroughSeq: 2, AttemptCount: 1,
			CompletedMessages: []conversation.Message{
				{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "目标"},
				{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "结论"},
			},
		},
		completeResult: CompletionResult{Committed: true, ActivationResult: ActivationActivated, ActiveSnapshotID: snapshotID},
	}
	executor := executorFunc(func(_ context.Context, task Task) (ExecutionResult, error) {
		if task.JobID != jobID || task.RequestedThroughSeq != 2 || len(task.CompletedMessages) != 2 {
			t.Fatalf("executor task = %+v", task)
		}
		return ExecutionResult{CurrentSnapshotID: snapshotID, ThroughSeq: 2}, nil
	})
	worker, err := NewWorker(repository, executor, Config{
		WorkerID: "memory-worker-test", LeaseDuration: time.Minute,
		RenewInterval: 10 * time.Second, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewWorker(): %v", err)
	}

	outcome := worker.Process(context.Background(), validIncomingMessage(t, jobID, conversationID, now))
	if outcome.Action != ActionAck || repository.completedLease.FencingToken != 7 ||
		repository.completedResult.CurrentSnapshotID != snapshotID {
		t.Fatalf("outcome/completion = %+v / %+v / %+v", outcome, repository.completedLease, repository.completedResult)
	}
}

func TestWorkerSchedulesApplicationRetryAndAcknowledgesMessage(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	lease := validLease(now)
	lease.AttemptCount = 2
	repository := &workerRepositoryStub{
		claim:    ClaimResult{Disposition: ClaimAcquired, Status: JobRunning, Lease: &lease},
		task:     validTask(lease),
		released: true,
	}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("summary provider is temporarily unavailable")
	}), now)

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.JobID, lease.ConversationID, now))

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if outcome.Action != ActionAck || repository.releaseCalls != 1 || repository.failCalls != 0 {
		t.Fatalf("outcome=%+v release=%d fail=%d", outcome, repository.releaseCalls, repository.failCalls)
	}
	if repository.retryAt.Sub(repository.releasedAt) != time.Minute ||
		repository.failureCode != "memory_compaction_transient_error" {
		t.Fatalf("retryAt=%s releasedAt=%s code=%q", repository.retryAt, repository.releasedAt, repository.failureCode)
	}
}

func TestWorkerFailsJobAfterRetryExhaustion(t *testing.T) {
	now := time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC)
	lease := validLease(now)
	lease.AttemptCount = lease.MaxAttempts
	repository := &workerRepositoryStub{
		claim:  ClaimResult{Disposition: ClaimAcquired, Status: JobRunning, Lease: &lease},
		task:   validTask(lease),
		failed: true,
	}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("summary validation repeatedly failed")
	}), now)

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.JobID, lease.ConversationID, now))

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if outcome.Action != ActionDeadLetter || repository.failCalls != 1 || repository.releaseCalls != 0 ||
		repository.failureCode != "memory_compaction_retry_exhausted" {
		t.Fatalf("outcome=%+v fail=%d release=%d code=%q", outcome, repository.failCalls,
			repository.releaseCalls, repository.failureCode)
	}
}

func TestWorkerStopsExecutionWhenLeaseIsLost(t *testing.T) {
	now := time.Now().UTC()
	lease := validLease(now)
	repository := &workerRepositoryStub{
		claim:      ClaimResult{Disposition: ClaimAcquired, Status: JobRunning, Lease: &lease},
		task:       validTask(lease),
		renewOwned: false,
	}
	worker, err := NewWorker(repository, executorFunc(func(ctx context.Context, _ Task) (ExecutionResult, error) {
		<-ctx.Done()
		return ExecutionResult{}, context.Cause(ctx)
	}), Config{
		WorkerID: "memory-worker-test", LeaseDuration: 50 * time.Millisecond,
		RenewInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker(): %v", err)
	}

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.JobID, lease.ConversationID, now))

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if outcome.Action != ActionRetry || repository.renewCalls < 1 || repository.completeCalls != 0 ||
		repository.releaseCalls != 0 || repository.failCalls != 0 {
		t.Fatalf("outcome=%+v renew=%d complete=%d release=%d fail=%d", outcome,
			repository.renewCalls, repository.completeCalls, repository.releaseCalls, repository.failCalls)
	}
}

func TestWorkerAcknowledgesTerminalAndDelayedDeliveries(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, disposition := range []ClaimDisposition{ClaimTerminal, ClaimDelayed} {
		t.Run(string(disposition), func(t *testing.T) {
			repository := &workerRepositoryStub{claim: ClaimResult{Disposition: disposition}}
			worker := newTestWorker(t, repository, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
				t.Fatal("executor must not run for terminal or delayed delivery")
				return ExecutionResult{}, nil
			}), now)
			outcome := worker.Process(context.Background(), validIncomingMessage(t, uuid.New(), uuid.New(), now))
			if outcome.Action != ActionAck {
				t.Fatalf("outcome=%+v", outcome)
			}
		})
	}
}

func TestWorkerDeadLettersInvalidMessageBeforeClaim(t *testing.T) {
	now := time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC)
	repository := &workerRepositoryStub{}
	worker := newTestWorker(t, repository, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		t.Fatal("executor must not run for an invalid message")
		return ExecutionResult{}, nil
	}), now)
	incoming := validIncomingMessage(t, uuid.New(), uuid.New(), now)
	incoming.MessageID = uuid.NewString()

	outcome := worker.Process(context.Background(), incoming)

	if outcome.Action != ActionDeadLetter || repository.claimCalls != 0 {
		t.Fatalf("outcome=%+v claim=%d", outcome, repository.claimCalls)
	}
}

type executorFunc func(context.Context, Task) (ExecutionResult, error)

func (f executorFunc) Execute(ctx context.Context, task Task) (ExecutionResult, error) {
	return f(ctx, task)
}

type workerRepositoryStub struct {
	mu              sync.Mutex
	claim           ClaimResult
	claimErr        error
	task            Task
	loadErr         error
	renewOwned      bool
	renewErr        error
	completeResult  CompletionResult
	completeErr     error
	released        bool
	releaseErr      error
	failed          bool
	failErr         error
	completedLease  Lease
	completedResult ExecutionResult
	claimCalls      int
	renewCalls      int
	completeCalls   int
	releaseCalls    int
	failCalls       int
	failureCode     string
	failureSummary  string
	releasedAt      time.Time
	retryAt         time.Time
}

func (s *workerRepositoryStub) Claim(context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time) (ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	return s.claim, s.claimErr
}

func (s *workerRepositoryStub) Renew(context.Context, Lease, time.Time, time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCalls++
	return s.renewOwned, s.renewErr
}

func (s *workerRepositoryStub) LoadTask(context.Context, Lease, time.Time) (Task, error) {
	return s.task, s.loadErr
}

func (s *workerRepositoryStub) Complete(_ context.Context, lease Lease, result ExecutionResult, _ time.Time) (CompletionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	s.completedLease = lease
	s.completedResult = result
	return s.completeResult, s.completeErr
}

func (s *workerRepositoryStub) ReleaseForRetry(
	_ context.Context, _ Lease, code, summary string, releasedAt, retryAt time.Time,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls++
	s.failureCode = code
	s.failureSummary = summary
	s.releasedAt = releasedAt
	s.retryAt = retryAt
	return s.released, s.releaseErr
}

func (s *workerRepositoryStub) Fail(_ context.Context, _ Lease, code, summary string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCalls++
	s.failureCode = code
	s.failureSummary = summary
	return s.failed, s.failErr
}

func newTestWorker(t *testing.T, repository Repository, executor Executor, now time.Time) *Worker {
	t.Helper()
	worker, err := NewWorker(repository, executor, Config{
		WorkerID: "memory-worker-test", LeaseDuration: time.Minute,
		RenewInterval: 10 * time.Second, Clock: func() time.Time { return now },
		RetryDelay: func(attempt int) time.Duration {
			return time.Duration(attempt) * 30 * time.Second
		},
	})
	if err != nil {
		t.Fatalf("NewWorker(): %v", err)
	}
	return worker
}

func validLease(now time.Time) Lease {
	return Lease{
		JobID: uuid.New(), ConversationID: uuid.New(), ClaimOwner: "memory-worker-test",
		AttemptCount: 1, MaxAttempts: 3, FencingToken: 1, LeaseUntil: now.Add(time.Minute),
	}
}

func validTask(lease Lease) Task {
	return Task{
		JobID: lease.JobID, ConversationID: lease.ConversationID,
		RequestedThroughSeq: 2, AttemptCount: lease.AttemptCount,
		CompletedMessages: []conversation.Message{
			{ID: uuid.New(), ConversationID: lease.ConversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "目标"},
			{ID: uuid.New(), ConversationID: lease.ConversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "结论"},
		},
	}
}

func validIncomingMessage(t *testing.T, jobID, conversationID uuid.UUID, now time.Time) IncomingMessage {
	t.Helper()
	messageID, correlationID := uuid.New(), uuid.New()
	payload, err := json.Marshal(compactionPayload{JobID: jobID.String(), ConversationID: conversationID.String()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body, err := json.Marshal(messageEnvelope{
		MessageID: messageID.String(), MessageType: MessageType, SchemaVersion: SchemaVersion,
		OccurredAt: now.UTC().Format(time.RFC3339Nano), CorrelationID: correlationID.String(), Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	}
}
