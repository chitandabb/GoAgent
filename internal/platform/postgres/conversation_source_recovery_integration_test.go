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

func TestConversationRepositoryReadSourceMessagesAgainstPostgres(t *testing.T) {
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

	ownerID := uuid.New()
	otherUserID := uuid.New()
	for index, userID := range []uuid.UUID{ownerID, otherUserID} {
		if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Source Recovery Owner', 'integration-hash', 'analyst', 'active', false)`,
			userID, "source_recovery_"+uuid.NewString()[:8]).Error; err != nil {
			t.Fatalf("insert user %d: %v", index, err)
		}
	}
	repository := NewConversationRepository(tx)
	current, err := repository.Create(
		ctx, ownerID, conversation.CreateInput{Title: "原文恢复"}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	createdAt := time.Now().UTC()
	if err := tx.Exec(`
INSERT INTO conversation_messages (id, conversation_id, seq, role, content, content_schema_version, created_at)
VALUES (?, ?, 1, 'user', '第一条原始消息', 1, ?),
       (?, ?, 2, 'assistant', '第二条原始消息', 1, ?),
       (?, ?, 3, 'user', '第三条原始消息', 1, ?)`,
		uuid.New(), current.ID, createdAt,
		uuid.New(), current.ID, createdAt.Add(time.Millisecond),
		uuid.New(), current.ID, createdAt.Add(2*time.Millisecond),
	).Error; err != nil {
		t.Fatalf("insert source messages: %v", err)
	}

	messages, err := repository.ReadSourceMessages(ctx, ownerID, current.ID, []int64{1, 3})
	if err != nil {
		t.Fatalf("ReadSourceMessages(owner): %v", err)
	}
	if len(messages) != 2 || messages[0].Seq != 1 || messages[0].Content != "第一条原始消息" ||
		messages[1].Seq != 3 || messages[1].Content != "第三条原始消息" {
		t.Fatalf("ReadSourceMessages(owner) = %+v", messages)
	}
	if _, err := repository.ReadSourceMessages(ctx, otherUserID, current.ID, []int64{1}); !errors.Is(err, conversationmemory.ErrSourceMessagesInvalid) {
		t.Fatalf("ReadSourceMessages(other user) error = %v, want ErrSourceMessagesInvalid", err)
	}
	if _, err := repository.ReadSourceMessages(ctx, ownerID, current.ID, []int64{1, 4}); !errors.Is(err, conversationmemory.ErrSourceMessagesInvalid) {
		t.Fatalf("ReadSourceMessages(missing sequence) error = %v, want ErrSourceMessagesInvalid", err)
	}
}
