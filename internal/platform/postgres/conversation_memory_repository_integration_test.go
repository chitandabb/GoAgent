//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationMemoryRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	userID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Memory Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "memory_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	conversationRepository := NewConversationRepository(tx)
	current, err := conversationRepository.Create(ctx, userID, conversation.CreateInput{Title: "结构化记忆"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	repository := NewConversationMemoryRepository(tx)
	firstCandidate := integrationMemoryCandidate(t, current.ID, nil, 1, 3)
	first, err := repository.Save(ctx, firstCandidate)
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if first.Version != 1 || first.PayloadSHA256 != firstCandidate.PayloadSHA256 {
		t.Fatalf("first snapshot = %+v", first)
	}
	loaded, err := repository.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get(first): %v", err)
	}
	if loaded.ID != first.ID || loaded.Version != 1 || loaded.Payload.ConversationGoal == nil ||
		loaded.Payload.ConversationGoal.Content != "完成上下文治理" {
		t.Fatalf("loaded snapshot = %+v", loaded)
	}

	mutatedPayload := `{"conversationGoal":null,"facts":[],"decisions":[],"corrections":[],"evidenceReferences":[],"openQuestions":[],"todos":[],"taskReferences":[],"reportReferences":[]}`
	updateErr := tx.Transaction(func(savepoint *gorm.DB) error {
		return savepoint.Exec(`UPDATE conversation_memory_snapshots SET payload = ?::jsonb WHERE id = ?`, mutatedPayload, first.ID).Error
	})
	if updateErr == nil {
		t.Fatal("immutable snapshot payload update unexpectedly succeeded")
	}

	secondCandidate := integrationMemoryCandidate(t, current.ID, &first.ID, 1, 5)
	second, err := repository.Save(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	latest, err := repository.Latest(ctx, current.ID)
	if err != nil {
		t.Fatalf("Latest(): %v", err)
	}
	if second.Version != 2 || latest.ID != second.ID || latest.SupersedesSnapshotID == nil ||
		*latest.SupersedesSnapshotID != first.ID || latest.FromSeq != 1 || latest.ThroughSeq != 5 {
		t.Fatalf("second/latest snapshots = %+v / %+v", second, latest)
	}
	disconnectedCandidate := integrationMemoryCandidate(t, current.ID, nil, 1, 7)
	if _, err := repository.Save(ctx, disconnectedCandidate); !errors.Is(err, conversationmemory.ErrInvalidSnapshot) {
		t.Fatalf("Save(disconnected successor) error = %v, want ErrInvalidSnapshot", err)
	}

	otherConversation, err := conversationRepository.Create(ctx, userID, conversation.CreateInput{Title: "其他会话"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create other conversation: %v", err)
	}
	invalidPredecessor := integrationMemoryCandidate(t, otherConversation.ID, &first.ID, 1, 7)
	if _, err := repository.Save(ctx, invalidPredecessor); !errors.Is(err, conversationmemory.ErrInvalidSnapshot) {
		t.Fatalf("Save(cross-conversation predecessor) error = %v, want ErrInvalidSnapshot", err)
	}

	if err := tx.Exec(`DELETE FROM conversations WHERE id = ?`, current.ID).Error; err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	if _, err := repository.Get(ctx, second.ID); !errors.Is(err, conversationmemory.ErrSnapshotNotFound) {
		t.Fatalf("Get(after conversation delete) error = %v, want ErrSnapshotNotFound", err)
	}
}

func integrationMemoryCandidate(
	t *testing.T,
	conversationID uuid.UUID,
	predecessor *uuid.UUID,
	fromSeq, throughSeq int64,
) conversationmemory.CandidateSnapshot {
	t.Helper()
	payload := conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{
			EntryID: "goal_context", Content: "完成上下文治理", SourceMessageSeqs: []int64{1},
			Status: conversationmemory.EntryStatusActive,
		},
		Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
		Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
		OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
		TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
	}
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, SupersedesSnapshotID: predecessor,
		FromSeq: fromSeq, ThroughSeq: throughSeq, SchemaVersion: conversationmemory.CurrentSchemaVersion,
		SummaryModelProfile: "conversation-memory", SummaryModelProvider: "dashscope", SummaryModelID: "qwen3.6-flash",
		PromptVersion: "conversation-memory-v1", Payload: payload,
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedTokens: 10},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot(): %v", err)
	}
	return candidate
}
