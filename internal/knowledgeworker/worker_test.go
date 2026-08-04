package knowledgeworker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

func TestKnowledgeWorkerCheckpointsAndCompletesBeforeAck(t *testing.T) {
	lease := validKnowledgeLease()
	repo := &knowledgeWorkerRepositoryStub{
		claim:       ClaimResult{Disposition: ClaimAcquired, Status: knowledge.IngestionRunning, Lease: &lease},
		renewResult: RenewalResult{Owned: true}, task: validKnowledgeWorkerTask(lease),
		checkpointSaved: true, completed: true,
	}
	executor := knowledgeExecutorFunc(func(
		ctx context.Context, _ Task, checkpoint func(context.Context, CheckpointUpdate) error,
	) (ExecutionResult, error) {
		if err := checkpoint(ctx, CheckpointUpdate{
			Stage: knowledge.IngestionStageParsing, ProgressPercent: 40,
			Checkpoint: json.RawMessage(`{"parsedPages":4}`),
		}); err != nil {
			return ExecutionResult{}, err
		}
		return ExecutionResult{
			ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"pages":10}`),
			Checkpoint: json.RawMessage(`{"indexed":true}`),
		}, nil
	})
	worker := newKnowledgeTestWorker(t, repo, executor)
	outcome := worker.Process(context.Background(), validKnowledgeIncomingMessage(t, lease.TaskID, lease.DocumentVersionID))
	if outcome.Action != ActionAck || repo.checkpointCalls != 1 || repo.completeCalls != 1 || repo.renewCalls != 1 {
		t.Fatalf("outcome=%+v checkpoint=%d complete=%d renew=%d", outcome,
			repo.checkpointCalls, repo.completeCalls, repo.renewCalls)
	}
}

func TestKnowledgeWorkerSchedulesTransientRetry(t *testing.T) {
	lease := validKnowledgeLease()
	lease.AttemptCount = 2
	repo := &knowledgeWorkerRepositoryStub{
		claim: ClaimResult{Disposition: ClaimAcquired, Status: knowledge.IngestionRunning, Lease: &lease},
		task:  validKnowledgeWorkerTask(lease), released: true,
	}
	worker := newKnowledgeTestWorker(t, repo, knowledgeExecutorFunc(func(
		context.Context, Task, func(context.Context, CheckpointUpdate) error,
	) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("OCR temporarily unavailable")
	}))
	outcome := worker.Process(context.Background(), validKnowledgeIncomingMessage(t, lease.TaskID, lease.DocumentVersionID))
	if outcome.Action != ActionRetry || outcome.RetryDelay != 2*time.Minute || repo.releaseCalls != 1 || repo.failCalls != 0 {
		t.Fatalf("outcome=%+v release=%d fail=%d", outcome, repo.releaseCalls, repo.failCalls)
	}
}

func TestKnowledgeWorkerFailsPermanentInputWithoutRetry(t *testing.T) {
	lease := validKnowledgeLease()
	repo := &knowledgeWorkerRepositoryStub{
		claim: ClaimResult{Disposition: ClaimAcquired, Status: knowledge.IngestionRunning, Lease: &lease},
		task:  validKnowledgeWorkerTask(lease), failed: true,
	}
	worker := newKnowledgeTestWorker(t, repo, knowledgeExecutorFunc(func(
		context.Context, Task, func(context.Context, CheckpointUpdate) error,
	) (ExecutionResult, error) {
		return ExecutionResult{}, fmtPermanentInput("encrypted PDF")
	}))
	outcome := worker.Process(context.Background(), validKnowledgeIncomingMessage(t, lease.TaskID, lease.DocumentVersionID))
	if outcome.Action != ActionDeadLetter || repo.failCalls != 1 || repo.releaseCalls != 0 {
		t.Fatalf("outcome=%+v fail=%d release=%d", outcome, repo.failCalls, repo.releaseCalls)
	}
}

func TestKnowledgeWorkerFinalizesCancellationClaim(t *testing.T) {
	lease := validKnowledgeLease()
	repo := &knowledgeWorkerRepositoryStub{
		claim:     ClaimResult{Disposition: ClaimCancellation, Status: knowledge.IngestionCancelRequested, Lease: &lease},
		cancelled: true,
	}
	worker := newKnowledgeTestWorker(t, repo, knowledgeExecutorFunc(func(
		context.Context, Task, func(context.Context, CheckpointUpdate) error,
	) (ExecutionResult, error) {
		t.Fatal("executor must not run for cancellation")
		return ExecutionResult{}, nil
	}))
	outcome := worker.Process(context.Background(), validKnowledgeIncomingMessage(t, lease.TaskID, lease.DocumentVersionID))
	if outcome.Action != ActionAck || repo.cancelCalls != 1 {
		t.Fatalf("outcome=%+v cancel=%d", outcome, repo.cancelCalls)
	}
}

func TestKnowledgeWorkerRejectsMismatchedMessageBeforeClaim(t *testing.T) {
	repo := &knowledgeWorkerRepositoryStub{}
	worker := newKnowledgeTestWorker(t, repo, knowledgeExecutorFunc(func(
		context.Context, Task, func(context.Context, CheckpointUpdate) error,
	) (ExecutionResult, error) {
		return ExecutionResult{}, nil
	}))
	incoming := validKnowledgeIncomingMessage(t, uuid.New(), uuid.New())
	incoming.MessageID = uuid.NewString()
	outcome := worker.Process(context.Background(), incoming)
	if outcome.Action != ActionDeadLetter || repo.claimCalls != 0 {
		t.Fatalf("outcome=%+v claim=%d", outcome, repo.claimCalls)
	}
}

func newKnowledgeTestWorker(t *testing.T, repo Repository, executor Executor) *Worker {
	t.Helper()
	worker, err := NewWorker(repo, executor, Config{
		WorkerID: "knowledge-worker-test", LeaseDuration: 2 * time.Hour,
		RenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func validKnowledgeLease() Lease {
	return Lease{
		TaskID: uuid.New(), DocumentVersionID: uuid.New(), ClaimOwner: "knowledge-worker-test",
		AttemptCount: 1, MaxAttempts: 3, LeaseUntil: time.Now().Add(2 * time.Hour),
	}
}

func validKnowledgeWorkerTask(lease Lease) Task {
	return Task{
		ID: lease.TaskID, DocumentVersionID: lease.DocumentVersionID, DocumentID: uuid.New(),
		CreatedBy: uuid.New(), Stage: knowledge.IngestionStageUploaded,
		AttemptCount: lease.AttemptCount, MaxAttempts: lease.MaxAttempts,
		Checkpoint: json.RawMessage(`{}`), PipelineVersion: "ingestion-v1",
		Source: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object",
			ETag: "etag", SizeBytes: 7, SHA256: knowledge.SHA256Hex("content"),
			MediaType: "text/plain", OriginalName: "manual.txt",
		},
	}
}

func validKnowledgeIncomingMessage(t *testing.T, taskID, versionID uuid.UUID) IncomingMessage {
	t.Helper()
	messageID, correlationID := uuid.New(), uuid.New()
	envelope := map[string]any{
		"messageId": messageID.String(), "messageType": MessageType,
		"schemaVersion": SchemaVersion, "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"correlationId": correlationID.String(), "causationId": nil,
		"payload": map[string]any{"taskId": taskID.String(), "documentVersionId": versionID.String()},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(),
		CorrelationID: correlationID.String(), Type: MessageType, Body: body,
	}
}

type knowledgeExecutorFunc func(context.Context, Task, func(context.Context, CheckpointUpdate) error) (ExecutionResult, error)

func (f knowledgeExecutorFunc) Execute(
	ctx context.Context, task Task, checkpoint func(context.Context, CheckpointUpdate) error,
) (ExecutionResult, error) {
	return f(ctx, task, checkpoint)
}

func fmtPermanentInput(message string) error {
	return errors.Join(ErrPermanentInput, errors.New(message))
}

type knowledgeWorkerRepositoryStub struct {
	mu              sync.Mutex
	claim           ClaimResult
	claimErr        error
	renewResult     RenewalResult
	renewErr        error
	task            Task
	loadErr         error
	checkpointSaved bool
	checkpointErr   error
	completed       bool
	completeErr     error
	released        bool
	releaseErr      error
	failed          bool
	failErr         error
	cancelled       bool
	cancelErr       error
	claimCalls      int
	renewCalls      int
	checkpointCalls int
	completeCalls   int
	releaseCalls    int
	failCalls       int
	cancelCalls     int
}

func (r *knowledgeWorkerRepositoryStub) Claim(
	context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time,
) (ClaimResult, error) {
	r.claimCalls++
	return r.claim, r.claimErr
}

func (r *knowledgeWorkerRepositoryStub) Renew(
	context.Context, Lease, time.Time, time.Time,
) (RenewalResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renewCalls++
	return r.renewResult, r.renewErr
}

func (r *knowledgeWorkerRepositoryStub) LoadTask(context.Context, Lease, time.Time) (Task, error) {
	return r.task, r.loadErr
}

func (r *knowledgeWorkerRepositoryStub) SaveCheckpoint(
	context.Context, Lease, CheckpointUpdate, time.Time,
) (bool, error) {
	r.checkpointCalls++
	return r.checkpointSaved, r.checkpointErr
}

func (r *knowledgeWorkerRepositoryStub) Complete(
	context.Context, Lease, ExecutionResult, time.Time,
) (bool, error) {
	r.completeCalls++
	return r.completed, r.completeErr
}

func (r *knowledgeWorkerRepositoryStub) ReleaseForRetry(
	context.Context, Lease, string, string, time.Time, time.Time,
) (bool, error) {
	r.releaseCalls++
	return r.released, r.releaseErr
}

func (r *knowledgeWorkerRepositoryStub) Fail(
	context.Context, Lease, string, string, time.Time,
) (bool, error) {
	r.failCalls++
	return r.failed, r.failErr
}

func (r *knowledgeWorkerRepositoryStub) FinalizeCancellation(
	context.Context, Lease, time.Time,
) (bool, error) {
	r.cancelCalls++
	return r.cancelled, r.cancelErr
}
