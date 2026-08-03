package diagnosis

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	TaskFailureAgentExecution  = "agent_execution_failed"
	MaxTaskRecoveryReasonRunes = 1000
)

var (
	ErrTaskRecoveryForbidden = errors.New("diagnosis task recovery is forbidden")
	ErrInvalidTaskRecovery   = errors.New("diagnosis task recovery is invalid")
)

// TaskRecovery 是一次管理员运维补偿事实，不会修改原始诊断输入或历史执行记录。
type TaskRecovery struct {
	ID                   uuid.UUID
	TaskID               uuid.UUID
	RecoveredBy          uuid.UUID
	IdempotencyKey       string
	Reason               string
	PreviousErrorCode    string
	PreviousErrorMessage string
	PreviousAttemptCount int
	TaskEventSeq         int64
	OutboxEventID        uuid.UUID
	CreatedAt            time.Time
}

type TaskRecoveryResult struct {
	Recovery TaskRecovery
	Replayed bool
}

type TaskRecoveryRecord struct {
	ID             uuid.UUID
	TaskID         uuid.UUID
	RecoveredBy    uuid.UUID
	IdempotencyKey string
	Reason         string
	RecoveredAt    time.Time
}

type TaskRecoveryRepository interface {
	RecoverFailedTask(ctx context.Context, input TaskRecoveryRecord) (TaskRecoveryResult, error)
}

type TaskRecoveryService struct {
	repository TaskRecoveryRepository
	clock      func() time.Time
}

func NewTaskRecoveryService(repository TaskRecoveryRepository) (*TaskRecoveryService, error) {
	if repository == nil {
		return nil, errors.New("task recovery repository is required")
	}
	return &TaskRecoveryService{
		repository: repository,
		clock:      func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *TaskRecoveryService) Recover(
	ctx context.Context,
	actor TaskActor,
	taskID uuid.UUID,
	idempotencyKey string,
	reason string,
) (TaskRecoveryResult, error) {
	if s == nil || s.repository == nil {
		return TaskRecoveryResult{}, errors.New("task recovery service is unavailable")
	}
	if actor.UserID == uuid.Nil || !actor.IsAdmin {
		return TaskRecoveryResult{}, ErrTaskRecoveryForbidden
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	reason = strings.TrimSpace(reason)
	if taskID == uuid.Nil || idempotencyKey == "" || len(idempotencyKey) > 128 ||
		reason == "" || len([]rune(reason)) > MaxTaskRecoveryReasonRunes {
		return TaskRecoveryResult{}, ErrInvalidTaskRecovery
	}
	if _, err := uuid.Parse(idempotencyKey); err != nil {
		return TaskRecoveryResult{}, ErrInvalidTaskRecovery
	}
	return s.repository.RecoverFailedTask(ctx, TaskRecoveryRecord{
		ID: uuid.New(), TaskID: taskID, RecoveredBy: actor.UserID,
		IdempotencyKey: idempotencyKey, Reason: reason, RecoveredAt: s.clock().UTC(),
	})
}

func IsRecoverableTaskFailure(code string) bool {
	return strings.TrimSpace(code) == TaskFailureAgentExecution
}
