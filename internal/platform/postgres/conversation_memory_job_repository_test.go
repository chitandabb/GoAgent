package postgres

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"

	"github.com/google/uuid"
)

func TestCurrentSummaryCompletesExecution(t *testing.T) {
	executionID := uuid.New()
	execution := conversationmemoryworker.ExecutionResult{CurrentSnapshotID: executionID, ThroughSeq: 4}
	tests := []struct {
		name      string
		activeID  uuid.UUID
		through   int64
		requested int64
		want      bool
	}{
		{name: "exact published summary", activeID: executionID, through: 4, requested: 4, want: true},
		{name: "newer summary covers job", activeID: uuid.New(), through: 6, requested: 4, want: true},
		{name: "different summary at same boundary", activeID: uuid.New(), through: 4, requested: 4, want: false},
		{name: "reported id has inconsistent boundary", activeID: executionID, through: 5, requested: 4, want: false},
		{name: "current summary does not cover job", activeID: uuid.New(), through: 3, requested: 4, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active := conversationmemory.Snapshot{CandidateSnapshot: conversationmemory.CandidateSnapshot{
				ID: tt.activeID, ThroughSeq: tt.through,
			}}
			if got := currentSummaryCompletesExecution(active, execution, tt.requested); got != tt.want {
				t.Fatalf("currentSummaryCompletesExecution() = %t, want %t", got, tt.want)
			}
		})
	}
}
