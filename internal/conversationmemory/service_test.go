package conversationmemory_test

import (
	"context"
	"errors"
	"reflect"
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

type activationGateFunc func(context.Context, conversationmemory.Snapshot) error

func (f activationGateFunc) ValidateForActivation(ctx context.Context, snapshot conversationmemory.Snapshot) error {
	return f(ctx, snapshot)
}

var acceptConversationMemoryActivation = activationGateFunc(func(context.Context, conversationmemory.Snapshot) error {
	return nil
})

type memoryRepositoryStub struct {
	latest *conversationmemory.Snapshot
	saved  []conversationmemory.CandidateSnapshot
}

type activationMemoryRepositoryStub struct {
	*memoryRepositoryStub
	active           *conversationmemory.Snapshot
	activationErr    error
	winnerOnConflict *conversationmemory.Snapshot
}

func TestSnapshotValidateAcceptsPersistedLifecycleStatuses(t *testing.T) {
	createdAt := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: uuid.New(), FromSeq: 1, ThroughSeq: 3,
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload: validPayload(),
		Usage: conversationmemory.SummaryUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedTokens: 10,
		},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot() error = %v", err)
	}
	activatedAt := createdAt.Add(time.Minute)
	for _, status := range []conversationmemory.SnapshotStatus{
		conversationmemory.SnapshotStatusActive,
		conversationmemory.SnapshotStatusSuperseded,
	} {
		t.Run(string(status), func(t *testing.T) {
			persisted := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
			persisted.Status = status
			persisted.ActivatedAt = &activatedAt
			if err := persisted.Validate(); err != nil {
				t.Fatalf("Snapshot.Validate() error = %v", err)
			}
		})
	}
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

func (r *memoryRepositoryStub) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if r.latest == nil || r.latest.Status != conversationmemory.SnapshotStatusActive {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *r.latest
	return &copy, nil
}

func (r *memoryRepositoryStub) ActiveIdentity(ctx context.Context, conversationID uuid.UUID) (conversationmemory.ActiveSnapshotIdentity, error) {
	active, err := r.Active(ctx, conversationID)
	if err != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, err
	}
	return conversationmemory.ActiveSnapshotIdentity{
		ConversationID: active.ConversationID, SnapshotID: active.ID,
		Version: active.Version, PayloadSHA256: active.PayloadSHA256,
	}, nil
}

func (r *memoryRepositoryStub) Activate(
	_ context.Context,
	request conversationmemory.ActivationRequest,
) (conversationmemory.Snapshot, error) {
	if r.latest == nil || r.latest.ID != request.CandidateSnapshotID {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
	}
	activated := *r.latest
	activated.Status = conversationmemory.SnapshotStatusActive
	activatedAt := request.ActivatedAt.UTC()
	activated.ActivatedAt = &activatedAt
	r.latest = &activated
	return activated, nil
}

func (r *activationMemoryRepositoryStub) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if r.active == nil {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *r.active
	return &copy, nil
}

func (r *activationMemoryRepositoryStub) ActiveIdentity(ctx context.Context, conversationID uuid.UUID) (conversationmemory.ActiveSnapshotIdentity, error) {
	active, err := r.Active(ctx, conversationID)
	if err != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, err
	}
	return conversationmemory.ActiveSnapshotIdentity{
		ConversationID: active.ConversationID, SnapshotID: active.ID,
		Version: active.Version, PayloadSHA256: active.PayloadSHA256,
	}, nil
}

func (r *activationMemoryRepositoryStub) Activate(
	_ context.Context,
	request conversationmemory.ActivationRequest,
) (conversationmemory.Snapshot, error) {
	if r.activationErr != nil {
		if r.winnerOnConflict != nil {
			winner := *r.winnerOnConflict
			r.active = &winner
		}
		return conversationmemory.Snapshot{}, r.activationErr
	}
	if r.latest == nil || r.latest.ID != request.CandidateSnapshotID {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
	}
	activated := *r.latest
	activated.Status = conversationmemory.SnapshotStatusActive
	activatedAt := request.ActivatedAt.UTC()
	activated.ActivatedAt = &activatedAt
	r.active = &activated
	return activated, nil
}

func TestConversationMemoryUsesEqualOrNewerCASWinner(t *testing.T) {
	conversationID := uuid.New()
	winner := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	repository := &activationMemoryRepositoryStub{
		memoryRepositoryStub: &memoryRepositoryStub{},
		activationErr:        conversationmemory.ErrSnapshotActivationConflict,
		winnerOnConflict:     &winner,
	}
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 1)

	prepared, err := service.PrepareActive(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
		ActivationGate: acceptConversationMemoryActivation,
	})
	if err != nil {
		t.Fatalf("PrepareActive() CAS winner error = %v", err)
	}
	if prepared.ID != winner.ID || prepared.Status != conversationmemory.SnapshotStatusActive {
		t.Fatalf("CAS winner = %+v, want %s", prepared, winner.ID)
	}
}

func activeSnapshotFixture(
	t *testing.T,
	conversationID uuid.UUID,
	fromSeq, throughSeq int64,
	predecessor *uuid.UUID,
) conversationmemory.Snapshot {
	t.Helper()
	createdAt := time.Now().Add(-time.Minute).UTC()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, SupersedesSnapshotID: predecessor,
		FromSeq: fromSeq, ThroughSeq: throughSeq, SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload:   validPayload(),
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot() fixture error = %v", err)
	}
	activatedAt := createdAt.Add(time.Second)
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = &activatedAt
	result := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
	if err := result.Validate(); err != nil {
		t.Fatalf("active Snapshot fixture error = %v", err)
	}
	return result
}

func TestConversationMemoryPreparesAndActivatesInitialSnapshot(t *testing.T) {
	conversationID := uuid.New()
	repository := &activationMemoryRepositoryStub{memoryRepositoryStub: &memoryRepositoryStub{}}
	compactor := compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		if input.PreviousSnapshot != nil || input.ThroughSeq != 3 {
			t.Fatalf("active compaction input = %+v", input)
		}
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 120, CompletionTokens: 40, TotalTokens: 160},
		}, nil
	})
	service := newMemoryService(t, repository, compactor, time.Now().UTC(), 2)

	snapshot, err := service.PrepareActive(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
		ActivationGate: acceptConversationMemoryActivation,
	})
	if err != nil {
		t.Fatalf("PrepareActive() error = %v", err)
	}
	if snapshot.Status != conversationmemory.SnapshotStatusActive || snapshot.ActivatedAt == nil ||
		snapshot.ThroughSeq != 3 || repository.active == nil || repository.active.ID != snapshot.ID {
		t.Fatalf("active snapshot/repository = %+v / %+v", snapshot, repository.active)
	}
}

func TestConversationMemoryRetriesInvalidHardCompactionThenActivates(t *testing.T) {
	conversationID := uuid.New()
	repository := &activationMemoryRepositoryStub{memoryRepositoryStub: &memoryRepositoryStub{}}
	attempts := 0
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		attempts++
		payload := validPayload()
		if attempts == 1 {
			payload.Facts[0].SourceMessageSeqs = []int64{2}
		} else if input.RepairCode != "user_source_required" {
			t.Fatalf("second hard-compaction repair code = %q", input.RepairCode)
		}
		return conversationmemory.CompactionOutput{
			Payload: payload,
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 2)

	snapshot, err := service.PrepareActive(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
		ActivationGate: acceptConversationMemoryActivation,
	})
	if err != nil {
		t.Fatalf("PrepareActive() retry error = %v", err)
	}
	if attempts != 2 || len(repository.saved) != 1 || snapshot.Status != conversationmemory.SnapshotStatusActive {
		t.Fatalf("attempts/saves/snapshot = %d/%d/%+v", attempts, len(repository.saved), snapshot)
	}
}

func TestConversationMemoryFeedsSchemaFailureCodeIntoRepairAttempt(t *testing.T) {
	repository := &memoryRepositoryStub{}
	var repairCodes []string
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		repairCodes = append(repairCodes, input.RepairCode)
		if input.Attempt == 1 {
			_, err := conversationmemory.DecodePayload([]byte(`{"facts":"invalid"}`))
			return conversationmemory.CompactionOutput{}, err
		}
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 2)
	conversationID := uuid.New()
	_, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repairCodes) != 2 || repairCodes[0] != "" || repairCodes[1] != "payload_schema_top_level_missing_conversation_goal" {
		t.Fatalf("repair codes = %#v", repairCodes)
	}
}

type failureCodedCompactionError struct {
	code         string
	nonRetryable bool
}

func (e failureCodedCompactionError) Error() string                 { return "content-free compaction failure" }
func (e failureCodedCompactionError) CompactionFailureCode() string { return e.code }
func (e failureCodedCompactionError) NonRetryableCompaction() bool  { return e.nonRetryable }

func TestConversationMemoryPreservesBoundedAttemptFailureCodes(t *testing.T) {
	repository := &memoryRepositoryStub{}
	attempts := 0
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		attempts++
		if attempts == 1 {
			return conversationmemory.CompactionOutput{}, failureCodedCompactionError{code: "provider_http_429"}
		}
		return conversationmemory.CompactionOutput{}, failureCodedCompactionError{code: "provider_http_5xx"}
	}), time.Now().UTC(), 3)

	conversationID := uuid.New()
	_, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	if !errors.Is(err, conversationmemory.ErrCompactionFailed) {
		t.Fatalf("GenerateShadow() error = %v, want ErrCompactionFailed", err)
	}
	codes := conversationmemory.CompactionAttemptFailureCodes(err)
	want := []string{"provider_http_429", "provider_http_5xx", "provider_http_5xx"}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("attempt codes = %#v, want %#v", codes, want)
	}
}

func TestConversationMemoryStopsAfterNonRetryableFailureAndPreservesCode(t *testing.T) {
	repository := &memoryRepositoryStub{}
	attempts := 0
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		attempts++
		return conversationmemory.CompactionOutput{}, failureCodedCompactionError{code: "provider_http_401", nonRetryable: true}
	}), time.Now().UTC(), 3)

	conversationID := uuid.New()
	_, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if got := conversationmemory.CompactionAttemptFailureCodes(err); !reflect.DeepEqual(got, []string{"provider_http_401"}) {
		t.Fatalf("attempt codes = %#v, want provider_http_401", got)
	}
}

func TestConversationMemoryPreservesDomainValidationFailureForEveryAttempt(t *testing.T) {
	repository := &memoryRepositoryStub{}
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		payload := validPayload()
		payload.Facts[0].SourceMessageSeqs = []int64{99}
		return conversationmemory.CompactionOutput{
			Payload: payload,
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 2)

	conversationID := uuid.New()
	_, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	want := []string{"source_out_of_range", "source_out_of_range"}
	if got := conversationmemory.CompactionAttemptFailureCodes(err); !reflect.DeepEqual(got, want) {
		t.Fatalf("attempt codes = %#v, want %#v", got, want)
	}
}

func TestNormalizeCompactionFailureCodeRejectsUnboundedValues(t *testing.T) {
	tests := map[string]string{
		"provider_http_429":                         "provider_http_429",
		"source_out_of_range":                       "source_out_of_range",
		"payload_schema_field_facts_string":         "payload_schema_field_facts_string",
		"payload_schema_top_level_missing_facts":    "payload_schema_top_level_missing_facts",
		"tenant_123456789":                          "compaction_failed",
		"payload_schema_field_private_model_string": "compaction_failed",
	}
	for input, want := range tests {
		if got := conversationmemory.NormalizeCompactionFailureCode(input); got != want {
			t.Fatalf("NormalizeCompactionFailureCode(%q) = %q, want %q", input, got, want)
		}
		if got := conversationmemory.ValidCompactionFailureCode(input); got != (input == want) {
			t.Fatalf("ValidCompactionFailureCode(%q) = %v, want %v", input, got, input == want)
		}
	}
}

func TestConversationMemoryPreservesFailureCodesWhenRetryWaitIsCanceled(t *testing.T) {
	repository := &memoryRepositoryStub{}
	ctx, cancel := context.WithCancel(context.Background())
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository,
		Compactor: compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
			cancel()
			return conversationmemory.CompactionOutput{}, failureCodedCompactionError{code: "provider_http_429"}
		}),
		SchemaVersion:   conversationmemory.CurrentSchemaVersion,
		MaxPayloadBytes: 64 * 1024,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "stepfun",
			ModelID: "step-3.5-flash", PromptVersion: "conversation-memory-v2",
		},
		MaxAttempts: 3, RetryBaseDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := uuid.New()
	_, err = service.GenerateShadow(ctx, conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateShadow() error = %v, want context.Canceled", err)
	}
	if got := conversationmemory.CompactionAttemptFailureCodes(err); !reflect.DeepEqual(got, []string{"provider_http_429"}) {
		t.Fatalf("attempt codes = %#v, want provider_http_429", got)
	}
}

type repairCodedCompactionError struct{ code string }

func (e repairCodedCompactionError) Error() string                { return "coded compaction failure" }
func (e repairCodedCompactionError) CompactionRepairCode() string { return e.code }

func TestConversationMemoryFeedsCompactorRepairCodeIntoRetry(t *testing.T) {
	repository := &memoryRepositoryStub{}
	var repairCodes []string
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		repairCodes = append(repairCodes, input.RepairCode)
		if input.Attempt == 1 {
			return conversationmemory.CompactionOutput{}, repairCodedCompactionError{code: "output_truncated"}
		}
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 2)
	conversationID := uuid.New()
	if _, err := service.GenerateShadow(context.Background(), conversationmemory.ShadowRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
	}); err != nil {
		t.Fatal(err)
	}
	if len(repairCodes) != 2 || repairCodes[0] != "" || repairCodes[1] != "output_truncated" {
		t.Fatalf("repair codes = %#v", repairCodes)
	}
}

func TestConversationMemoryRejectsInvalidCompletedMessagesBeforeActiveFastPath(t *testing.T) {
	conversationID := uuid.New()
	active := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	repository := &activationMemoryRepositoryStub{
		memoryRepositoryStub: &memoryRepositoryStub{latest: &active},
		active:               &active,
	}
	compactorCalls := 0
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		compactorCalls++
		return conversationmemory.CompactionOutput{}, nil
	}), time.Now().UTC(), 1)
	invalid := initialMessages(conversationID)
	invalid[0].ConversationID = uuid.New()

	_, err := service.PrepareActive(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: invalid,
		ActivationGate: acceptConversationMemoryActivation,
	})
	if !errors.Is(err, conversationmemory.ErrInvalidShadowInput) || compactorCalls != 0 {
		t.Fatalf("PrepareActive() error/compactor calls = %v/%d, want invalid input/0", err, compactorCalls)
	}
}

func TestConversationMemoryDoesNotActivateCandidateRejectedByMainPromptBudget(t *testing.T) {
	conversationID := uuid.New()
	repository := &activationMemoryRepositoryStub{memoryRepositoryStub: &memoryRepositoryStub{}}
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, _ conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		return conversationmemory.CompactionOutput{
			Payload: validPayload(),
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 1)
	rejected := errors.New("summary exceeds main prompt budget")

	_, err := service.PrepareActive(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
		ActivationGate: activationGateFunc(func(_ context.Context, snapshot conversationmemory.Snapshot) error {
			if snapshot.Status != conversationmemory.SnapshotStatusCandidate {
				t.Fatalf("activation gate Snapshot status = %s, want candidate", snapshot.Status)
			}
			return rejected
		}),
	})
	if !errors.Is(err, rejected) || repository.active != nil || len(repository.saved) != 1 ||
		repository.saved[0].Status != conversationmemory.SnapshotStatusCandidate {
		t.Fatalf("PrepareActive() error/active/saved = %v/%+v/%+v", err, repository.active, repository.saved)
	}
}

func TestConversationMemoryPreparesCandidateWithoutActivatingIt(t *testing.T) {
	conversationID := uuid.New()
	active := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	repository := &activationMemoryRepositoryStub{
		memoryRepositoryStub: &memoryRepositoryStub{latest: &active},
		active:               &active,
	}
	service := newMemoryService(t, repository, compactorFunc(func(_ context.Context, input conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		if input.PreviousSnapshot == nil || input.PreviousSnapshot.ID != active.ID ||
			len(input.NewMessages) != 2 || input.NewMessages[0].Seq != 4 {
			t.Fatalf("candidate compaction input = %+v", input)
		}
		payload := validPayload()
		payload.Decisions = append(payload.Decisions, conversationmemory.Entry{
			EntryID: "decision_async_memory", Content: "达到软阈值后异步压缩",
			SourceMessageSeqs: []int64{4, 5}, Status: conversationmemory.EntryStatusActive,
		})
		return conversationmemory.CompactionOutput{
			Payload: payload,
			Usage:   conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		}, nil
	}), time.Now().UTC(), 1)
	messages := append(initialMessages(conversationID),
		conversation.Message{ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleUser, Content: "软阈值异步压缩"},
		conversation.Message{ID: uuid.New(), ConversationID: conversationID, Seq: 5, Role: conversation.MessageRoleAssistant, Content: "已记录"},
	)

	prepared, err := service.PrepareActivationCandidate(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: messages,
		ActivationGate: acceptConversationMemoryActivation,
	})
	if err != nil {
		t.Fatalf("PrepareActivationCandidate() error = %v", err)
	}
	if prepared.CandidateSnapshot == nil || prepared.CurrentSnapshot != nil ||
		prepared.ExpectedActiveSnapshotID == nil || *prepared.ExpectedActiveSnapshotID != active.ID ||
		prepared.CandidateSnapshot.Status != conversationmemory.SnapshotStatusCandidate ||
		prepared.CandidateSnapshot.ThroughSeq != 5 || repository.active == nil || repository.active.ID != active.ID {
		t.Fatalf("prepared/active = %+v / %+v", prepared, repository.active)
	}
}

func TestConversationMemoryCandidatePreparationReturnsCoveringActiveSnapshot(t *testing.T) {
	conversationID := uuid.New()
	active := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	repository := &activationMemoryRepositoryStub{
		memoryRepositoryStub: &memoryRepositoryStub{latest: &active},
		active:               &active,
	}
	compactorCalls := 0
	service := newMemoryService(t, repository, compactorFunc(func(context.Context, conversationmemory.CompactionInput) (conversationmemory.CompactionOutput, error) {
		compactorCalls++
		return conversationmemory.CompactionOutput{}, nil
	}), time.Now().UTC(), 1)

	prepared, err := service.PrepareActivationCandidate(context.Background(), conversationmemory.PrepareActiveRequest{
		ConversationID: conversationID, CompletedMessages: initialMessages(conversationID),
		ActivationGate: acceptConversationMemoryActivation,
	})
	if err != nil {
		t.Fatalf("PrepareActivationCandidate() error = %v", err)
	}
	if prepared.CurrentSnapshot == nil || prepared.CurrentSnapshot.ID != active.ID ||
		prepared.CandidateSnapshot != nil || prepared.ExpectedActiveSnapshotID != nil || compactorCalls != 0 {
		t.Fatalf("prepared/compactor calls = %+v / %d", prepared, compactorCalls)
	}
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
		snapshot.Provenance.ModelProfile != "conversation-memory" || snapshot.Provenance.ModelProvider != "dashscope" ||
		snapshot.Provenance.ModelID != "qwen3.6-flash" || snapshot.Provenance.PromptVersion != "conversation-memory-v1" ||
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
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload:   validPayload(),
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
			{
				ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleUser, Content: "Tail 就按 15%",
				Citations: []conversation.MessageCitation{{
					Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
					SourceRef:     "knowledge:8c4c15e7-1d72-453d-b1a0-c64f70a03dc8/2cd3198e-0bff-4ab2-bc4c-e6838043039f",
					ContentSHA256: strings.Repeat("a", 64),
				}},
			},
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
	repository conversationmemory.ActivationRepository,
	compactor conversationmemory.Compactor,
	now time.Time,
	maxAttempts int,
) *conversationmemory.Service {
	t.Helper()
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository, Compactor: compactor,
		SchemaVersion: conversationmemory.CurrentSchemaVersion, MaxPayloadBytes: 64 * 1024,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		MaxAttempts: maxAttempts,
		Clock:       func() time.Time { return now },
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
