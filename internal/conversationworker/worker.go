package conversationworker

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

const (
	defaultMaxAttempts = 4
	maxFailureMessage  = 1000
)

type Repository interface {
	ClaimTurn(ctx context.Context, turnID uuid.UUID, workerID string, claimedAt, leaseExpiresAt time.Time) (conversation.TurnExecution, error)
	RenewTurnExecution(ctx context.Context, turnID uuid.UUID, workerID string, renewedAt, leaseExpiresAt time.Time) (bool, error)
	QueueTurnRetry(ctx context.Context, userID, turnID uuid.UUID, workerID string, scheduledAt, retryAt time.Time) error
	FailTurnExecution(ctx context.Context, userID, turnID uuid.UUID, workerID string, failure *conversation.AgentRunFailureRecord, failedAt time.Time) error
}

type Executor interface {
	ExecuteAcceptedTurn(ctx context.Context, execution conversation.TurnExecution, workerID string) (conversation.ConversationTurn, error)
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
	MaxAttempts   int
	Clock         func() time.Time
}

type Worker struct {
	repository    Repository
	executor      Executor
	workerID      string
	leaseDuration time.Duration
	renewInterval time.Duration
	maxAttempts   int
	clock         func() time.Time
}

func New(repository Repository, executor Executor, cfg Config) (*Worker, error) {
	if repository == nil || executor == nil {
		return nil, errors.New("conversation worker dependencies are nil")
	}
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	if cfg.WorkerID == "" || len(cfg.WorkerID) > 128 {
		return nil, errors.New("conversation worker id is invalid")
	}
	if cfg.LeaseDuration < time.Second || cfg.LeaseDuration > 10*time.Minute ||
		cfg.RenewInterval <= 0 || cfg.RenewInterval*2 >= cfg.LeaseDuration {
		return nil, errors.New("conversation worker lease configuration is invalid")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 10 {
		return nil, errors.New("conversation worker max attempts must be between 1 and 10")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{
		repository: repository, executor: executor, workerID: cfg.WorkerID,
		leaseDuration: cfg.LeaseDuration, renewInterval: cfg.RenewInterval,
		maxAttempts: cfg.MaxAttempts, clock: cfg.Clock,
	}, nil
}

func (w *Worker) Process(ctx context.Context, incoming IncomingMessage) Outcome {
	message, err := ParseMessage(incoming)
	if err != nil {
		return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
	}
	claimedAt := w.clock().UTC()
	execution, err := w.repository.ClaimTurn(
		ctx, message.TurnID, w.workerID, claimedAt, claimedAt.Add(w.leaseDuration),
	)
	if err != nil {
		switch {
		case errors.Is(err, conversation.ErrTurnAlreadyCompleted):
			return Outcome{Action: ActionAck, Reason: "conversation turn is already completed"}
		case errors.Is(err, repository.ErrNotFound):
			return Outcome{Action: ActionDeadLetter, Reason: "conversation turn does not exist"}
		case errors.Is(err, conversation.ErrCommandNotLatest),
			errors.Is(err, conversation.ErrConversationArchived),
			errors.Is(err, conversation.ErrInvalidMessage):
			return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
		case errors.Is(err, conversation.ErrTurnInProgress):
			return retryOutcome(30*time.Second, err)
		default:
			return retryOutcome(30*time.Second, err)
		}
	}
	return w.execute(ctx, execution)
}

func (w *Worker) execute(ctx context.Context, execution conversation.TurnExecution) Outcome {
	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	heartbeatDone := make(chan error, 1)
	go w.renewLease(executionCtx, cancelExecution, execution.TurnID, heartbeatDone)
	_, executionErr := w.executor.ExecuteAcceptedTurn(executionCtx, execution, w.workerID)
	cancelExecution(nil)
	heartbeatErr := <-heartbeatDone

	if executionErr == nil {
		return Outcome{Action: ActionAck, Reason: "conversation assistant message committed"}
	}
	if ctx.Err() != nil {
		return Outcome{Action: ActionRequeue, Reason: stableReason(ctx.Err())}
	}
	if heartbeatErr != nil || errors.Is(executionErr, conversation.ErrTurnLeaseLost) {
		if heartbeatErr != nil {
			executionErr = errors.Join(executionErr, heartbeatErr)
		}
		return retryOutcome(30*time.Second, executionErr)
	}
	if execution.AttemptCount >= w.maxAttempts {
		return w.failPermanently(ctx, execution, executionErr)
	}
	scheduledAt := w.clock().UTC()
	retryAt := scheduledAt.Add(retryDelay(execution.AttemptCount))
	if err := w.repository.QueueTurnRetry(
		ctx, execution.Actor.UserID, execution.TurnID, w.workerID, scheduledAt, retryAt,
	); err != nil {
		return retryOutcome(30*time.Second, errors.Join(executionErr, err))
	}
	return retryOutcome(retryAt.Sub(scheduledAt), executionErr)
}

func (w *Worker) renewLease(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	turnID uuid.UUID,
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
			renewedAt := w.clock().UTC()
			owned, err := w.repository.RenewTurnExecution(
				ctx, turnID, w.workerID, renewedAt, renewedAt.Add(w.leaseDuration),
			)
			if err == nil && owned {
				continue
			}
			if err == nil {
				err = conversation.ErrTurnLeaseLost
			}
			cancel(err)
			done <- err
			return
		}
	}
}

func (w *Worker) failPermanently(ctx context.Context, execution conversation.TurnExecution, cause error) Outcome {
	var failure *conversation.AgentRunFailureRecord
	if record, ok := conversation.AgentRunFailureRecordFrom(cause); ok {
		failure = &record
	}
	err := w.repository.FailTurnExecution(
		ctx, execution.Actor.UserID, execution.TurnID, w.workerID, failure, w.clock().UTC(),
	)
	if err != nil {
		return retryOutcome(30*time.Second, errors.Join(cause, err))
	}
	return Outcome{Action: ActionDeadLetter, Reason: stableReason(cause)}
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
