package diagnosis

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
)

func TestDiagnosisReportServiceAuthorizesOwnerAndAdmin(t *testing.T) {
	taskID := uuid.New()
	ownerID := uuid.New()
	reportID := uuid.New()
	repositoryStub := &diagnosisReportRepositoryStub{lookup: TaskReportLookup{
		TaskID: taskID, TaskCreator: ownerID, TaskStatus: TaskSucceeded,
		Report: &DiagnosisReport{ID: reportID, TaskID: taskID},
	}}
	service, err := NewDiagnosisReportService(repositoryStub)
	if err != nil {
		t.Fatalf("NewDiagnosisReportService(): %v", err)
	}

	for _, actor := range []TaskActor{{UserID: ownerID}, {UserID: uuid.New(), IsAdmin: true}} {
		report, getErr := service.Get(context.Background(), actor, taskID)
		if getErr != nil || report.ID != reportID {
			t.Fatalf("Get(%+v): report=%+v err=%v", actor, report, getErr)
		}
	}
	if repositoryStub.calls != 2 {
		t.Fatalf("repository calls = %d, want 2", repositoryStub.calls)
	}
}

func TestDiagnosisReportServiceRejectsForbiddenAndUnavailableReports(t *testing.T) {
	taskID := uuid.New()
	ownerID := uuid.New()
	tests := []struct {
		name   string
		actor  TaskActor
		lookup TaskReportLookup
		want   error
	}{
		{name: "missing actor", actor: TaskActor{}, want: ErrTaskForbidden},
		{name: "other analyst", actor: TaskActor{UserID: uuid.New()}, lookup: TaskReportLookup{
			TaskID: taskID, TaskCreator: ownerID, TaskStatus: TaskSucceeded,
			Report: &DiagnosisReport{ID: uuid.New(), TaskID: taskID},
		}, want: ErrTaskForbidden},
		{name: "pending report", actor: TaskActor{UserID: ownerID}, lookup: TaskReportLookup{
			TaskID: taskID, TaskCreator: ownerID, TaskStatus: TaskPending,
		}, want: ErrTaskReportUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositoryStub := &diagnosisReportRepositoryStub{lookup: test.lookup}
			service, err := NewDiagnosisReportService(repositoryStub)
			if err != nil {
				t.Fatalf("NewDiagnosisReportService(): %v", err)
			}
			_, err = service.Get(context.Background(), test.actor, taskID)
			if !errors.Is(err, test.want) {
				t.Fatalf("Get() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDiagnosisReportServicePreservesRepositoryErrors(t *testing.T) {
	service, err := NewDiagnosisReportService(&diagnosisReportRepositoryStub{err: repository.ErrNotFound})
	if err != nil {
		t.Fatalf("NewDiagnosisReportService(): %v", err)
	}
	_, err = service.Get(context.Background(), TaskActor{UserID: uuid.New()}, uuid.New())
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Get() error = %v, want repository.ErrNotFound", err)
	}
}

type diagnosisReportRepositoryStub struct {
	lookup TaskReportLookup
	err    error
	calls  int
}

func (s *diagnosisReportRepositoryStub) FindTaskReport(context.Context, uuid.UUID) (TaskReportLookup, error) {
	s.calls++
	return s.lookup, s.err
}
