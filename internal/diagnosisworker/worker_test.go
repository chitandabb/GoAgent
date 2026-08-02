package diagnosisworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

func TestWorkerProcessCommitsBeforeAck(t *testing.T) {
	lease := diagnosis.TaskLease{TaskID: uuid.New(), ClaimOwner: "worker-test", AttemptCount: 1, LeaseUntil: time.Now().Add(time.Minute)}
	leaser := &fakeLeaseService{claim: diagnosis.TaskClaimResult{
		Disposition: diagnosis.TaskClaimAcquired, Status: diagnosis.TaskRunning, Lease: &lease,
	}, renewOwned: true}
	repo := &fakeWorkerRepository{task: validWorkerTask(lease.TaskID), completeResult: true}
	executed := false
	worker := newTestWorker(t, leaser, repo, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		executed = true
		return ExecutionResult{}, nil
	}))

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.TaskID))
	if outcome.Action != ActionAck || !executed || repo.completeCalls != 1 || leaser.renewCalls != 1 {
		t.Fatalf("outcome=%+v executed=%v completeCalls=%d renewCalls=%d", outcome, executed, repo.completeCalls, leaser.renewCalls)
	}
}

func TestWorkerProcessFinalizesCancellation(t *testing.T) {
	taskID := uuid.New()
	leaser := &fakeLeaseService{claim: diagnosis.TaskClaimResult{
		Disposition: diagnosis.TaskClaimCancellationRequested, Status: diagnosis.TaskCancelRequested,
	}}
	repo := &fakeWorkerRepository{cancellationStatus: diagnosis.TaskCancelled}
	worker := newTestWorker(t, leaser, repo, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		t.Fatal("executor must not run for cancellation")
		return ExecutionResult{}, nil
	}))

	outcome := worker.Process(context.Background(), validIncomingMessage(t, taskID))
	if outcome.Action != ActionAck || repo.cancelCalls != 1 {
		t.Fatalf("outcome=%+v cancelCalls=%d", outcome, repo.cancelCalls)
	}
}

func TestWorkerProcessUsesBoundedRetryForHeldLease(t *testing.T) {
	leaser := &fakeLeaseService{claim: diagnosis.TaskClaimResult{
		Disposition: diagnosis.TaskClaimLeaseHeld, Status: diagnosis.TaskRunning,
	}}
	worker := newTestWorker(t, leaser, &fakeWorkerRepository{}, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		return ExecutionResult{}, nil
	}))
	outcome := worker.Process(context.Background(), validIncomingMessage(t, uuid.New()))
	if outcome.Action != ActionRetry || outcome.RetryDelay != 30*time.Second {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestWorkerProcessReleasesTransientFailureForRetry(t *testing.T) {
	lease := diagnosis.TaskLease{TaskID: uuid.New(), ClaimOwner: "worker-test", AttemptCount: 2, LeaseUntil: time.Now().Add(time.Minute)}
	leaser := &fakeLeaseService{claim: diagnosis.TaskClaimResult{
		Disposition: diagnosis.TaskClaimAcquired, Status: diagnosis.TaskRunning, Lease: &lease,
	}, renewOwned: true}
	repo := &fakeWorkerRepository{task: validWorkerTask(lease.TaskID), releaseResult: true}
	worker := newTestWorker(t, leaser, repo, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("model temporarily unavailable")
	}))

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.TaskID))
	if outcome.Action != ActionRetry || outcome.RetryDelay != 2*time.Minute || repo.releaseCalls != 1 || repo.failCalls != 0 {
		t.Fatalf("outcome=%+v releaseCalls=%d failCalls=%d", outcome, repo.releaseCalls, repo.failCalls)
	}
}

func TestWorkerProcessFailsAfterRetryBudget(t *testing.T) {
	lease := diagnosis.TaskLease{TaskID: uuid.New(), ClaimOwner: "worker-test", AttemptCount: 4, LeaseUntil: time.Now().Add(time.Minute)}
	leaser := &fakeLeaseService{claim: diagnosis.TaskClaimResult{
		Disposition: diagnosis.TaskClaimAcquired, Status: diagnosis.TaskRunning, Lease: &lease,
	}, renewOwned: true}
	repo := &fakeWorkerRepository{task: validWorkerTask(lease.TaskID), failResult: true}
	worker := newTestWorker(t, leaser, repo, executorFunc(func(context.Context, Task) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("model remains unavailable")
	}))

	outcome := worker.Process(context.Background(), validIncomingMessage(t, lease.TaskID))
	if outcome.Action != ActionDeadLetter || repo.failCalls != 1 || repo.releaseCalls != 0 {
		t.Fatalf("outcome=%+v failCalls=%d releaseCalls=%d", outcome, repo.failCalls, repo.releaseCalls)
	}
}

func newTestWorker(t *testing.T, leases TaskLeaseService, repo Repository, executor AgentExecutor) *Worker {
	t.Helper()
	worker, err := New(leases, repo, executor, Config{
		WorkerID: "worker-test", RenewInterval: time.Hour, MaxAttempts: 4,
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return worker
}

func validWorkerTask(taskID uuid.UUID) Task {
	return Task{ID: taskID, CreatedBy: uuid.New(), Role: auth.RoleAnalyst, RequestText: "diagnose"}
}

func validIncomingMessage(t *testing.T, taskID uuid.UUID) IncomingMessage {
	t.Helper()
	messageID := uuid.New()
	correlationID := uuid.New()
	return IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: DiagnosisMessageType,
		Body: mustEnvelope(t, messageID, correlationID, taskID, nil),
	}
}

type executorFunc func(context.Context, Task) (ExecutionResult, error)

func (f executorFunc) Execute(ctx context.Context, task Task) (ExecutionResult, error) {
	return f(ctx, task)
}

type fakeLeaseService struct {
	mu         sync.Mutex
	claim      diagnosis.TaskClaimResult
	claimErr   error
	renewOwned bool
	renewErr   error
	renewCalls int
}

func (f *fakeLeaseService) Claim(context.Context, uuid.UUID, string) (diagnosis.TaskClaimResult, error) {
	return f.claim, f.claimErr
}

func (f *fakeLeaseService) Renew(context.Context, diagnosis.TaskLease) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	return f.renewOwned, f.renewErr
}

type fakeWorkerRepository struct {
	task               Task
	loadErr            error
	completeResult     bool
	completeErr        error
	releaseResult      bool
	releaseErr         error
	failResult         bool
	failErr            error
	cancellationStatus diagnosis.TaskStatus
	cancellationErr    error
	completeCalls      int
	releaseCalls       int
	failCalls          int
	cancelCalls        int
}

func (f *fakeWorkerRepository) LoadTask(context.Context, diagnosis.TaskLease, time.Time) (Task, error) {
	return f.task, f.loadErr
}

func (f *fakeWorkerRepository) Complete(context.Context, diagnosis.TaskLease, ExecutionResult, time.Time) (bool, error) {
	f.completeCalls++
	return f.completeResult, f.completeErr
}

func (f *fakeWorkerRepository) ReleaseForRetry(context.Context, diagnosis.TaskLease, string, string, time.Time) (bool, error) {
	f.releaseCalls++
	return f.releaseResult, f.releaseErr
}

func (f *fakeWorkerRepository) Fail(context.Context, diagnosis.TaskLease, string, string, time.Time) (bool, error) {
	f.failCalls++
	return f.failResult, f.failErr
}

func (f *fakeWorkerRepository) FinalizeCancellation(context.Context, uuid.UUID, time.Time) (diagnosis.TaskStatus, error) {
	f.cancelCalls++
	return f.cancellationStatus, f.cancellationErr
}
