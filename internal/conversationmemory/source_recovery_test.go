package conversationmemory_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
)

type sourceActiveReaderStub struct {
	snapshot *conversationmemory.Snapshot
	err      error
	calls    int
}

func (s *sourceActiveReaderStub) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	s.calls++
	if s.snapshot == nil {
		return nil, s.err
	}
	copy := *s.snapshot
	return &copy, s.err
}

type sourceMessageReaderStub struct {
	messages       []conversation.Message
	err            error
	userID         uuid.UUID
	conversationID uuid.UUID
	sequences      []int64
	calls          int
}

func (s *sourceMessageReaderStub) ReadSourceMessages(
	_ context.Context,
	userID, conversationID uuid.UUID,
	sequences []int64,
) ([]conversation.Message, error) {
	s.calls++
	s.userID = userID
	s.conversationID = conversationID
	s.sequences = append([]int64(nil), sequences...)
	bySequence := make(map[int64]conversation.Message, len(s.messages))
	for _, message := range s.messages {
		bySequence[message.Seq] = message
	}
	result := make([]conversation.Message, 0, len(sequences))
	for _, sequence := range sequences {
		if message, ok := bySequence[sequence]; ok {
			result = append(result, message)
		}
	}
	return result, s.err
}

type sourceTokenCounterFunc func(context.Context, string) (int, error)

func (f sourceTokenCounterFunc) Count(ctx context.Context, content string) (int, error) {
	return f(ctx, content)
}

func TestSourceRecoveryReadsOnlyTheActiveEntrySourcesForTheCurrentActor(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	messages := initialMessages(conversationID)
	reader := &sourceMessageReaderStub{messages: []conversation.Message{messages[2]}}
	recovery := newSourceRecovery(t, &sourceActiveReaderStub{snapshot: &snapshot}, reader, 20, 8192)

	result, err := recovery.Read(
		conversationmemory.WithSourceRecoveryRun(context.Background()),
		conversationmemory.SourceReadRequest{
			Actor: conversation.Actor{UserID: userID}, ConversationID: conversationID,
			EntryID: "correction_timezone",
		},
	)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].MessageRef != "conversation_message:"+messages[2].ID.String() ||
		result.Messages[0].Seq != 3 || result.Messages[0].Role != conversation.MessageRoleUser ||
		result.Messages[0].Content != messages[2].Content || !result.Messages[0].ContentComplete ||
		result.Messages[0].ContentOffsetRunes != 0 || result.HasMore || result.ContinuationCursor != "" {
		t.Fatalf("Read() result = %+v", result)
	}
	if reader.calls != 1 || reader.userID != userID || reader.conversationID != conversationID ||
		!slices.Equal(reader.sequences, []int64{3}) {
		t.Fatalf("reader calls/user/conversation/sequences = %d/%s/%s/%v",
			reader.calls, reader.userID, reader.conversationID, reader.sequences)
	}
}

func TestSourceRecoveryAllowsOnlySequencesDeclaredByCurrentSnapshotEntries(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := activeSnapshotFixture(t, conversationID, 1, 3, nil)
	messages := initialMessages(conversationID)
	reader := &sourceMessageReaderStub{messages: []conversation.Message{messages[0], messages[2]}}
	recovery := newSourceRecovery(t, &sourceActiveReaderStub{snapshot: &snapshot}, reader, 20, 8192)
	ctx := conversationmemory.WithSourceRecoveryRun(context.Background())

	result, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID, SourceMessageSeqs: []int64{1, 3},
	})
	if err != nil || len(result.Messages) != 2 || result.Messages[0].Seq != 1 || result.Messages[1].Seq != 3 {
		t.Fatalf("Read() direct sources = %+v, %v", result, err)
	}

	for name, request := range map[string]conversationmemory.SourceReadRequest{
		"unknown entry": {
			Actor: userActor(userID), ConversationID: conversationID, EntryID: "missing_entry",
		},
		"superseded entry": {
			Actor: userActor(userID), ConversationID: conversationID, EntryID: "fact_timezone",
		},
		"undeclared sequence": {
			Actor: userActor(userID), ConversationID: conversationID, SourceMessageSeqs: []int64{99},
		},
		"duplicate sequence": {
			Actor: userActor(userID), ConversationID: conversationID, SourceMessageSeqs: []int64{3, 3},
		},
		"unsorted sequence": {
			Actor: userActor(userID), ConversationID: conversationID, SourceMessageSeqs: []int64{3, 1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			callsBefore := reader.calls
			if _, err := recovery.Read(ctx, request); !errors.Is(err, conversationmemory.ErrSourceNotAuthorized) {
				t.Fatalf("Read() error = %v, want ErrSourceNotAuthorized", err)
			}
			if reader.calls != callsBefore {
				t.Fatalf("unauthorized read reached Message Reader: %d -> %d", callsBefore, reader.calls)
			}
		})
	}
}

func TestSourceRecoveryContinuesAcrossTheTwentyMessageLimitWithRunBoundCursor(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	sequences := make([]int64, 21)
	messages := make([]conversation.Message, 21)
	for index := range sequences {
		sequence := int64(index + 1)
		sequences[index] = sequence
		messages[index] = conversation.Message{
			ID: uuid.New(), ConversationID: conversationID, Seq: sequence,
			Role: conversation.MessageRoleUser, Content: "source message",
		}
	}
	snapshot := sourceSnapshotFixture(t, conversationID, sequences)
	active := &sourceActiveReaderStub{snapshot: &snapshot}
	recovery := newSourceRecovery(t, active, &sourceMessageReaderStub{messages: messages}, 20, 8192)
	runCtx := conversationmemory.WithSourceRecoveryRun(context.Background())

	first, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID, EntryID: "goal_sources",
	})
	if err != nil || len(first.Messages) != 20 || !first.HasMore || first.ContinuationCursor == "" ||
		first.Messages[19].Seq != 20 {
		t.Fatalf("first Read() = %+v, %v", first, err)
	}
	if _, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(uuid.New()), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("cross-user cursor error = %v", err)
	}
	if _, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: uuid.New(),
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("cross-conversation cursor error = %v", err)
	}
	second, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	})
	if err != nil || len(second.Messages) != 1 || second.Messages[0].Seq != 21 ||
		second.HasMore || second.ContinuationCursor != "" {
		t.Fatalf("second Read() = %+v, %v", second, err)
	}
	if _, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("replayed cursor error = %v", err)
	}
	otherRun := conversationmemory.WithSourceRecoveryRun(context.Background())
	if _, err := recovery.Read(otherRun, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("cross-run cursor error = %v", err)
	}
}

func TestSourceRecoveryContinuesInsideOneOversizedMessage(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	message := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: strings.Repeat("中", 600),
	}
	recovery := newSourceRecovery(
		t, &sourceActiveReaderStub{snapshot: &snapshot},
		&sourceMessageReaderStub{messages: []conversation.Message{message}}, 20, 512,
	)
	runCtx := conversationmemory.WithSourceRecoveryRun(context.Background())

	first, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID, EntryID: "goal_sources",
	})
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Content == "" ||
		first.Messages[0].ContentComplete || first.Messages[0].ContentOffsetRunes != 0 ||
		!first.HasMore || first.ContinuationCursor == "" {
		t.Fatalf("first oversized Read() = %+v, %v", first, err)
	}
	firstRunes := len([]rune(first.Messages[0].Content))
	second, err := recovery.Read(runCtx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	})
	if err != nil || len(second.Messages) != 1 || second.Messages[0].ContentOffsetRunes != firstRunes ||
		second.Messages[0].Content == "" {
		t.Fatalf("second oversized Read() = %+v, %v, firstRunes=%d", second, err, firstRunes)
	}
}

func TestSourceRecoveryFindsRelevantWindowInsideOneOversizedMessage(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	marker := "SQLSTATE HYT00 数据库连接池超时"
	message := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role:    conversation.MessageRoleUser,
		Content: strings.Repeat("前", 12_000) + marker + strings.Repeat("后", 7_000),
	}
	recovery := newSourceRecovery(
		t, &sourceActiveReaderStub{snapshot: &snapshot},
		&sourceMessageReaderStub{messages: []conversation.Message{message}}, 20, 512,
	)

	result, err := recovery.Read(
		conversationmemory.WithSourceRecoveryRun(context.Background()),
		conversationmemory.SourceReadRequest{
			Actor: userActor(userID), ConversationID: conversationID,
			EntryID: "goal_sources", Query: "SQLSTATE HYT00 连接池超时 错误码",
		},
	)
	if err != nil {
		t.Fatalf("Read(relevant window): %v", err)
	}
	if result.Mode != conversationmemory.SourceReadModeRelevant || len(result.Messages) != 1 ||
		!strings.Contains(result.Messages[0].Content, marker) ||
		result.Messages[0].ContentOffsetRunes == 0 || result.Messages[0].MatchScore < 1 {
		t.Fatalf("relevant result = %+v", result)
	}
}

func TestSourceRecoveryReturnsEmptyRelevantResultWhenQueryDoesNotMatch(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	reader := &sourceMessageReaderStub{messages: []conversation.Message{{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "数据库连接正常",
	}}}
	recovery := newSourceRecovery(t, &sourceActiveReaderStub{snapshot: &snapshot}, reader, 20, 512)

	result, err := recovery.Read(
		conversationmemory.WithSourceRecoveryRun(context.Background()),
		conversationmemory.SourceReadRequest{
			Actor: userActor(userID), ConversationID: conversationID,
			EntryID: "goal_sources", Query: "不存在的错误标记",
		},
	)
	if err != nil {
		t.Fatalf("Read(no relevant match): %v", err)
	}
	if result.Mode != conversationmemory.SourceReadModeRelevant || len(result.Messages) != 0 ||
		result.HasMore || result.ContinuationAvailable || result.ContinuationCursor != "" {
		t.Fatalf("no-match result = %+v", result)
	}
	if reader.calls != 1 {
		t.Fatalf("message reader calls = %d, want 1", reader.calls)
	}
}

func TestSourceRecoverySupportsSingleRuneRelevantQuery(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	message := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "设备 E 停机，请检查错误码",
	}
	recovery := newSourceRecovery(
		t, &sourceActiveReaderStub{snapshot: &snapshot},
		&sourceMessageReaderStub{messages: []conversation.Message{message}}, 20, 512,
	)

	result, err := recovery.Read(
		conversationmemory.WithSourceRecoveryRun(context.Background()),
		conversationmemory.SourceReadRequest{
			Actor: userActor(userID), ConversationID: conversationID,
			EntryID: "goal_sources", Query: "E",
		},
	)
	if err != nil {
		t.Fatalf("Read(single-rune query): %v", err)
	}
	if result.Mode != conversationmemory.SourceReadModeRelevant || len(result.Messages) != 1 ||
		!strings.Contains(result.Messages[0].Content, "E") || result.Messages[0].MatchScore < 1 {
		t.Fatalf("single-rune result = %+v", result)
	}
}

func TestSourceRecoveryRelevantCursorAdvancesAndRemainsRunScoped(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	message := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role:    conversation.MessageRoleUser,
		Content: strings.Repeat("前", 400) + "TARGET needle" + strings.Repeat("后", 1_200),
	}
	recovery := newSourceRecovery(
		t, &sourceActiveReaderStub{snapshot: &snapshot},
		&sourceMessageReaderStub{messages: []conversation.Message{message}}, 20, 512,
	)
	ctx := conversationmemory.WithSourceRecoveryRun(context.Background())

	first, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		EntryID: "goal_sources", Query: "TARGET needle",
	})
	if err != nil || first.Mode != conversationmemory.SourceReadModeRelevant ||
		len(first.Messages) != 1 || !first.HasMore || !first.ContinuationAvailable ||
		first.ContinuationCursor == "" || first.Messages[0].WindowComplete {
		t.Fatalf("first relevant Read() = %+v, %v", first, err)
	}
	for name, request := range map[string]conversationmemory.SourceReadRequest{
		"cross user": {
			Actor: userActor(uuid.New()), ConversationID: conversationID,
			ContinuationCursor: first.ContinuationCursor,
		},
		"cross conversation": {
			Actor: userActor(userID), ConversationID: uuid.New(),
			ContinuationCursor: first.ContinuationCursor,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := recovery.Read(ctx, request); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
				t.Fatalf("Read() error = %v, want ErrSourceCursorInvalid", err)
			}
		})
	}
	second, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	})
	if err != nil || second.Mode != conversationmemory.SourceReadModeRelevant ||
		len(second.Messages) != 1 || second.Messages[0].ContentOffsetRunes != first.Messages[0].ContentEndRunes {
		t.Fatalf("second relevant Read() = %+v, %v", second, err)
	}
	if _, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("replayed relevant cursor error = %v", err)
	}
	otherRun := conversationmemory.WithSourceRecoveryRun(context.Background())
	if _, err := recovery.Read(otherRun, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: second.ContinuationCursor,
	}); second.ContinuationCursor != "" && !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("cross-run relevant cursor error = %v", err)
	}
}

func TestSourceRecoveryReadsOneAuthorizedMessageFromExplicitOffset(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	snapshot := sourceSnapshotFixture(t, conversationID, []int64{1})
	marker := "从这里恢复后半段"
	message := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role:    conversation.MessageRoleUser,
		Content: strings.Repeat("前", 12_000) + marker + strings.Repeat("后", 7_000),
	}
	reader := &sourceMessageReaderStub{messages: []conversation.Message{message}}
	recovery := newSourceRecovery(t, &sourceActiveReaderStub{snapshot: &snapshot}, reader, 20, 512)
	ctx := conversationmemory.WithSourceRecoveryRun(context.Background())

	result, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		SourceMessageSeqs: []int64{1}, ContentOffsetRunes: sourceOffset(12_000),
	})
	if err != nil {
		t.Fatalf("Read(explicit offset): %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].ContentOffsetRunes != 12_000 ||
		!strings.HasPrefix(result.Messages[0].Content, marker) {
		t.Fatalf("offset result = %+v", result)
	}
	callsBefore := reader.calls
	for name, request := range map[string]conversationmemory.SourceReadRequest{
		"entry offset": {
			Actor: userActor(userID), ConversationID: conversationID,
			EntryID: "goal_sources", ContentOffsetRunes: sourceOffset(1),
		},
		"offset with query": {
			Actor: userActor(userID), ConversationID: conversationID,
			SourceMessageSeqs: []int64{1}, Query: "恢复", ContentOffsetRunes: sourceOffset(1),
		},
		"offset with multiple sequences": {
			Actor: userActor(userID), ConversationID: conversationID,
			SourceMessageSeqs: []int64{1, 2}, ContentOffsetRunes: sourceOffset(1),
		},
		"zero offset with entry": {
			Actor: userActor(userID), ConversationID: conversationID,
			EntryID: "goal_sources", ContentOffsetRunes: sourceOffset(0),
		},
		"zero offset with query": {
			Actor: userActor(userID), ConversationID: conversationID,
			SourceMessageSeqs: []int64{1}, Query: "恢复", ContentOffsetRunes: sourceOffset(0),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := recovery.Read(ctx, request); !errors.Is(err, conversationmemory.ErrInvalidSourceRead) {
				t.Fatalf("Read() error = %v, want ErrInvalidSourceRead", err)
			}
		})
	}
	if reader.calls != callsBefore {
		t.Fatal("invalid offset request reached Message Reader")
	}
}

func TestSourceRecoveryRejectsCursorAfterActiveSnapshotChanges(t *testing.T) {
	conversationID := uuid.New()
	userID := uuid.New()
	sequences := make([]int64, 21)
	messages := make([]conversation.Message, 21)
	for index := range sequences {
		sequences[index] = int64(index + 1)
		messages[index] = conversation.Message{
			ID: uuid.New(), ConversationID: conversationID, Seq: sequences[index],
			Role: conversation.MessageRoleUser, Content: "source",
		}
	}
	snapshot := sourceSnapshotFixture(t, conversationID, sequences)
	active := &sourceActiveReaderStub{snapshot: &snapshot}
	recovery := newSourceRecovery(t, active, &sourceMessageReaderStub{messages: messages}, 20, 8192)
	ctx := conversationmemory.WithSourceRecoveryRun(context.Background())
	first, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		EntryID: "goal_sources", Query: "source",
	})
	if err != nil || first.Mode != conversationmemory.SourceReadModeRelevant || first.ContinuationCursor == "" {
		t.Fatalf("first Read() = %+v, %v", first, err)
	}
	replacement := sourceSnapshotFixture(t, conversationID, sequences)
	active.snapshot = &replacement
	if _, err := recovery.Read(ctx, conversationmemory.SourceReadRequest{
		Actor: userActor(userID), ConversationID: conversationID,
		ContinuationCursor: first.ContinuationCursor,
	}); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("changed Active cursor error = %v", err)
	}
}

func userActor(userID uuid.UUID) conversation.Actor {
	return conversation.Actor{UserID: userID}
}

func sourceOffset(value int) *int {
	return &value
}

func sourceSnapshotFixture(
	t *testing.T,
	conversationID uuid.UUID,
	sequences []int64,
) conversationmemory.Snapshot {
	t.Helper()
	createdAt := time.Now().Add(-time.Minute).UTC()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID,
		FromSeq: sequences[0], ThroughSeq: sequences[len(sequences)-1],
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload: conversationmemory.Payload{
			ConversationGoal: &conversationmemory.Entry{
				EntryID: "goal_sources", Content: "恢复原文", SourceMessageSeqs: append([]int64(nil), sequences...),
				Status: conversationmemory.EntryStatusActive,
			},
			Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
			Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
			OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
			TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
		},
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot() error = %v", err)
	}
	activatedAt := createdAt.Add(time.Second)
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = &activatedAt
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("source snapshot Validate() error = %v", err)
	}
	return snapshot
}

func newSourceRecovery(
	t *testing.T,
	active conversationmemory.ActiveSnapshotReader,
	messages conversationmemory.SourceMessageReader,
	maxMessages, maxTokens int,
) *conversationmemory.SourceRecovery {
	t.Helper()
	recovery, err := conversationmemory.NewSourceRecovery(conversationmemory.SourceRecoveryConfig{
		ActiveSnapshots: active, Messages: messages,
		TokenCounter: sourceTokenCounterFunc(func(_ context.Context, content string) (int, error) {
			return len([]rune(content)), nil
		}),
		MaxMessages: maxMessages, MaxTokens: maxTokens,
	})
	if err != nil {
		t.Fatalf("NewSourceRecovery() error = %v", err)
	}
	return recovery
}
