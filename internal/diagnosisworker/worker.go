package diagnosisworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

const (
	defaultMaxAttempts = 4 // 首次执行加三次自动重试。
	maxFailureMessage  = 1000
)

var ErrLeaseLost = errors.New("diagnosis worker lease lost")

type DataSource struct {
	ID         uuid.UUID
	Role       agent.DataSourceRole
	SafetyMode agent.DataSourceSafetyMode
}

type Task struct {
	ID           uuid.UUID
	CreatedBy    uuid.UUID
	Role         auth.Role
	RequestText  string
	RequestScope map[string]any
	CaseSnapshot externalcase.ExternalCase
	DataSources  []DataSource
}

type ExecutionResult struct {
	Orchestration agent.OrchestrationResult
	ModelProvider string
	ModelID       string
	PromptVersion string
}

type TaskLeaseService interface {
	Claim(ctx context.Context, taskID uuid.UUID, workerID string) (diagnosis.TaskClaimResult, error)
	Renew(ctx context.Context, lease diagnosis.TaskLease) (bool, error)
}

type Repository interface {
	LoadTask(ctx context.Context, lease diagnosis.TaskLease, now time.Time) (Task, error)
	Complete(ctx context.Context, lease diagnosis.TaskLease, result ExecutionResult, completedAt time.Time) (bool, error)
	ReleaseForRetry(ctx context.Context, lease diagnosis.TaskLease, code, message string, releasedAt time.Time) (bool, error)
	Fail(ctx context.Context, lease diagnosis.TaskLease, code, message string, failedAt time.Time) (bool, error)
	FinalizeCancellation(ctx context.Context, taskID uuid.UUID, completedAt time.Time) (diagnosis.TaskStatus, error)
}

type AgentExecutor interface {
	Execute(ctx context.Context, task Task) (ExecutionResult, error)
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
	RenewInterval time.Duration
	MaxAttempts   int
	Clock         func() time.Time
}

type Worker struct {
	leases        TaskLeaseService
	repository    Repository
	executor      AgentExecutor
	workerID      string
	renewInterval time.Duration
	maxAttempts   int
	clock         func() time.Time
}

func New(
	leases TaskLeaseService,
	repository Repository,
	executor AgentExecutor,
	cfg Config,
) (*Worker, error) {
	if leases == nil || repository == nil || executor == nil {
		return nil, errors.New("diagnosis worker dependencies are nil")
	}
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	if cfg.WorkerID == "" || len(cfg.WorkerID) > 128 {
		return nil, errors.New("diagnosis worker id is invalid")
	}
	if cfg.RenewInterval <= 0 {
		return nil, errors.New("diagnosis worker renew interval must be positive")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 10 {
		return nil, errors.New("diagnosis worker max attempts must be between 1 and 10")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{
		leases: leases, repository: repository, executor: executor,
		workerID: cfg.WorkerID, renewInterval: cfg.RenewInterval,
		maxAttempts: cfg.MaxAttempts, clock: cfg.Clock,
	}, nil
}

func (w *Worker) Process(ctx context.Context, incoming IncomingMessage) Outcome {
	message, err := ParseMessage(incoming)
	if err != nil {
		return Outcome{Action: ActionDeadLetter, Reason: stableReason(err)}
	}
	claim, err := w.leases.Claim(ctx, message.TaskID, w.workerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Outcome{Action: ActionDeadLetter, Reason: "diagnosis task does not exist"}
		}
		return retryOutcome(30*time.Second, err)
	}
	switch claim.Disposition {
	case diagnosis.TaskClaimTerminal:
		if claim.Status == diagnosis.TaskFailed {
			return Outcome{Action: ActionDeadLetter, Reason: "task is already failed"}
		}
		return Outcome{Action: ActionAck, Reason: "task is already terminal"}
	case diagnosis.TaskClaimCancellationRequested:
		return w.finalizeCancellation(ctx, message.TaskID)
	case diagnosis.TaskClaimLeaseHeld:
		return Outcome{Action: ActionRetry, RetryDelay: 30 * time.Second, Reason: "task lease is held"}
	case diagnosis.TaskClaimAcquired:
		if claim.Lease == nil {
			return Outcome{Action: ActionRetry, RetryDelay: 30 * time.Second, Reason: "claim returned no lease"}
		}
		return w.execute(ctx, *claim.Lease)
	default:
		return Outcome{Action: ActionDeadLetter, Reason: "unsupported claim disposition"}
	}
}

func (w *Worker) execute(ctx context.Context, lease diagnosis.TaskLease) Outcome {
	now := w.clock().UTC()
	task, err := w.repository.LoadTask(ctx, lease, now)
	if err != nil {
		if errors.Is(err, diagnosis.ErrInvalidTaskSnapshot) || errors.Is(err, diagnosis.ErrInvalidTask) ||
			errors.Is(err, diagnosis.ErrTaskForbidden) {
			return w.failPermanent(ctx, lease, "invalid_task_execution_input", err)
		}
		return w.releaseAndRetry(ctx, lease, err)
	}

	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	heartbeatDone := make(chan error, 1)
	go w.renewLease(executionCtx, cancelExecution, lease, heartbeatDone)
	result, executionErr := w.executor.Execute(executionCtx, task)
	cancelExecution(nil)
	heartbeatErr := <-heartbeatDone

	if ctx.Err() != nil {
		return Outcome{Action: ActionRequeue, Reason: stableReason(ctx.Err())}
	}
	if heartbeatErr != nil {
		return w.handleLostLease(ctx, lease.TaskID, heartbeatErr)
	}
	if executionErr != nil {
		return w.releaseAndRetry(ctx, lease, executionErr)
	}

	// 终态提交前再续租一次，缩小最后一次心跳和数据库提交之间的失效窗口。
	owned, err := w.leases.Renew(ctx, lease)
	if err != nil || !owned {
		if err == nil {
			err = ErrLeaseLost
		}
		return w.handleLostLease(ctx, lease.TaskID, err)
	}
	completed, err := w.repository.Complete(ctx, lease, result, w.clock().UTC())
	if err != nil {
		return w.releaseAndRetry(ctx, lease, err)
	}
	if !completed {
		return w.handleLostLease(ctx, lease.TaskID, ErrLeaseLost)
	}
	return Outcome{Action: ActionAck, Reason: "diagnosis result committed"}
}

func (w *Worker) renewLease(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	lease diagnosis.TaskLease,
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
			owned, err := w.leases.Renew(ctx, lease)
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

func (w *Worker) releaseAndRetry(ctx context.Context, lease diagnosis.TaskLease, cause error) Outcome {
	if lease.AttemptCount >= w.maxAttempts {
		return w.failPermanent(ctx, lease, "agent_execution_failed", cause)
	}
	released, err := w.repository.ReleaseForRetry(
		ctx, lease, "agent_execution_retry", stableReason(cause), w.clock().UTC(),
	)
	if err != nil {
		return retryOutcome(retryDelay(lease.AttemptCount), err)
	}
	if !released {
		return w.handleLostLease(ctx, lease.TaskID, ErrLeaseLost)
	}
	return retryOutcome(retryDelay(lease.AttemptCount), cause)
}

func (w *Worker) failPermanent(ctx context.Context, lease diagnosis.TaskLease, code string, cause error) Outcome {
	failed, err := w.repository.Fail(ctx, lease, code, stableReason(cause), w.clock().UTC())
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if !failed {
		return w.handleLostLease(ctx, lease.TaskID, ErrLeaseLost)
	}
	return Outcome{Action: ActionDeadLetter, Reason: stableReason(cause)}
}

func (w *Worker) handleLostLease(ctx context.Context, taskID uuid.UUID, cause error) Outcome {
	status, err := w.repository.FinalizeCancellation(ctx, taskID, w.clock().UTC())
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if isTerminal(status) {
		return Outcome{Action: ActionAck, Reason: "task reached terminal state"}
	}
	return retryOutcome(30*time.Second, cause)
}

func (w *Worker) finalizeCancellation(ctx context.Context, taskID uuid.UUID) Outcome {
	status, err := w.repository.FinalizeCancellation(ctx, taskID, w.clock().UTC())
	if err != nil {
		return retryOutcome(30*time.Second, err)
	}
	if isTerminal(status) {
		return Outcome{Action: ActionAck, Reason: "task cancellation committed"}
	}
	return Outcome{Action: ActionRetry, RetryDelay: 30 * time.Second, Reason: "task cancellation raced with execution"}
}

func isTerminal(status diagnosis.TaskStatus) bool {
	return status == diagnosis.TaskSucceeded || status == diagnosis.TaskFailed || status == diagnosis.TaskCancelled
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
