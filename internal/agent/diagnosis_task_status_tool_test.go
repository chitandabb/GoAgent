package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

func TestGetDiagnosisTaskStatusToolReturnsSafeProgressSummary(t *testing.T) {
	taskID, reportID := uuid.New(), uuid.New()
	startedAt := time.Date(2026, time.August, 8, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	reader := &diagnosisTaskStatusReaderStub{result: conversation.DiagnosisTaskStatusResult{
		Task: diagnosis.DiagnosisTask{
			ID: taskID, Status: diagnosis.TaskSucceeded, AttemptCount: 2,
			ReportID: &reportID, StartedAt: &startedAt, UpdatedAt: startedAt.Add(time.Minute),
		},
	}}
	current, err := NewGetDiagnosisTaskStatusTool(reader)
	if err != nil {
		t.Fatalf("NewGetDiagnosisTaskStatusTool(): %v", err)
	}
	result, err := current.InvokableRun(context.Background(), `{"taskId":"`+taskID.String()+`"}`)
	if err != nil {
		t.Fatalf("InvokableRun(): %v", err)
	}
	for _, expected := range []string{
		taskID.String(), `"status":"succeeded"`, `"terminal":true`,
		`"attemptCount":2`, `"reportAvailable":true`, reportID.String(), "2026-08-08T01:30:00Z",
	} {
		if !strings.Contains(result, expected) {
			t.Fatalf("result %q does not contain %q", result, expected)
		}
	}
	if reader.taskID != taskID {
		t.Fatalf("reader task id = %s, want %s", reader.taskID, taskID)
	}
}

func TestGetDiagnosisTaskStatusToolRejectsInvalidTaskID(t *testing.T) {
	reader := &diagnosisTaskStatusReaderStub{}
	current, _ := NewGetDiagnosisTaskStatusTool(reader)
	if _, err := current.InvokableRun(context.Background(), `{"taskId":"not-a-uuid"}`); err == nil {
		t.Fatal("InvokableRun accepted an invalid task ID")
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

type diagnosisTaskStatusReaderStub struct {
	result conversation.DiagnosisTaskStatusResult
	taskID uuid.UUID
	calls  int
}

func (s *diagnosisTaskStatusReaderStub) GetDiagnosisTaskStatus(
	_ context.Context,
	taskID uuid.UUID,
) (conversation.DiagnosisTaskStatusResult, error) {
	s.calls++
	s.taskID = taskID
	return s.result, nil
}
