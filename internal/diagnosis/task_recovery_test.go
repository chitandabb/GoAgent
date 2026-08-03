package diagnosis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTaskRecoveryServiceRequiresAdminAndNormalizesInput(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	repository := &taskRecoveryRepositoryStub{result: TaskRecoveryResult{
		Recovery: TaskRecovery{ID: uuid.New(), TaskID: uuid.New()},
	}}
	service, err := NewTaskRecoveryService(repository)
	if err != nil {
		t.Fatalf("NewTaskRecoveryService(): %v", err)
	}
	service.clock = func() time.Time { return now }
	taskID := uuid.New()
	key := uuid.NewString()

	if _, err := service.Recover(context.Background(), TaskActor{UserID: uuid.New()}, taskID, key, "恢复"); !errors.Is(err, ErrTaskRecoveryForbidden) {
		t.Fatalf("analyst Recover() error = %v, want ErrTaskRecoveryForbidden", err)
	}
	result, err := service.Recover(context.Background(), TaskActor{UserID: uuid.New(), IsAdmin: true}, taskID, "  "+key+" ", "  模型服务已恢复  ")
	if err != nil {
		t.Fatalf("admin Recover(): %v", err)
	}
	if result.Recovery.ID == uuid.Nil || repository.input.TaskID != taskID ||
		repository.input.IdempotencyKey != key || repository.input.Reason != "模型服务已恢复" ||
		!repository.input.RecoveredAt.Equal(now) {
		t.Fatalf("result=%+v input=%+v", result, repository.input)
	}
}

func TestTaskRecoveryServiceRejectsInvalidInput(t *testing.T) {
	service, _ := NewTaskRecoveryService(&taskRecoveryRepositoryStub{})
	actor := TaskActor{UserID: uuid.New(), IsAdmin: true}
	tests := []struct {
		name   string
		taskID uuid.UUID
		key    string
		reason string
	}{
		{name: "missing task", key: uuid.NewString(), reason: "恢复"},
		{name: "invalid key", taskID: uuid.New(), key: "not-uuid", reason: "恢复"},
		{name: "missing reason", taskID: uuid.New(), key: uuid.NewString()},
		{name: "long reason", taskID: uuid.New(), key: uuid.NewString(), reason: strings.Repeat("a", MaxTaskRecoveryReasonRunes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Recover(context.Background(), actor, test.taskID, test.key, test.reason); !errors.Is(err, ErrInvalidTaskRecovery) {
				t.Fatalf("Recover() error = %v, want ErrInvalidTaskRecovery", err)
			}
		})
	}
}

func TestIsRecoverableTaskFailureIsAllowlisted(t *testing.T) {
	if !IsRecoverableTaskFailure(TaskFailureAgentExecution) {
		t.Fatal("agent execution failure should be recoverable")
	}
	for _, code := range []string{"", "invalid_task_execution_input", "permission_denied"} {
		if IsRecoverableTaskFailure(code) {
			t.Fatalf("failure %q should not be recoverable", code)
		}
	}
}

type taskRecoveryRepositoryStub struct {
	input  TaskRecoveryRecord
	result TaskRecoveryResult
	err    error
}

func (s *taskRecoveryRepositoryStub) RecoverFailedTask(
	_ context.Context,
	input TaskRecoveryRecord,
) (TaskRecoveryResult, error) {
	s.input = input
	if s.err != nil {
		return TaskRecoveryResult{}, s.err
	}
	if s.result.Recovery.ID == uuid.Nil {
		s.result.Recovery = TaskRecovery{
			ID: input.ID, TaskID: input.TaskID, RecoveredBy: input.RecoveredBy,
			IdempotencyKey: input.IdempotencyKey, Reason: input.Reason, CreatedAt: input.RecoveredAt,
		}
	}
	return s.result, nil
}
