package knowledgeworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

const maxFailureMessage = 1000

var (
	ErrLeaseLost             = errors.New("knowledge ingestion worker lease lost")
	ErrCancellationRequested = errors.New("knowledge ingestion cancellation requested")
	ErrPermanentInput        = errors.New("knowledge ingestion permanent input error")
)

type ClaimDisposition string

const (
	ClaimAcquired     ClaimDisposition = "acquired"
	ClaimLeaseHeld    ClaimDisposition = "lease_held"
	ClaimDelayed      ClaimDisposition = "delayed"
	ClaimCancellation ClaimDisposition = "cancellation_requested"
	ClaimTerminal     ClaimDisposition = "terminal"
)

type Lease struct {
	TaskID            uuid.UUID
	DocumentVersionID uuid.UUID
	ClaimOwner        string
	AttemptCount      int
	MaxAttempts       int
	LeaseUntil        time.Time
}

type ClaimResult struct {
	Disposition ClaimDisposition
	Status      knowledge.IngestionTaskStatus
	Lease       *Lease
	RetryAfter  time.Duration
}

type RenewalResult struct {
	Owned                 bool
	CancellationRequested bool
}

type Task struct {
	ID                uuid.UUID
	DocumentVersionID uuid.UUID
	DocumentID        uuid.UUID
	CreatedBy         uuid.UUID
	Stage             knowledge.IngestionStage
	AttemptCount      int
	MaxAttempts       int
	Checkpoint        json.RawMessage
	ProgressPercent   int
	PipelineVersion   string
	Source            objectstore.ObjectRef
}

type CheckpointUpdate struct {
	Stage           knowledge.IngestionStage
	ProgressPercent int
	Checkpoint      json.RawMessage
}

type ExecutionResult struct {
	Partial          bool
	ParserVersion    string
	ParserMetadata   json.RawMessage
	Checkpoint       json.RawMessage
	Artifact         objectstore.ObjectRef
	Chunks           []knowledge.ChunkDraft
	EmbeddingProfile *knowledge.EmbeddingProfile
	Embeddings       []knowledge.ChunkEmbeddingDraft
	EmbeddingUsage   knowledge.EmbeddingUsage
}

type Repository interface {
	Claim(context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time) (ClaimResult, error)
	Renew(context.Context, Lease, time.Time, time.Time) (RenewalResult, error)
	LoadTask(context.Context, Lease, time.Time) (Task, error)
	SaveCheckpoint(context.Context, Lease, CheckpointUpdate, time.Time) (bool, error)
	SaveParsedResult(context.Context, Lease, ExecutionResult, time.Time) (bool, error)
	Complete(context.Context, Lease, ExecutionResult, time.Time) (bool, error)
	ReleaseForRetry(context.Context, Lease, string, string, time.Time, time.Time) (bool, error)
	Fail(context.Context, Lease, string, string, time.Time) (bool, error)
	FinalizeCancellation(context.Context, Lease, time.Time) (bool, error)
}

type Executor interface {
	Execute(context.Context, Task, func(context.Context, CheckpointUpdate) error) (ExecutionResult, error)
}

type Action string

const (
	ActionAck        Action = "ack"
	ActionRetry      Action = "retry"
	ActionDeadLetter Action = "dead_letter"
	ActionRequeue    Action = "requeue"
)

type Outcome struct {
	Action     Action
	RetryDelay time.Duration
	Reason     string
}

type Config struct {
	WorkerID      string
	LeaseDuration time.Duration
	RenewInterval time.Duration
	Clock         func() time.Time
}

type Worker struct {
	repository    Repository
	executor      Executor
	workerID      string
	leaseDuration time.Duration
	renewInterval time.Duration
	clock         func() time.Time
}

func NewWorker(repository Repository, executor Executor, cfg Config) (*Worker, error) {
	if repository == nil || executor == nil {
		return nil, errors.New("knowledge ingestion worker dependencies are nil")
	}
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	if cfg.WorkerID == "" || len(cfg.WorkerID) > 128 {
		return nil, errors.New("knowledge ingestion worker id is invalid")
	}
	if cfg.LeaseDuration <= 0 || cfg.RenewInterval <= 0 || cfg.RenewInterval >= cfg.LeaseDuration {
		return nil, errors.New("knowledge ingestion worker lease configuration is invalid")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{
		repository: repository, executor: executor, workerID: cfg.WorkerID,
		leaseDuration: cfg.LeaseDuration, renewInterval: cfg.RenewInterval, clock: cfg.Clock,
	}, nil
}

func (w *Worker) Process(ctx context.Context, incoming IncomingMessage) Outcome {
	message, err := ParseMessage(incoming)
	if err != nil {
		return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
	}
	now := w.clock().UTC()
	claim, err := w.repository.Claim(
		ctx, message.TaskID, message.DocumentVersionID, w.workerID, now, now.Add(w.leaseDuration),
	)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Outcome{Action: ActionDeadLetter, Reason: "knowledge ingestion task does not exist"}
		}
		return retryOutcome(30*time.Second, err)
	}
	switch claim.Disposition {
	case ClaimTerminal:
		if claim.Status == knowledge.IngestionFailed {
			return Outcome{Action: ActionDeadLetter, Reason: "knowledge ingestion task is already failed"}
		}
		return Outcome{Action: ActionAck, Reason: "knowledge ingestion task is already terminal"}
	case ClaimLeaseHeld:
		return retryOutcome(30*time.Second, errors.New("knowledge ingestion task lease is held"))
	case ClaimDelayed:
		delay := claim.RetryAfter
		if delay <= 0 {
			delay = 30 * time.Second
		}
		return retryOutcome(delay, errors.New("knowledge ingestion task is waiting for retry"))
	case ClaimCancellation:
		if claim.Lease == nil {
			return retryOutcome(30*time.Second, errors.New("cancellation claim returned no lease"))
		}
		return w.finalizeCancellation(ctx, *claim.Lease)
	case ClaimAcquired:
		if claim.Lease == nil {
			return retryOutcome(30*time.Second, errors.New("claim returned no lease"))
		}
		return w.execute(ctx, *claim.Lease)
	default:
		return Outcome{Action: ActionDeadLetter, Reason: "unsupported knowledge ingestion claim disposition"}
	}
}

func (w *Worker) execute(ctx context.Context, lease Lease) Outcome {
	task, err := w.repository.LoadTask(ctx, lease, w.clock().UTC())
	if err != nil {
		if errors.Is(err, ErrPermanentInput) {
			return w.failPermanent(ctx, lease, "invalid_ingestion_input", err)
		}
		return w.releaseAndRetry(ctx, lease, err)
	}
	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	heartbeatDone := make(chan error, 1)
	go w.renewLease(executionCtx, cancelExecution, lease, heartbeatDone)

	var checkpointMu sync.Mutex
	reportCheckpoint := func(checkpointCtx context.Context, update CheckpointUpdate) error {
		checkpointMu.Lock()
		defer checkpointMu.Unlock()
		if err := validateCheckpointUpdate(update); err != nil {
			return fmt.Errorf("%w: %v", ErrPermanentInput, err)
		}
		saved, saveErr := w.repository.SaveCheckpoint(checkpointCtx, lease, update, w.clock().UTC())
		if saveErr != nil {
			return saveErr
		}
		if !saved {
			return ErrLeaseLost
		}
		return nil
	}
	result, executionErr := w.executor.Execute(executionCtx, task, reportCheckpoint)
	if executionErr == nil {
		if err := validateExecutionResult(result); err != nil {
			executionErr = fmt.Errorf("%w: %v", ErrPermanentInput, err)
		}
	}
	if executionErr == nil {
		saved, err := w.repository.SaveParsedResult(executionCtx, lease, result, w.clock().UTC())
		if err != nil {
			executionErr = err
		} else if !saved {
			executionErr = ErrLeaseLost
		}
	}
	if executionErr == nil {
		publishingCheckpoint, err := json.Marshal(map[string]any{
			"artifactSha256": result.Artifact.SHA256, "chunkCount": len(result.Chunks),
		})
		if err != nil {
			executionErr = err
		} else {
			saved, saveErr := w.repository.SaveCheckpoint(executionCtx, lease, CheckpointUpdate{
				Stage: knowledge.IngestionStagePublishing, ProgressPercent: 95,
				Checkpoint: publishingCheckpoint,
			}, w.clock().UTC())
			if saveErr != nil {
				executionErr = saveErr
			} else if !saved {
				executionErr = ErrLeaseLost
			}
		}
	}
	cancelExecution(nil)
	heartbeatErr := <-heartbeatDone

	if ctx.Err() != nil {
		return Outcome{Action: ActionRequeue, Reason: stableReason(ctx.Err())}
	}
	if errors.Is(heartbeatErr, ErrCancellationRequested) || errors.Is(context.Cause(executionCtx), ErrCancellationRequested) {
		return w.finalizeCancellation(ctx, lease)
	}
	if heartbeatErr != nil {
		return retryOutcome(30*time.Second, heartbeatErr)
	}
	if executionErr != nil {
		if errors.Is(executionErr, ErrPermanentInput) {
			return w.failPermanent(ctx, lease, "invalid_ingestion_input", executionErr)
		}
		return w.releaseAndRetry(ctx, lease, executionErr)
	}
	now := w.clock().UTC()
	renewed, err := w.repository.Renew(ctx, lease, now, now.Add(w.leaseDuration))
	if err != nil || !renewed.Owned {
		if err == nil {
			err = ErrLeaseLost
		}
		return retryOutcome(30*time.Second, err)
	}
	if renewed.CancellationRequested {
		return w.finalizeCancellation(ctx, lease)
	}
	completed, err := w.repository.Complete(ctx, lease, result, w.clock().UTC())
	if err != nil {
		return w.releaseAndRetry(ctx, lease, err)
	}
	if !completed {
		return retryOutcome(30*time.Second, ErrLeaseLost)
	}
	return Outcome{Action: ActionAck, Reason: "knowledge ingestion result committed"}
}

func validateExecutionResult(result ExecutionResult) error {
	if strings.TrimSpace(result.ParserVersion) == "" || len(result.ParserVersion) > 128 ||
		!validJSONObject(result.ParserMetadata) || !validJSONObject(result.Checkpoint) {
		return errors.New("parser result metadata is invalid")
	}
	if result.Artifact.Bucket != objectstore.BucketKnowledgeArtifacts {
		return errors.New("parser result artifact uses an invalid bucket")
	}
	if err := result.Artifact.Validate(); err != nil {
		return err
	}
	if len(result.Chunks) == 0 || len(result.Chunks) > 10000 {
		return errors.New("parser result chunks are required and bounded")
	}
	for _, chunk := range result.Chunks {
		if err := chunk.Validate(); err != nil {
			return err
		}
	}
	if result.EmbeddingProfile == nil {
		if len(result.Embeddings) != 0 || result.EmbeddingUsage.TotalTokens != 0 {
			return errors.New("parser result has embeddings without a profile")
		}
		return nil
	}
	if err := result.EmbeddingProfile.Validate(); err != nil {
		return err
	}
	if len(result.Embeddings) != len(result.Chunks) || result.EmbeddingUsage.TotalTokens < 0 {
		return errors.New("parser result embedding count is invalid")
	}
	for ordinal, embedding := range result.Embeddings {
		if embedding.ChunkOrdinal != ordinal || embedding.ContentSHA256 != result.Chunks[ordinal].ContentSHA256 {
			return errors.New("parser result embedding does not match its chunk")
		}
		if err := embedding.Validate(*result.EmbeddingProfile); err != nil {
			return err
		}
	}
	return nil
}

func validJSONObject(raw json.RawMessage) bool {
	var object map[string]any
	return len(raw) > 0 && json.Unmarshal(raw, &object) == nil && object != nil
}

func (w *Worker) renewLease(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	lease Lease,
	done chan<- error,
) {
	ticker := time.NewTicker(w.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			now := w.clock().UTC()
			result, err := w.repository.Renew(ctx, lease, now, now.Add(w.leaseDuration))
			if err == nil && result.Owned && !result.CancellationRequested {
				continue
			}
			if err == nil {
				if result.CancellationRequested {
					err = ErrCancellationRequested
				} else {
					err = ErrLeaseLost
				}
			}
			cancel(err)
			done <- err
			return
		}
	}
}

func (w *Worker) releaseAndRetry(ctx context.Context, lease Lease, cause error) Outcome {
	if lease.AttemptCount >= lease.MaxAttempts {
		return w.failPermanent(ctx, lease, "ingestion_retry_exhausted", cause)
	}
	delay := retryDelay(lease.AttemptCount)
	now := w.clock().UTC()
	released, err := w.repository.ReleaseForRetry(
		ctx, lease, "ingestion_transient_error", stableReason(cause), now, now.Add(delay),
	)
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if !released {
		return retryOutcome(30*time.Second, ErrLeaseLost)
	}
	return retryOutcome(delay, cause)
}

func (w *Worker) failPermanent(ctx context.Context, lease Lease, code string, cause error) Outcome {
	failed, err := w.repository.Fail(ctx, lease, code, stableReason(cause), w.clock().UTC())
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if !failed {
		return retryOutcome(30*time.Second, ErrLeaseLost)
	}
	return Outcome{Action: ActionDeadLetter, Reason: stableReason(cause)}
}

func (w *Worker) finalizeCancellation(ctx context.Context, lease Lease) Outcome {
	completed, err := w.repository.FinalizeCancellation(ctx, lease, w.clock().UTC())
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if !completed {
		return retryOutcome(30*time.Second, ErrLeaseLost)
	}
	return Outcome{Action: ActionAck, Reason: "knowledge ingestion cancellation committed"}
}

func validateCheckpointUpdate(update CheckpointUpdate) error {
	switch update.Stage {
	case knowledge.IngestionStageScanning, knowledge.IngestionStageParsing,
		knowledge.IngestionStageChunking, knowledge.IngestionStageIndexing,
		knowledge.IngestionStagePublishing:
	default:
		return errors.New("checkpoint stage is invalid")
	}
	if update.ProgressPercent < 0 || update.ProgressPercent > 99 {
		return errors.New("checkpoint progress must be between 0 and 99")
	}
	var object map[string]any
	if len(update.Checkpoint) == 0 || json.Unmarshal(update.Checkpoint, &object) != nil || object == nil {
		return errors.New("checkpoint must be a JSON object")
	}
	return nil
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 30 * time.Second
	case 2:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func retryOutcome(delay time.Duration, err error) Outcome {
	return Outcome{Action: ActionRetry, RetryDelay: delay, Reason: stableReason(err)}
}

func stableReason(err error) string {
	if err == nil {
		return "unspecified worker error"
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		value = fmt.Sprintf("%T", err)
	}
	if len(value) > maxFailureMessage {
		value = strings.ToValidUTF8(value[:maxFailureMessage], "?")
	}
	return value
}
