//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConversationTurnRepositoryAgainstPostgres(t *testing.T) {
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
VALUES (?, ?, 'Conversation Owner', 'integration-hash', 'analyst', 'active', false)`,
		userID, "conversation_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	repository := NewConversationRepository(tx)
	current, err := repository.Create(ctx, userID, conversation.CreateInput{Title: "幂等回合"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	startedAt := time.Now().UTC()
	key := uuid.NewString()
	beginInput := conversation.BeginTurnInput{
		Message: conversation.AppendMessageInput{
			ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "知识库如何更新？",
		},
		IdempotencyKey: key, RequestFingerprint: strings.Repeat("a", 64),
		StartedAt: startedAt, LeaseExpiresAt: startedAt.Add(time.Minute),
	}
	first, err := repository.BeginTurn(ctx, userID, beginInput)
	if err != nil {
		t.Fatalf("BeginTurn(): %v", err)
	}
	if !first.Created || first.TurnID == uuid.Nil || first.UserMessage.Seq != 1 {
		t.Fatalf("first turn = %+v", first)
	}
	if _, err := repository.BeginTurn(ctx, userID, beginInput); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("concurrent BeginTurn() error = %v, want ErrTurnInProgress", err)
	}
	if err := repository.FailTurn(ctx, userID, first.TurnID, startedAt.Add(time.Second)); err != nil {
		t.Fatalf("FailTurn(): %v", err)
	}
	retriedInput := beginInput
	retriedInput.StartedAt = startedAt.Add(2 * time.Second)
	retriedInput.LeaseExpiresAt = startedAt.Add(time.Minute + 2*time.Second)
	retried, err := repository.BeginTurn(ctx, userID, retriedInput)
	if err != nil {
		t.Fatalf("retry BeginTurn(): %v", err)
	}
	if retried.Created || retried.TurnID != first.TurnID || retried.UserMessage.ID != first.UserMessage.ID {
		t.Fatalf("retried turn = %+v, first = %+v", retried, first)
	}
	completed, err := repository.CompleteTurn(
		ctx, userID, first.TurnID, "采用不可变版本重新发布。", startedAt.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("CompleteTurn(): %v", err)
	}
	if completed.AssistantMessage.Seq != 2 || completed.AssistantMessage.Role != conversation.MessageRoleAssistant {
		t.Fatalf("completed turn = %+v", completed)
	}
	replayed, err := repository.BeginTurn(ctx, userID, retriedInput)
	if err != nil {
		t.Fatalf("replay BeginTurn(): %v", err)
	}
	if replayed.AssistantMessage == nil || replayed.AssistantMessage.ID != completed.AssistantMessage.ID {
		t.Fatalf("replayed turn = %+v", replayed)
	}
	conflictInput := retriedInput
	conflictInput.RequestFingerprint = strings.Repeat("b", 64)
	if _, err := repository.BeginTurn(ctx, userID, conflictInput); !errors.Is(err, conversation.ErrTurnIdempotencyConflict) {
		t.Fatalf("conflicting BeginTurn() error = %v, want ErrTurnIdempotencyConflict", err)
	}

	secondInput := beginInput
	secondInput.IdempotencyKey = uuid.NewString()
	secondInput.RequestFingerprint = strings.Repeat("c", 64)
	secondInput.StartedAt = startedAt.Add(4 * time.Second)
	secondInput.LeaseExpiresAt = startedAt.Add(5 * time.Second)
	second, err := repository.BeginTurn(ctx, userID, secondInput)
	if err != nil {
		t.Fatalf("second BeginTurn(): %v", err)
	}
	expiredRetry := secondInput
	expiredRetry.StartedAt = startedAt.Add(6 * time.Second)
	expiredRetry.LeaseExpiresAt = startedAt.Add(time.Minute + 6*time.Second)
	reclaimed, err := repository.BeginTurn(ctx, userID, expiredRetry)
	if err != nil {
		t.Fatalf("reclaim expired BeginTurn(): %v", err)
	}
	if reclaimed.Created || reclaimed.TurnID != second.TurnID || reclaimed.UserMessage.ID != second.UserMessage.ID {
		t.Fatalf("reclaimed turn = %+v, second = %+v", reclaimed, second)
	}
	if _, err := repository.AppendMessage(ctx, userID, conversation.AppendMessageInput{
		ConversationID: current.ID, Role: conversation.MessageRoleUser, Content: "并发追加",
	}, startedAt.Add(7*time.Second)); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Fatalf("AppendMessage during active turn error = %v, want ErrTurnInProgress", err)
	}
	if err := repository.FailTurn(ctx, userID, second.TurnID, startedAt.Add(8*time.Second)); err != nil {
		t.Fatalf("fail second turn: %v", err)
	}

	var turnCount, messageCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = ?", current.ID).Scan(&turnCount).Error; err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?", current.ID).Scan(&messageCount).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if turnCount != 2 || messageCount != 3 {
		t.Fatalf("turn/message counts = %d/%d, want 2/3", turnCount, messageCount)
	}
}
