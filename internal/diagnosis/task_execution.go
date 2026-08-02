package diagnosis

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TaskClaimDisposition string

const (
	TaskClaimAcquired              TaskClaimDisposition = "acquired"
	TaskClaimLeaseHeld             TaskClaimDisposition = "lease_held"
	TaskClaimCancellationRequested TaskClaimDisposition = "cancellation_requested"
	TaskClaimTerminal              TaskClaimDisposition = "terminal"
)

type TaskLease struct {
	TaskID       uuid.UUID
	ClaimOwner   string
	AttemptCount int
	LeaseUntil   time.Time
}

type TaskClaimResult struct {
	Disposition TaskClaimDisposition
	Status      TaskStatus
	Lease       *TaskLease
}

type TaskClaimRecord struct {
	TaskID     uuid.UUID
	ClaimOwner string
	ClaimedAt  time.Time
	LeaseUntil time.Time
}

type TaskLeaseRenewal struct {
	TaskLease
	RenewedAt     time.Time
	NewLeaseUntil time.Time
}

// TaskExecutionRepository 是 Worker 领取和续租所需的最小持久化契约。
// 后续步骤、证据和报告写入必须继续携带同一个 TaskLease 作为 fencing token。
type TaskExecutionRepository interface {
	ClaimTask(ctx context.Context, input TaskClaimRecord) (TaskClaimResult, error)
	RenewTaskLease(ctx context.Context, input TaskLeaseRenewal) (bool, error)
}

type TaskExecutionService struct {
	repository    TaskExecutionRepository
	leaseDuration time.Duration
	clock         func() time.Time
}

func NewTaskExecutionService(repository TaskExecutionRepository, leaseDuration time.Duration) (*TaskExecutionService, error) {
	if repository == nil {
		return nil, errors.New("task execution repository is nil")
	}
	if leaseDuration <= 0 {
		return nil, errors.New("task execution lease duration must be positive")
	}
	return &TaskExecutionService{
		repository: repository, leaseDuration: leaseDuration,
		clock: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *TaskExecutionService) Claim(ctx context.Context, taskID uuid.UUID, workerID string) (TaskClaimResult, error) {
	if s == nil || s.repository == nil {
		return TaskClaimResult{}, errors.New("task execution service is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	if taskID == uuid.Nil || workerID == "" || len(workerID) > 128 {
		return TaskClaimResult{}, ErrInvalidTask
	}
	now := s.clock().UTC()
	return s.repository.ClaimTask(ctx, TaskClaimRecord{
		TaskID: taskID, ClaimOwner: workerID, ClaimedAt: now, LeaseUntil: now.Add(s.leaseDuration),
	})
}

func (s *TaskExecutionService) Renew(ctx context.Context, lease TaskLease) (bool, error) {
	if s == nil || s.repository == nil {
		return false, errors.New("task execution service is unavailable")
	}
	lease.ClaimOwner = strings.TrimSpace(lease.ClaimOwner)
	if lease.TaskID == uuid.Nil || lease.ClaimOwner == "" || len(lease.ClaimOwner) > 128 || lease.AttemptCount < 1 {
		return false, ErrInvalidTask
	}
	now := s.clock().UTC()
	return s.repository.RenewTaskLease(ctx, TaskLeaseRenewal{
		TaskLease: lease, RenewedAt: now, NewLeaseUntil: now.Add(s.leaseDuration),
	})
}
