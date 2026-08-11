package conversationmemoryworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
)

func TestServiceExecutorReturnsUnactivatedCandidate(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	lease := validLease(now)
	repository := &executorMemoryRepository{}
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository,
		Compactor: executorCompactorFunc(func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
			return conversationmemory.CompactionOutput{
				Payload: validExecutorPayload(),
				Usage:   conversationmemory.SummaryUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
			}, nil
		}),
		SchemaVersion: conversationmemory.CurrentSchemaVersion, MaxPayloadBytes: 64 * 1024,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		MaxAttempts: 1, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	executor, err := NewServiceExecutor(service, executorActivationGateFunc(func(context.Context, conversationmemory.Snapshot) error {
		return nil
	}))
	if err != nil {
		t.Fatalf("NewServiceExecutor(): %v", err)
	}

	result, err := executor.Execute(context.Background(), validTask(lease))
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if result.CandidateSnapshotID == nil || result.CurrentSnapshotID != nil || result.ThroughSeq != 2 ||
		repository.active != nil || repository.saved == nil || repository.saved.ID != *result.CandidateSnapshotID {
		t.Fatalf("result/repository = %+v / %+v", result, repository)
	}
}

func TestServiceExecutorUsesOneModelCallPerDurableJobAttempt(t *testing.T) {
	now := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	lease := validLease(now)
	lease.AttemptCount = 2
	calls := 0
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: &executorMemoryRepository{},
		Compactor: executorCompactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
			calls++
			if input.Attempt != lease.AttemptCount {
				t.Fatalf("compaction attempt = %d, want %d", input.Attempt, lease.AttemptCount)
			}
			return conversationmemory.CompactionOutput{}, errors.New("provider unavailable")
		}),
		SchemaVersion: conversationmemory.CurrentSchemaVersion, MaxPayloadBytes: 64 * 1024,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		MaxAttempts: 3, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	executor, err := NewServiceExecutor(service, executorActivationGateFunc(func(context.Context, conversationmemory.Snapshot) error {
		return nil
	}))
	if err != nil {
		t.Fatalf("NewServiceExecutor(): %v", err)
	}

	_, err = executor.Execute(context.Background(), validTask(lease))
	if !errors.Is(err, conversationmemory.ErrCompactionFailed) || calls != 1 {
		t.Fatalf("Execute() error/calls = %v/%d", err, calls)
	}
}

type executorCompactorFunc func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error)

func (f executorCompactorFunc) Compact(ctx context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
	return f(ctx, input)
}

type executorActivationGateFunc func(context.Context, conversationmemory.Snapshot) error

func (f executorActivationGateFunc) ValidateForActivation(ctx context.Context, snapshot conversationmemory.Snapshot) error {
	return f(ctx, snapshot)
}

type executorMemoryRepository struct {
	saved  *conversationmemory.Snapshot
	active *conversationmemory.Snapshot
}

func (r *executorMemoryRepository) Latest(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if r.saved == nil {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *r.saved
	return &copy, nil
}

func (r *executorMemoryRepository) Get(context.Context, uuid.UUID) (conversationmemory.Snapshot, error) {
	return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
}

func (r *executorMemoryRepository) Save(_ context.Context, candidate conversationmemory.CandidateSnapshot) (conversationmemory.Snapshot, error) {
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
	r.saved = &snapshot
	return snapshot, nil
}

func (r *executorMemoryRepository) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if r.active == nil {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *r.active
	return &copy, nil
}

func (r *executorMemoryRepository) ActiveIdentity(ctx context.Context, conversationID uuid.UUID) (conversationmemory.ActiveSnapshotIdentity, error) {
	active, err := r.Active(ctx, conversationID)
	if err != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, err
	}
	return conversationmemory.ActiveSnapshotIdentity{
		ConversationID: active.ConversationID, SnapshotID: active.ID,
		Version: active.Version, PayloadSHA256: active.PayloadSHA256,
	}, nil
}

func (r *executorMemoryRepository) Activate(context.Context, conversationmemory.ActivationRequest) (conversationmemory.Snapshot, error) {
	panic("ServiceExecutor must not activate a candidate directly")
}

func validExecutorPayload() conversationmemory.Payload {
	return conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{
			EntryID: "goal_context", Content: "完成上下文治理",
			SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive,
		},
		Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
		Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
		OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
		TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
	}
}
