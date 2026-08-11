package conversationmemory_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
)

type compactorFunc func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error)

func (f compactorFunc) Compact(ctx context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
	return f(ctx, input)
}

type memoryRepositoryStub struct {
	latest *conversationmemory.Snapshot
	saved  []conversationmemory.CandidateSnapshot
}

func (r *memoryRepositoryStub) Latest(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if r.latest == nil {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *r.latest
	return &copy, nil
}

func (r *memoryRepositoryStub) Get(_ context.Context, snapshotID uuid.UUID) (conversationmemory.Snapshot, error) {
	if r.latest == nil || r.latest.ID != snapshotID {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
	}
	return *r.latest, nil
}

func (r *memoryRepositoryStub) Save(_ context.Context, candidate conversationmemory.CandidateSnapshot) (conversationmemory.Snapshot, error) {
	r.saved = append(r.saved, candidate)
	version := int64(1)
	if r.latest != nil {
		version = r.latest.Version + 1
	}
	result := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: version}
	r.latest = &result
	return result, nil
}

func TestConversationMemoryGeneratesAnInitialShadowSnapshot(t *testing.T) {
	conversationID := uuid.New()
	repository := &memoryRepositoryStub{}
	compactor := compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		if input.PreviousSnapshot != nil || len(input.NewMessages) != 3 || input.NewMessages[0].Seq != 1 || input.Attempt != 1 {
			t.Fatalf("initial compaction input = %+v", input)
		}
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 120, CompletionTokens: 40, TotalTokens: 160, CachedTokens: 20},
		}, nil
	})
	createdAt := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	service := newMemoryService(t, repository, compactor, createdAt, 3)

	snapshot, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID:    conversationID,
		CompletedMessages: initialMessages(conversationID),
	})
	if err != nil {
		t.Fatalf("GenerateShadow() error = %v", err)
	}
	if snapshot.ConversationID != conversationID || snapshot.Version != 1 || snapshot.FromSeq != 1 ||
		snapshot.ThroughSeq != 3 || snapshot.SupersedesSnapshotID != nil ||
		snapshot.SchemaVersion != conversationmemory.CurrentSchemaVersion || snapshot.Status != conversationmemory.SnapshotStatusCandidate ||
		snapshot.SummaryModelProfile != "conversation-memory" || snapshot.SummaryModelProvider != "dashscope" ||
		snapshot.SummaryModelID != "qwen3.6-flash" || snapshot.PromptVersion != "conversation-memory-v1" ||
		snapshot.PayloadSHA256 == "" || snapshot.Usage.TotalTokens != 160 || !snapshot.CreatedAt.Equal(createdAt) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(repository.saved) != 1 {
		t.Fatalf("saved candidates = %d, want 1", len(repository.saved))
	}
}

func TestConversationMemoryRetriesInvalidModelOutputWithoutPersistingFallback(t *testing.T) {
	conversationID := uuid.New()
	repository := &memoryRepositoryStub{}
	attempts := 0
	compactor := compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		attempts++
		payload := validPayload()
		usage := conversationmemory.SummaryUsage{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50}
		if attempts == 1 {
			payload.Facts[0].SourceMessageSeqs = []int64{2}
			return conversationmemory.CompactionOutput{Payload: payload, Usage: usage}, nil
		}
		if input.RepairCode != "user_source_required" {
			t.Fatalf("second attempt repair code = %q", input.RepairCode)
		}
		return conversationmemory.CompactionOutput{Payload: payload, Usage: usage}, nil
	})
	service := newMemoryService(t, repository, compactor, time.Now().UTC(), 2)

	if _, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	}); err != nil {
		t.Fatalf("GenerateShadow() error = %v", err)
	}
	if attempts != 2 || len(repository.saved) != 1 {
		t.Fatalf("attempts/saves = %d/%d, want 2/1", attempts, len(repository.saved))
	}
}

func TestConversationMemoryIncrementallyMergesThePreviousSnapshot(t *testing.T) {
	conversationID := uuid.New()
	previousCandidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, FromSeq: 1, ThroughSeq: 3,
		SchemaVersion:       conversationmemory.CurrentSchemaVersion,
		SummaryModelProfile: "conversation-memory", SummaryModelProvider: "dashscope", SummaryModelID: "qwen3.6-flash",
		PromptVersion: "conversation-memory-v1", Payload: validPayload(),
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
		CreatedAt: time.Now().Add(-time.Minute).UTC(),
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot(previous): %v", err)
	}
	previous := conversationmemory.Snapshot{CandidateSnapshot: previousCandidate, Version: 1}
	repository := &memoryRepositoryStub{latest: &previous}
	compactor := compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		if input.PreviousSnapshot == nil || input.PreviousSnapshot.ID != previous.ID || len(input.NewMessages) != 2 ||
			input.NewMessages[0].Seq != 4 || input.NewMessages[1].Seq != 5 {
			t.Fatalf("incremental compaction input = %+v", input)
		}
		payload := validPayload()
		payload.Decisions = append(payload.Decisions, conversationmemory.Entry{
			EntryID: "decision_tail_ratio", Content: "Tail 占窗口 15%",
			SourceMessageSeqs: []int64{4, 5}, Status: conversationmemory.EntryStatusActive,
		})
		return conversationmemory.CompactionOutput{
			Payload: payload, Usage: conversationmemory.SummaryUsage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
		}, nil
	})
	service := newMemoryService(t, repository, compactor, time.Now().UTC(), 1)

	snapshot, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID,
		CompletedMessages: []conversation.Message{
			{ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleUser, Content: "Tail 就按 15%"},
			{ID: uuid.New(), ConversationID: conversationID, Seq: 5, Role: conversation.MessageRoleAssistant, Content: "已确认"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateShadow() error = %v", err)
	}
	if snapshot.Version != 2 || snapshot.SupersedesSnapshotID == nil || *snapshot.SupersedesSnapshotID != previous.ID ||
		snapshot.FromSeq != 1 || snapshot.ThroughSeq != 5 || len(snapshot.Payload.Decisions) != 1 {
		t.Fatalf("incremental snapshot = %+v", snapshot)
	}
}

func TestConversationMemoryReturnsRetryableErrorAfterBoundedAttempts(t *testing.T) {
	conversationID := uuid.New()
	repository := &memoryRepositoryStub{}
	attempts := 0
	service := newMemoryService(t, repository, compactorFunc(func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		attempts++
		return conversationmemory.CompactionOutput{}, errors.New("provider unavailable")
	}), time.Now().UTC(), 3)

	_, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	if !errors.Is(err, conversationmemory.ErrCompactionFailed) || attempts != 3 || len(repository.saved) != 0 {
		t.Fatalf("GenerateShadow() error/attempts/saves = %v/%d/%d", err, attempts, len(repository.saved))
	}
}

func newMemoryService(
	t *testing.T,
	repository conversationmemory.Repository,
	compactor conversationmemory.Compactor,
	now time.Time,
	maxAttempts int,
) *conversationmemory.Service {
	t.Helper()
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository, Compactor: compactor,
		SchemaVersion: conversationmemory.CurrentSchemaVersion, MaxPayloadBytes: 64 * 1024,
		SummaryModelProfile: "conversation-memory", SummaryModelProvider: "dashscope", SummaryModelID: "qwen3.6-flash",
		PromptVersion: "conversation-memory-v1", MaxAttempts: maxAttempts,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func initialMessages(conversationID uuid.UUID) []conversation.Message {
	return []conversation.Message{
		{
			ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser,
			Content: "目标是完成上下文治理，服务器使用 UTC。",
		},
		{
			ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant,
			Content: "知识库采用不可变版本。",
			Citations: []conversation.MessageCitation{
				{
					Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
					SourceRef:     "knowledge:8c4c15e7-1d72-453d-b1a0-c64f70a03dc8/2cd3198e-0bff-4ab2-bc4c-e6838043039f",
					ContentSHA256: strings.Repeat("a", 64),
				},
			},
		},
		{
			ID: uuid.New(), ConversationID: conversationID, Seq: 3, Role: conversation.MessageRoleUser,
			Content: "更正：服务器使用 Asia/Shanghai，并运行固定集评测。",
			TaskReferences: []conversation.TaskReference{
				{TaskID: uuid.MustParse("f954b23d-28c3-4dd4-a94b-8e859d3c6dcc"), Kind: conversation.ReferenceKindReferenced},
			},
		},
	}
}
