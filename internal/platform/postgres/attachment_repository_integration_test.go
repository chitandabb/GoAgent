//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/attachment"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	repositorydomain "github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAttachmentRepositoryEnforcesConversationAndMessageOwnership(t *testing.T) {
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

	ownerID, otherID := uuid.New(), uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Attachment Owner', 'integration-hash', 'analyst', 'active', false),
       (?, ?, 'Attachment Other', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "attachment_owner_"+uuid.NewString()[:8],
		otherID, "attachment_other_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}
	conversationRepository := NewConversationRepository(tx)
	ownerConversation, err := conversationRepository.Create(ctx, ownerID, conversation.CreateInput{Title: "附件会话"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	otherConversation, err := conversationRepository.Create(ctx, otherID, conversation.CreateInput{Title: "其他会话"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	attachmentRepository := NewAttachmentRepository(tx)
	item := integrationAttachment(ownerID, ownerConversation.ID, uuid.New())
	if err := attachmentRepository.Create(ctx, item); err != nil {
		t.Fatalf("Create(): %v", err)
	}
	found, err := attachmentRepository.FindByIdempotency(ctx, ownerID, item.IdempotencyKey)
	if err != nil || found.ID != item.ID || found.Ref.ObjectKey != item.Ref.ObjectKey {
		t.Fatalf("FindByIdempotency()=%+v err=%v", found, err)
	}

	message, err := conversationRepository.AppendMessage(ctx, ownerID, conversation.AppendMessageInput{
		ConversationID: ownerConversation.ID, Role: conversation.MessageRoleUser, Content: "请分析附件",
		Attachments: []conversation.MessageAttachmentInput{{AttachmentID: item.ID, Purpose: "log_file"}},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("AppendMessage(): %v", err)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].AttachmentID != item.ID ||
		message.Attachments[0].OriginalName != item.Ref.OriginalName || message.Attachments[0].Purpose != "log_file" {
		t.Fatalf("message attachments=%+v", message.Attachments)
	}
	if _, err := attachmentRepository.GetMessageReadable(
		ctx, ownerID, ownerConversation.ID, message.ID, item.ID,
	); err != nil {
		t.Fatalf("GetMessageReadable(): %v", err)
	}
	if _, err := attachmentRepository.GetMessageReadable(
		ctx, otherID, ownerConversation.ID, message.ID, item.ID,
	); !errors.Is(err, repositorydomain.ErrNotFound) {
		t.Fatalf("cross-user GetMessageReadable() error=%v", err)
	}

	unlinked := integrationAttachment(ownerID, ownerConversation.ID, uuid.New())
	if err := attachmentRepository.Create(ctx, unlinked); err != nil {
		t.Fatal(err)
	}
	if _, err := attachmentRepository.GetReadable(ctx, ownerID, ownerConversation.ID, unlinked.ID); err != nil {
		t.Fatalf("conversation preview should read staged attachment: %v", err)
	}
	if _, err := attachmentRepository.GetMessageReadable(
		ctx, ownerID, ownerConversation.ID, message.ID, unlinked.ID,
	); !errors.Is(err, repositorydomain.ErrNotFound) {
		t.Fatalf("unlinked GetMessageReadable() error=%v", err)
	}

	forbidden := integrationAttachment(ownerID, otherConversation.ID, uuid.New())
	if err := attachmentRepository.Create(ctx, forbidden); !errors.Is(err, repositorydomain.ErrNotFound) {
		t.Fatalf("Create(other user's conversation) error=%v", err)
	}
	if _, err := conversationRepository.AppendMessage(ctx, otherID, conversation.AppendMessageInput{
		ConversationID: otherConversation.ID, Role: conversation.MessageRoleUser, Content: "越权附件",
		Attachments: []conversation.MessageAttachmentInput{{AttachmentID: item.ID, Purpose: "context"}},
	}, time.Now().UTC()); !errors.Is(err, repositorydomain.ErrNotFound) {
		t.Fatalf("cross-user AppendMessage() error=%v", err)
	}
}

func integrationAttachment(ownerID, conversationID, idempotencyKey uuid.UUID) attachment.Attachment {
	now := time.Now().UTC()
	return attachment.Attachment{
		ID: uuid.New(), OwnerUserID: ownerID, Scope: attachment.ScopeSession,
		ConversationID: &conversationID, Status: attachment.StatusUploaded,
		IdempotencyKey: idempotencyKey, RequestFingerprint: strings.Repeat("a", 64),
		Ref: objectstore.ObjectRef{
			Bucket: objectstore.BucketAttachments, ObjectKey: "attachments/integration/" + uuid.NewString(),
			ETag: "integration-etag", SizeBytes: 12,
			SHA256:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MediaType: "text/plain", OriginalName: "error.log",
		},
		UploadedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}
