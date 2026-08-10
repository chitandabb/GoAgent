//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestKnowledgeCitationRepositoryEnforcesPersonalScope(t *testing.T) {
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	ownerID, otherID := uuid.New(), uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Citation Owner', 'integration-hash', 'analyst', 'active', false),
       (?, ?, 'Citation Other', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "citation_owner_"+uuid.NewString()[:8],
		otherID, "citation_other_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatal(err)
	}
	documentID := uuid.New()
	knowledgeRepository := NewKnowledgeRepository(tx)
	if _, err := knowledgeRepository.CreateDocument(ctx, knowledge.CreateDocumentInput{
		ID: documentID, Scope: knowledge.ScopePersonal, OwnerUserID: &ownerID,
		Title: "个人排障笔记", CreatedBy: ownerID,
	}); err != nil {
		t.Fatal(err)
	}
	content := "ERP-504 出现时先检查连接池等待队列。"
	chunks, err := knowledge.ChunkMarkdown(content, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	version, err := knowledgeRepository.PublishVersion(ctx, publishKnowledgeVersionInput(
		uuid.New(), documentID, ownerID, content, chunks,
	))
	if err != nil {
		t.Fatal(err)
	}
	var chunkRecord struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	if err := tx.Raw(`SELECT id FROM knowledge_chunks WHERE document_version_id = ? ORDER BY ordinal LIMIT 1`, version.ID).Scan(&chunkRecord).Error; err != nil {
		t.Fatal(err)
	}
	chunkID := chunkRecord.ID
	citations := NewKnowledgeCitationRepository(tx)
	preview, err := citations.GetCitation(ctx, ownerID, chunkID)
	if err != nil || preview.ChunkID != chunkID || preview.ContentSHA256 != knowledge.SHA256Hex(preview.ContentText) {
		t.Fatalf("owner GetCitation()=%+v err=%v", preview, err)
	}
	if _, err := citations.GetCitation(ctx, otherID, chunkID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other GetCitation() error=%v", err)
	}
}
