//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestKnowledgeRepositoryPublishesVersionsAndFiltersSearchScope(t *testing.T) {
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
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ownerID := uuid.New()
	otherID := uuid.New()
	globalDocumentID := uuid.New()
	personalDocumentID := uuid.New()
	documentIDs := []uuid.UUID{globalDocumentID, personalDocumentID}
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		_ = cleanup.Exec("DELETE FROM knowledge_chunks WHERE document_version_id IN (SELECT id FROM knowledge_document_versions WHERE document_id IN ?)", documentIDs).Error
		_ = cleanup.Exec("DELETE FROM knowledge_document_versions WHERE document_id IN ?", documentIDs).Error
		_ = cleanup.Exec("DELETE FROM knowledge_documents WHERE id IN ?", documentIDs).Error
		_ = cleanup.Exec("DELETE FROM users WHERE id IN ?", []uuid.UUID{ownerID, otherID}).Error
	})

	mustExecKnowledgeTest(t, db, ctx, `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Knowledge Owner', 'test-hash', 'analyst'),
       (?, ?, 'Knowledge Other', 'test-hash', 'analyst')`,
		ownerID, "knowledge_owner_"+uuid.NewString()[:8],
		otherID, "knowledge_other_"+uuid.NewString()[:8])

	repository := NewKnowledgeRepository(db)
	_, err = repository.CreateDocument(ctx, knowledge.CreateDocumentInput{
		ID: globalDocumentID, Scope: knowledge.ScopeGlobal,
		Title: "MES 报工排障手册", CreatedBy: ownerID,
	})
	if err != nil {
		t.Fatalf("CreateDocument(global): %v", err)
	}
	_, err = repository.CreateDocument(ctx, knowledge.CreateDocumentInput{
		ID: personalDocumentID, Scope: knowledge.ScopePersonal, OwnerUserID: &ownerID,
		Title: "客户 A 私有记录", CreatedBy: ownerID,
	})
	if err != nil {
		t.Fatalf("CreateDocument(personal): %v", err)
	}

	oldChunks, err := knowledge.ChunkMarkdown("设备报工失败时先检查旧版缓存。", knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	oldVersionID := uuid.New()
	oldVersion, err := repository.PublishVersion(ctx, publishKnowledgeVersionInput(
		oldVersionID, globalDocumentID, ownerID, "设备报工失败时先检查旧版缓存。", oldChunks,
	))
	if err != nil || oldVersion.Version != 1 {
		t.Fatalf("PublishVersion(old) = %#v, %v", oldVersion, err)
	}
	newContent := "# 故障处理\n\n设备 E-100 报工失败时，应核对事务日志和 ERP 接口状态。\n\n处理前应记录连接池指标并保留现场时间窗口。"
	newChunks, err := knowledge.ChunkMarkdown(newContent, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	newVersionID := uuid.New()
	newVersion, err := repository.PublishVersion(ctx, publishKnowledgeVersionInput(
		newVersionID, globalDocumentID, ownerID, newContent, newChunks,
	))
	if err != nil || newVersion.Version != 2 {
		t.Fatalf("PublishVersion(new) = %#v, %v", newVersion, err)
	}
	privateContent := "客户 A 的序列号 SN-PRIVATE 报工失败，需要检查专属映射。"
	privateChunks, err := knowledge.ChunkMarkdown(privateContent, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.PublishVersion(ctx, publishKnowledgeVersionInput(
		uuid.New(), personalDocumentID, ownerID, privateContent, privateChunks,
	))
	if err != nil {
		t.Fatalf("PublishVersion(personal): %v", err)
	}

	ownerResults, err := repository.SearchFTS(ctx, ownerID, "报工失败", 10)
	if err != nil {
		t.Fatalf("SearchFTS(owner): %v", err)
	}
	if len(ownerResults) != 2 {
		t.Fatalf("SearchFTS(owner) returned %d results, want global current + personal: %#v", len(ownerResults), ownerResults)
	}
	for _, result := range ownerResults {
		if result.DocumentVersionID == oldVersionID || result.ContentText == "设备报工失败时先检查旧版缓存。" {
			t.Fatalf("SearchFTS(owner) returned retired version: %#v", result)
		}
	}
	var globalHit knowledge.SearchResult
	for _, result := range ownerResults {
		if result.DocumentID == globalDocumentID {
			globalHit = result
			break
		}
	}
	if globalHit.ChunkID == uuid.Nil {
		t.Fatalf("SearchFTS(owner) did not return the global hit: %#v", ownerResults)
	}
	contextGroups, err := repository.ExpandContext(ctx, ownerID, []knowledge.SearchResult{globalHit}, 1, 1800)
	if err != nil {
		t.Fatalf("ExpandContext(owner): %v", err)
	}
	if len(contextGroups) != 1 || len(contextGroups[0].Chunks) != 1 ||
		contextGroups[0].Chunks[0].ContentText != "处理前应记录连接池指标并保留现场时间窗口。" {
		t.Fatalf("ExpandContext(owner) = %#v", contextGroups)
	}
	otherResults, err := repository.SearchFTS(ctx, otherID, "报工失败", 10)
	if err != nil {
		t.Fatalf("SearchFTS(other): %v", err)
	}
	if len(otherResults) != 1 || otherResults[0].DocumentID != globalDocumentID {
		t.Fatalf("SearchFTS(other) = %#v, want only global document", otherResults)
	}
}

func TestKnowledgeRepositoryQueuesVersionTaskEventAndOutboxAtomically(t *testing.T) {
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
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	creatorID, documentID := uuid.New(), uuid.New()
	versionID, taskID, outboxID, correlationID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		_ = cleanup.Exec("DELETE FROM outbox_events WHERE id = ?", outboxID).Error
		_ = cleanup.Exec("DELETE FROM knowledge_ingestion_events WHERE task_id = ?", taskID).Error
		_ = cleanup.Exec("DELETE FROM knowledge_ingestion_tasks WHERE id = ?", taskID).Error
		_ = cleanup.Exec("DELETE FROM knowledge_document_versions WHERE id = ?", versionID).Error
		_ = cleanup.Exec("DELETE FROM knowledge_documents WHERE id = ?", documentID).Error
		_ = cleanup.Exec("DELETE FROM users WHERE id = ?", creatorID).Error
	})
	mustExecKnowledgeTest(t, db, ctx, `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Knowledge Queue Owner', 'test-hash', 'admin')`,
		creatorID, "knowledge_queue_"+uuid.NewString()[:8])
	repository := NewKnowledgeRepository(db)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	idempotencyKey := uuid.NewString()
	requestFingerprint := knowledge.SHA256Hex("queue-version-request")
	queued, err := repository.QueueVersion(ctx, knowledge.QueueVersionInput{
		VersionID: versionID, TaskID: taskID, OutboxEventID: outboxID,
		CorrelationID: correlationID, DocumentID: documentID, CreatedBy: creatorID,
		Source: objectstore.ObjectRef{
			Bucket:    objectstore.BucketKnowledgeSources,
			ObjectKey: "knowledge-source/integration/" + versionID.String(),
			VersionID: "object-version-1", ETag: "etag-1", SizeBytes: 14,
			SHA256: knowledge.SHA256Hex("source-content"), MediaType: "application/pdf",
			OriginalName: "manual.pdf",
		},
		PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: idempotencyKey, RequestFingerprint: requestFingerprint,
		NewDocument: &knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal,
			Title: "Queue Integration Manual", CreatedBy: creatorID,
		},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("QueueVersion: %v", err)
	}
	if queued.Version.Version != 1 || queued.Task.Status != knowledge.IngestionPending {
		t.Fatalf("queued = %+v", queued)
	}
	var facts struct {
		VersionStatus string `gorm:"column:version_status"`
		IsCurrent     bool   `gorm:"column:is_current"`
		TaskStatus    string `gorm:"column:task_status"`
		TaskStage     string `gorm:"column:task_stage"`
		EventCount    int64  `gorm:"column:event_count"`
		OutboxCount   int64  `gorm:"column:outbox_count"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT version.status AS version_status, version.is_current,
       task.status AS task_status, task.stage AS task_stage,
       (SELECT COUNT(*) FROM knowledge_ingestion_events event WHERE event.task_id = task.id) AS event_count,
       (SELECT COUNT(*) FROM outbox_events outbox
          WHERE outbox.id = ? AND outbox.event_type = 'knowledge.ingest'
            AND outbox.aggregate_type = 'knowledge_ingestion_task') AS outbox_count
FROM knowledge_document_versions version
JOIN knowledge_ingestion_tasks task ON task.document_version_id = version.id
WHERE version.id = ? AND task.id = ?`, outboxID, versionID, taskID).Scan(&facts).Error; err != nil {
		t.Fatalf("read queued facts: %v", err)
	}
	if facts.VersionStatus != "queued" || facts.IsCurrent || facts.TaskStatus != "pending" ||
		facts.TaskStage != "uploaded" || facts.EventCount != 1 || facts.OutboxCount != 1 {
		t.Fatalf("queued facts = %+v", facts)
	}
	replayed, replayFingerprint, err := repository.FindQueuedVersionByIdempotency(ctx, creatorID, idempotencyKey)
	if err != nil {
		t.Fatalf("FindQueuedVersionByIdempotency: %v", err)
	}
	if replayed.Task.ID != taskID || replayed.Version.ID != versionID || replayFingerprint != requestFingerprint {
		t.Fatalf("replayed = %+v fingerprint = %q", replayed, replayFingerprint)
	}
}

func publishKnowledgeVersionInput(
	versionID, documentID, creatorID uuid.UUID,
	content string,
	chunks []knowledge.ChunkDraft,
) knowledge.PublishVersionInput {
	return knowledge.PublishVersionInput{
		ID: versionID, DocumentID: documentID,
		SourceMediaType: "text/markdown", SourceSizeBytes: int64(len([]byte(content))),
		SourceSHA256: knowledge.SHA256Hex(content), ParserVersion: "markdown-v1",
		CreatedBy: creatorID, Chunks: chunks,
	}
}

func mustExecKnowledgeTest(t *testing.T, db *gorm.DB, ctx context.Context, query string, args ...any) {
	t.Helper()
	if err := db.WithContext(ctx).Exec(query, args...).Error; err != nil {
		t.Fatalf("prepare knowledge fixture: %v", err)
	}
}
