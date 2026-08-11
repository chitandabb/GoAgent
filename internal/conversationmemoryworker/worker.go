package conversationmemoryworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

const maxFailureMessage = 1000

var ErrLeaseLost = errors.New("conversation memory worker lease lost")

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobRetryWait JobStatus = "retry_wait"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

type ClaimDisposition string

const (
	ClaimAcquired  ClaimDisposition = "acquired"
	ClaimLeaseHeld ClaimDisposition = "lease_held"
	ClaimDelayed   ClaimDisposition = "delayed"
	ClaimTerminal  ClaimDisposition = "terminal"
)

type Lease struct {
	JobID          uuid.UUID
	ConversationID uuid.UUID
	ClaimOwner     string
	AttemptCount   int
	MaxAttempts    int
	FencingToken   int64
	LeaseUntil     time.Time
}

func (l Lease) Validate() error {
	if l.JobID == uuid.Nil || l.ConversationID == uuid.Nil || strings.TrimSpace(l.ClaimOwner) == "" ||
		l.AttemptCount < 1 || l.MaxAttempts < 1 || l.AttemptCount > l.MaxAttempts ||
		l.FencingToken < 1 || l.LeaseUntil.IsZero() {
		return errors.New("conversation memory lease is invalid")
	}
	return nil
}

type ClaimResult struct {
	Disposition ClaimDisposition
	Status      JobStatus
	Lease       *Lease
	RetryAfter  time.Duration
}

type Task struct {
	JobID               uuid.UUID
	ConversationID      uuid.UUID
	RequestedThroughSeq int64
	AttemptCount        int
	CompletedMessages   []conversation.Message
}

func (t Task) Validate() error {
	if t.JobID == uuid.Nil || t.ConversationID == uuid.Nil || t.RequestedThroughSeq < 1 || t.AttemptCount < 1 ||
		len(t.CompletedMessages) == 0 {
		return errors.New("conversation memory task is invalid")
	}
	for index, message := range t.CompletedMessages {
		if message.ID == uuid.Nil || message.ConversationID != t.ConversationID || message.Seq != int64(index+1) ||
			!message.Role.Valid() || strings.TrimSpace(message.Content) == "" {
			return errors.New("conversation memory task messages are invalid")
		}
	}
	if t.CompletedMessages[len(t.CompletedMessages)-1].Seq != t.RequestedThroughSeq {
		return errors.New("conversation memory task coverage is invalid")
	}
	return nil
}

type ExecutionResult struct {
	CandidateSnapshotID      *uuid.UUID
	CurrentSnapshotID        *uuid.UUID
	ExpectedActiveSnapshotID *uuid.UUID
	ThroughSeq               int64
}

func (r ExecutionResult) Validate() error {
	hasCandidate := r.CandidateSnapshotID != nil && *r.CandidateSnapshotID != uuid.Nil
	hasCurrent := r.CurrentSnapshotID != nil && *r.CurrentSnapshotID != uuid.Nil
	if hasCandidate == hasCurrent || r.ThroughSeq < 1 {
		return errors.New("conversation memory execution result is invalid")
	}
	if hasCurrent && r.ExpectedActiveSnapshotID != nil {
		return errors.New("current conversation memory result cannot carry an expected Active")
	}
	if r.ExpectedActiveSnapshotID != nil && *r.ExpectedActiveSnapshotID == uuid.Nil {
		return errors.New("expected Active snapshot is invalid")
	}
	return nil
}

type ActivationResult string

const (
	ActivationActivated      ActivationResult = "activated"
	ActivationAlreadyCurrent ActivationResult = "already_current"
	ActivationCASWinner      ActivationResult = "cas_winner"
)

type CompletionResult struct {
	Committed        bool
	ActivationResult ActivationResult
	ActiveSnapshotID uuid.UUID
}

type Repository interface {
	Claim(context.Context, uuid.UUID, uuid.UUID, string, time.Time, time.Time) (ClaimResult, error)
	Renew(context.Context, Lease, time.Time, time.Time) (bool, error)
	LoadTask(context.Context, Lease, time.Time) (Task, error)
	Complete(context.Context, Lease, ExecutionResult, time.Time) (CompletionResult, error)
	ReleaseForRetry(context.Context, Lease, string, string, time.Time, time.Time) (bool, error)
	Fail(context.Context, Lease, string, string, time.Time) (bool, error)
}

type Executor interface {
	Execute(context.Context, Task) (ExecutionResult, error)
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
	RetryDelay    func(int) time.Duration
}

type Worker struct {
	repository    Repository
	executor      Executor
	workerID      string
	leaseDuration time.Duration
	renewInterval time.Duration
	clock         func() time.Time
	retryDelay    func(int) time.Duration
}

func NewWorker(repository Repository, executor Executor, config Config) (*Worker, error) {
	if repository == nil || executor == nil {
		return nil, errors.New("conversation memory worker dependencies are nil")
	}
	config.WorkerID = strings.TrimSpace(config.WorkerID)
	if config.WorkerID == "" || len(config.WorkerID) > 128 || config.LeaseDuration <= 0 ||
		config.RenewInterval <= 0 || config.RenewInterval*2 >= config.LeaseDuration {
		return nil, errors.New("conversation memory worker configuration is invalid")
	}
	if config.Clock == nil {
		config.Clock = func() time.Time { return time.Now().UTC() }
	}
	if config.RetryDelay == nil {
		config.RetryDelay = defaultRetryDelay
	}
	return &Worker{
		repository: repository, executor: executor, workerID: config.WorkerID,
		leaseDuration: config.LeaseDuration, renewInterval: config.RenewInterval,
		clock: config.Clock, retryDelay: config.RetryDelay,
	}, nil
}

func (w *Worker) Process(ctx context.Context, incoming IncomingMessage) Outcome {
	message, err := ParseMessage(incoming)
	if err != nil {
		return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
	}
	now := w.clock().UTC()
	claim, err := w.repository.Claim(
		ctx, message.JobID, message.ConversationID, w.workerID, now, now.Add(w.leaseDuration),
	)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Outcome{Action: ActionDeadLetter, Reason: "conversation memory job does not exist"}
		}
		return retryOutcome(30*time.Second, err)
	}
	switch claim.Disposition {
	case ClaimTerminal:
		return Outcome{Action: ActionAck, Reason: "conversation memory job is already terminal"}
	case ClaimLeaseHeld:
		return retryOutcome(30*time.Second, errors.New("conversation memory job lease is held"))
	case ClaimDelayed:
		return Outcome{Action: ActionAck, Reason: "conversation memory retry is already scheduled"}
	case ClaimAcquired:
		if claim.Lease == nil {
			return retryOutcome(30*time.Second, errors.New("conversation memory claim returned no lease"))
		}
		return w.execute(ctx, *claim.Lease)
	default:
		return Outcome{Action: ActionDeadLetter, Reason: "conversation memory claim disposition is invalid"}
	}
}

func (w *Worker) execute(ctx context.Context, lease Lease) Outcome {
	if err := lease.Validate(); err != nil {
		return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
	}
	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	heartbeatDone := make(chan error, 1)
	go w.renewLease(executionCtx, cancelExecution, lease, heartbeatDone)
	task, loadErr := w.repository.LoadTask(executionCtx, lease, w.clock().UTC())
	var result ExecutionResult
	executionErr := loadErr
	if executionErr == nil {
		executionErr = task.Validate()
	}
	if executionErr == nil {
		result, executionErr = w.executor.Execute(executionCtx, task)
		if executionErr == nil {
			executionErr = result.Validate()
		}
	}
	if executionErr == nil {
		completed, completeErr := w.repository.Complete(executionCtx, lease, result, w.clock().UTC())
		if completeErr != nil {
			executionErr = completeErr
		} else if !completed.Committed {
			executionErr = ErrLeaseLost
		}
	}
	cancelExecution(nil)
	heartbeatErr := <-heartbeatDone
	if executionErr == nil {
		return Outcome{Action: ActionAck, Reason: "conversation memory snapshot publication committed"}
	}
	if ctx.Err() != nil {
		return Outcome{Action: ActionRequeue, Reason: stableReason(ctx.Err())}
	}
	if heartbeatErr != nil || errors.Is(executionErr, ErrLeaseLost) {
		if heartbeatErr != nil {
			executionErr = errors.Join(executionErr, heartbeatErr)
		}
		return retryOutcome(30*time.Second, executionErr)
	}
	if lease.AttemptCount >= lease.MaxAttempts {
		return w.failPermanently(ctx, lease, executionErr)
	}
	delay := w.retryDelay(lease.AttemptCount)
	if delay <= 0 || delay > 24*time.Hour {
		delay = defaultRetryDelay(lease.AttemptCount)
	}
	scheduledAt := w.clock().UTC()
	released, err := w.repository.ReleaseForRetry(
		ctx, lease, "memory_compaction_transient_error", stableReason(executionErr),
		scheduledAt, scheduledAt.Add(delay),
	)
	if err != nil || !released {
		if err == nil {
			err = ErrLeaseLost
		}
		return retryOutcome(30*time.Second, errors.Join(executionErr, err))
	}
	return Outcome{Action: ActionAck, Reason: "conversation memory retry scheduled"}
}

func (w *Worker) renewLease(ctx context.Context, cancel context.CancelCauseFunc, lease Lease, done chan<- error) {
	ticker := time.NewTicker(w.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			now := w.clock().UTC()
			owned, err := w.repository.Renew(ctx, lease, now, now.Add(w.leaseDuration))
			if err == nil && owned {
				continue
			}
			if err == nil {
				err = ErrLeaseLost
			}
			cancel(err)
			done <- err
			return
		}
	}
}

func (w *Worker) failPermanently(ctx context.Context, lease Lease, cause error) Outcome {
	failed, err := w.repository.Fail(
		ctx, lease, "memory_compaction_retry_exhausted", stableReason(cause), w.clock().UTC(),
	)
	if err != nil || !failed {
		if err == nil {
			err = ErrLeaseLost
		}
		return retryOutcome(30*time.Second, errors.Join(cause, err))
	}
	return Outcome{Action: ActionDeadLetter, Reason: stableReason(cause)}
}

func defaultRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return 30 * time.Second * time.Duration(1<<(attempt-1))
}

func retryOutcome(delay time.Duration, err error) Outcome {
	return Outcome{Action: ActionRetry, RetryDelay: delay, Reason: stableReason(err)}
}

func stableReason(err error) string {
	if err == nil {
		return "unspecified memory worker error"
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
