package diagnosis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTaskExecutionServiceBuildsClaimAndRenewsSameFencingToken(t *testing.T) {
	taskID := uuid.New()
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	repository := &taskExecutionRepositoryStub{}
	service, err := NewTaskExecutionService(repository, 45*time.Second)
	if err != nil {
		t.Fatalf("NewTaskExecutionService(): %v", err)
	}
	service.clock = func() time.Time { return now }

	claimed, err := service.Claim(context.Background(), taskID, " worker-1 ")
	if err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	if claimed.Disposition != TaskClaimAcquired || repository.claimInput.ClaimOwner != "worker-1" ||
		!repository.claimInput.LeaseUntil.Equal(now.Add(45*time.Second)) {
		t.Fatalf("claimed=%+v input=%+v", claimed, repository.claimInput)
	}

	lease := TaskLease{TaskID: taskID, ClaimOwner: "worker-1", AttemptCount: 2, LeaseUntil: now.Add(45 * time.Second)}
	renewed, err := service.Renew(context.Background(), lease)
	if err != nil || !renewed {
		t.Fatalf("Renew() renewed=%v err=%v", renewed, err)
	}
	if repository.renewInput.AttemptCount != 2 || !repository.renewInput.NewLeaseUntil.Equal(now.Add(45*time.Second)) {
		t.Fatalf("renew input=%+v", repository.renewInput)
	}
}

func TestTaskExecutionServiceRejectsInvalidWorkerAndLease(t *testing.T) {
	service, _ := NewTaskExecutionService(&taskExecutionRepositoryStub{}, time.Minute)
	if _, err := service.Claim(context.Background(), uuid.New(), " "); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Claim() error=%v, want ErrInvalidTask", err)
	}
	if _, err := service.Renew(context.Background(), TaskLease{TaskID: uuid.New(), ClaimOwner: "worker"}); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Renew() error=%v, want ErrInvalidTask", err)
	}
}

type taskExecutionRepositoryStub struct {
	claimInput  TaskClaimRecord
	claimResult TaskClaimResult
	claimErr    error
	renewInput  TaskLeaseRenewal
	renewed     bool
	renewErr    error
}

func (s *taskExecutionRepositoryStub) ClaimTask(_ context.Context, input TaskClaimRecord) (TaskClaimResult, error) {
	s.claimInput = input
	if s.claimErr != nil {
		return TaskClaimResult{}, s.claimErr
	}
	if s.claimResult.Disposition == "" {
		s.claimResult = TaskClaimResult{Disposition: TaskClaimAcquired, Status: TaskRunning}
	}
	return s.claimResult, nil
}

func (s *taskExecutionRepositoryStub) RenewTaskLease(_ context.Context, input TaskLeaseRenewal) (bool, error) {
	s.renewInput = input
	if s.renewErr != nil {
		return false, s.renewErr
	}
	if !s.renewed {
		return true, nil
	}
	return s.renewed, nil
}
