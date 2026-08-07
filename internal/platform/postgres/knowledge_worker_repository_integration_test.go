//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestKnowledgeWorkerRepositoryClaimsCheckpointsAndPublishesReadyVersion(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, versionID, taskID, outboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	repository := NewKnowledgeWorkerRepository(db)
	profile, err := knowledge.NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		knowledge.EmbeddingInputQuery, knowledge.EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureEmbeddingProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim, err := repository.Claim(ctx, taskID, versionID, "worker-a", now, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claim.Disposition != knowledgeworker.ClaimAcquired || claim.Lease == nil || claim.Lease.AttemptCount != 1 {
		t.Fatalf("claim = %+v", claim)
	}
	held, err := repository.Claim(ctx, taskID, versionID, "worker-b", now.Add(time.Second), now.Add(2*time.Minute))
	if err != nil || held.Disposition != knowledgeworker.ClaimLeaseHeld {
		t.Fatalf("held claim = %+v err=%v", held, err)
	}
	task, err := repository.LoadTask(ctx, *claim.Lease, now.Add(2*time.Second))
	if err != nil || task.Source.ObjectKey == "" || task.PipelineVersion != "ingestion-v1" {
		t.Fatalf("LoadTask = %+v err=%v", task, err)
	}
	saved, err := repository.SaveCheckpoint(ctx, *claim.Lease, knowledgeworker.CheckpointUpdate{
		Stage: knowledge.IngestionStageParsing, ProgressPercent: 45,
		Checkpoint: json.RawMessage(`{"parsedPages":4}`),
	}, now.Add(3*time.Second))
	if err != nil || !saved {
		t.Fatalf("SaveCheckpoint = %v err=%v", saved, err)
	}
	parsedResult := knowledgeworker.ExecutionResult{
		ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"pages":9}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
		Artifact: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeArtifacts, ObjectKey: "knowledge-artifact/integration/" + versionID.String(),
			ETag: "artifact-etag", SizeBytes: 12, SHA256: knowledge.SHA256Hex("artifact"),
			MediaType: "application/json", OriginalName: "manual.elements.json",
		},
		Chunks: []knowledge.ChunkDraft{{
			ElementType: knowledge.ElementText, ContentText: "parsed content",
			SearchText:    knowledge.NormalizeSearchText("parsed content"),
			ContentSHA256: knowledge.SHA256Hex("parsed content"),
		}},
		EmbeddingProfile: &profile,
		Embeddings: []knowledge.ChunkEmbeddingDraft{{
			ChunkOrdinal: 0, ContentSHA256: knowledge.SHA256Hex("parsed content"),
			Vector: unitEmbeddingVector(1024, 0),
		}},
		EmbeddingUsage: knowledge.EmbeddingUsage{TotalTokens: 2},
	}
	staged, err := repository.SaveParsedResult(ctx, *claim.Lease, parsedResult, now.Add(4*time.Second))
	if err != nil || !staged {
		t.Fatalf("SaveParsedResult = %v err=%v", staged, err)
	}
	publishing, err := repository.SaveCheckpoint(ctx, *claim.Lease, knowledgeworker.CheckpointUpdate{
		Stage: knowledge.IngestionStagePublishing, ProgressPercent: 95,
		Checkpoint: json.RawMessage(`{"artifactStaged":true}`),
	}, now.Add(5*time.Second))
	if err != nil || !publishing {
		t.Fatalf("SaveCheckpoint publishing = %v err=%v", publishing, err)
	}
	var publishingStatus string
	if err := db.WithContext(ctx).Raw(
		"SELECT status FROM knowledge_document_versions WHERE id = ?", versionID,
	).Scan(&publishingStatus).Error; err != nil || publishingStatus != "publishing" {
		t.Fatalf("publishing version status = %q err=%v", publishingStatus, err)
	}
	completed, err := repository.Complete(ctx, *claim.Lease, parsedResult, now.Add(6*time.Second))
	if err != nil || !completed {
		t.Fatalf("Complete = %v err=%v", completed, err)
	}
	var facts struct {
		TaskStatus     string
		TaskStage      string
		Progress       int `gorm:"column:progress_percent"`
		VersionStatus  string
		IsCurrent      bool
		EventCount     int64
		ChunkCount     int64
		EmbeddingCount int64
		ArtifactSHA    string `gorm:"column:artifact_sha"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT task.status AS task_status, task.stage AS task_stage, task.progress_percent,
       version.status AS version_status, version.is_current,
       (SELECT COUNT(*) FROM knowledge_ingestion_events event WHERE event.task_id = task.id) AS event_count,
       (SELECT COUNT(*) FROM knowledge_chunks chunk WHERE chunk.document_version_id = version.id) AS chunk_count,
       (SELECT COUNT(*) FROM knowledge_chunk_embeddings embedding
        JOIN knowledge_chunks chunk ON chunk.id = embedding.chunk_id
        WHERE chunk.document_version_id = version.id AND embedding.profile_id = ?) AS embedding_count,
       COALESCE(version.element_artifact_sha256, '') AS artifact_sha
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
WHERE task.id = ?`, profile.ID, taskID).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts.TaskStatus != "succeeded" || facts.TaskStage != "completed" || facts.Progress != 100 ||
		facts.VersionStatus != "ready" || !facts.IsCurrent || facts.EventCount != 6 ||
		facts.ChunkCount != 1 || facts.EmbeddingCount != 1 || facts.ArtifactSHA != knowledge.SHA256Hex("artifact") {
		t.Fatalf("facts = %+v", facts)
	}
	searchResults, err := NewKnowledgeRepository(db).SearchFTS(ctx, creatorID, "parsed content", 5)
	if err != nil {
		t.Fatalf("SearchFTS: %v", err)
	}
	if len(searchResults) != 1 || searchResults[0].DocumentVersionID != versionID ||
		searchResults[0].ContentText != "parsed content" {
		t.Fatalf("search results = %+v", searchResults)
	}
	cleanupKnowledgeWorkerFacts(t, db, creatorID, documentID, versionID, taskID, outboxID)
}

func TestKnowledgeWorkerRepositoryBatchesChunksAndEmbeddings(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, versionID, taskID, outboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	defer cleanupKnowledgeWorkerFacts(t, db, creatorID, documentID, versionID, taskID, outboxID)
	repository, err := NewKnowledgeWorkerRepositoryWithBatchSize(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := knowledge.NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		knowledge.EmbeddingInputQuery, knowledge.EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureEmbeddingProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim, err := repository.Claim(ctx, taskID, versionID, "worker-batch", now, now.Add(time.Minute))
	if err != nil || claim.Lease == nil {
		t.Fatalf("Claim = %+v err=%v", claim, err)
	}
	result := knowledgeworker.ExecutionResult{
		ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"pages":3}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
		Artifact: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeArtifacts, ObjectKey: "knowledge-artifact/integration/" + versionID.String(),
			ETag: "artifact-etag", SizeBytes: 12, SHA256: knowledge.SHA256Hex("artifact"),
			MediaType: "application/json", OriginalName: "batch.elements.json",
		},
		EmbeddingProfile: &profile, EmbeddingUsage: knowledge.EmbeddingUsage{TotalTokens: 6},
	}
	for ordinal := 0; ordinal < 3; ordinal++ {
		content := "batch content " + string(rune('a'+ordinal))
		result.Chunks = append(result.Chunks, knowledge.ChunkDraft{
			ElementType: knowledge.ElementText, SectionPath: []string{"batch"},
			ContentText: content, SearchText: knowledge.NormalizeSearchText(content),
			ContentSHA256: knowledge.SHA256Hex(content),
		})
		result.Embeddings = append(result.Embeddings, knowledge.ChunkEmbeddingDraft{
			ChunkOrdinal: ordinal, ContentSHA256: knowledge.SHA256Hex(content),
			Vector: unitEmbeddingVector(1024, ordinal),
		})
	}
	staged, err := repository.SaveParsedResult(ctx, *claim.Lease, result, now.Add(time.Second))
	if err != nil || !staged {
		t.Fatalf("SaveParsedResult = %v err=%v", staged, err)
	}
	var counts struct {
		Chunks     int64
		Embeddings int64
	}
	if err := db.WithContext(ctx).Raw(`
SELECT COUNT(DISTINCT chunk.id) AS chunks, COUNT(embedding.chunk_id) AS embeddings
FROM knowledge_chunks chunk
LEFT JOIN knowledge_chunk_embeddings embedding ON embedding.chunk_id = chunk.id
WHERE chunk.document_version_id = ?`, versionID).Scan(&counts).Error; err != nil {
		t.Fatal(err)
	}
	if counts.Chunks != 3 || counts.Embeddings != 3 {
		t.Fatalf("counts = %+v", counts)
	}
}

func unitEmbeddingVector(dimensions, index int) []float32 {
	vector := make([]float32, dimensions)
	vector[index] = 1
	return vector
}

func TestKnowledgeWorkerRepositoryReclaimsExpiredLeaseAndFencesOldOwner(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, versionID, taskID, outboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	repository := NewKnowledgeWorkerRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := repository.Claim(ctx, taskID, versionID, "worker-a", now, now.Add(2*time.Second))
	if err != nil || first.Lease == nil {
		t.Fatalf("first claim = %+v err=%v", first, err)
	}
	second, err := repository.Claim(ctx, taskID, versionID, "worker-b", now.Add(3*time.Second), now.Add(time.Minute))
	if err != nil || second.Disposition != knowledgeworker.ClaimAcquired || second.Lease == nil || second.Lease.AttemptCount != 2 {
		t.Fatalf("second claim = %+v err=%v", second, err)
	}
	oldSaved, err := repository.SaveCheckpoint(ctx, *first.Lease, knowledgeworker.CheckpointUpdate{
		Stage: knowledge.IngestionStageParsing, ProgressPercent: 10, Checkpoint: json.RawMessage(`{"old":true}`),
	}, now.Add(4*time.Second))
	if err != nil || oldSaved {
		t.Fatalf("old lease checkpoint = %v err=%v", oldSaved, err)
	}
	newSaved, err := repository.SaveCheckpoint(ctx, *second.Lease, knowledgeworker.CheckpointUpdate{
		Stage: knowledge.IngestionStageParsing, ProgressPercent: 25, Checkpoint: json.RawMessage(`{"new":true}`),
	}, now.Add(4*time.Second))
	if err != nil || !newSaved {
		t.Fatalf("new lease checkpoint = %v err=%v", newSaved, err)
	}
	oldResult := knowledgeParsedResult(versionID, "old lease content")
	oldParsed, err := repository.SaveParsedResult(ctx, *first.Lease, oldResult, now.Add(5*time.Second))
	if err != nil || oldParsed {
		t.Fatalf("old lease parsed result = %v err=%v", oldParsed, err)
	}
	newResult := knowledgeParsedResult(versionID, "new lease content")
	newParsed, err := repository.SaveParsedResult(ctx, *second.Lease, newResult, now.Add(5*time.Second))
	if err != nil || !newParsed {
		t.Fatalf("new lease parsed result = %v err=%v", newParsed, err)
	}
	var staged struct {
		ContentText string
		ArtifactSHA string `gorm:"column:artifact_sha"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT chunk.content_text, version.element_artifact_sha256 AS artifact_sha
FROM knowledge_chunks chunk
JOIN knowledge_document_versions version ON version.id = chunk.document_version_id
WHERE version.id = ?`, versionID).Scan(&staged).Error; err != nil {
		t.Fatal(err)
	}
	if staged.ContentText != "new lease content" || staged.ArtifactSHA != newResult.Artifact.SHA256 {
		t.Fatalf("staged result = %+v", staged)
	}
	cleanupKnowledgeWorkerFacts(t, db, creatorID, documentID, versionID, taskID, outboxID)
}

func TestKnowledgeWorkerRepositoryRetryDelayAndPartialReadyDoNotPublish(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, versionID, taskID, outboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	repository := NewKnowledgeWorkerRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, _ := repository.Claim(ctx, taskID, versionID, "worker-a", now, now.Add(time.Minute))
	availableAt := now.Add(2 * time.Minute)
	released, err := repository.ReleaseForRetry(ctx, *first.Lease, "ocr_unavailable", "retry OCR", now.Add(time.Second), availableAt)
	if err != nil || !released {
		t.Fatalf("ReleaseForRetry = %v err=%v", released, err)
	}
	delayed, err := repository.Claim(ctx, taskID, versionID, "worker-b", now.Add(time.Minute), now.Add(3*time.Minute))
	if err != nil || delayed.Disposition != knowledgeworker.ClaimDelayed || delayed.RetryAfter != time.Minute {
		t.Fatalf("delayed = %+v err=%v", delayed, err)
	}
	second, err := repository.Claim(ctx, taskID, versionID, "worker-b", availableAt, availableAt.Add(time.Minute))
	if err != nil || second.Lease == nil || second.Lease.AttemptCount != 2 {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	completed, err := repository.Complete(ctx, *second.Lease, knowledgeworker.ExecutionResult{
		Partial: true, ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"missing":["ocr"]}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
	}, availableAt.Add(time.Second))
	if err != nil || !completed {
		t.Fatalf("Complete partial = %v err=%v", completed, err)
	}
	var version struct {
		Status    string
		IsCurrent bool
	}
	if err := db.WithContext(ctx).Raw("SELECT status, is_current FROM knowledge_document_versions WHERE id = ?", versionID).Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version.Status != "partial_ready" || version.IsCurrent {
		t.Fatalf("version = %+v", version)
	}
	cleanupKnowledgeWorkerFacts(t, db, creatorID, documentID, versionID, taskID, outboxID)
}

func TestKnowledgeRepositoryRequestsCancellationAndWorkerFinalizesIt(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, versionID, taskID, outboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	control := NewKnowledgeRepository(db)
	requested, err := control.RequestIngestionCancellation(ctx, taskID, creatorID, time.Now().UTC())
	if err != nil || !requested.Changed || requested.Task.Status != knowledge.IngestionCancelRequested {
		t.Fatalf("RequestIngestionCancellation = %+v err=%v", requested, err)
	}
	replayed, err := control.RequestIngestionCancellation(ctx, taskID, creatorID, time.Now().UTC())
	if err != nil || replayed.Changed {
		t.Fatalf("replayed cancellation = %+v err=%v", replayed, err)
	}
	repository := NewKnowledgeWorkerRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim, err := repository.Claim(ctx, taskID, versionID, "worker-cancel", now, now.Add(time.Minute))
	if err != nil || claim.Disposition != knowledgeworker.ClaimCancellation || claim.Lease == nil {
		t.Fatalf("cancellation claim = %+v err=%v", claim, err)
	}
	finalized, err := repository.FinalizeCancellation(ctx, *claim.Lease, now.Add(time.Second))
	if err != nil || !finalized {
		t.Fatalf("FinalizeCancellation = %v err=%v", finalized, err)
	}
	detail, err := control.FindIngestionTask(ctx, taskID)
	if err != nil || detail.Status != knowledge.IngestionCancelled || detail.CompletedAt == nil {
		t.Fatalf("FindIngestionTask = %+v err=%v", detail, err)
	}
	cleanupKnowledgeWorkerFacts(t, db, creatorID, documentID, versionID, taskID, outboxID)
}

func TestKnowledgeWorkerRepositoryOlderVersionCannotReplaceNewerCurrent(t *testing.T) {
	db, ctx, cleanup := prepareKnowledgeWorkerIntegration(t)
	defer cleanup()
	creatorID, documentID, olderVersionID, olderTaskID, olderOutboxID := insertQueuedKnowledgeTask(t, db, ctx, nil, 3)
	newerVersionID, newerTaskID, newerOutboxID := uuid.New(), uuid.New(), uuid.New()
	repository := NewKnowledgeRepository(db)
	_, err := repository.QueueVersion(ctx, knowledge.QueueVersionInput{
		VersionID: newerVersionID, TaskID: newerTaskID, OutboxEventID: newerOutboxID,
		CorrelationID: uuid.New(), DocumentID: documentID, CreatedBy: creatorID,
		Source: objectstore.ObjectRef{
			Bucket:    objectstore.BucketKnowledgeSources,
			ObjectKey: "knowledge-source/integration/" + newerVersionID.String(), ETag: "etag-newer",
			SizeBytes: 13, SHA256: knowledge.SHA256Hex("newer-content"), MediaType: "text/plain",
			OriginalName: "manual-v2.txt",
		},
		PipelineVersion: "ingestion-v1", MaxAttempts: 3,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: knowledge.SHA256Hex("request-" + newerTaskID.String()),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("queue newer version: %v", err)
	}
	worker := NewKnowledgeWorkerRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	newerClaim, err := worker.Claim(ctx, newerTaskID, newerVersionID, "worker-newer", now, now.Add(time.Minute))
	if err != nil || newerClaim.Lease == nil {
		t.Fatalf("claim newer = %+v err=%v", newerClaim, err)
	}
	if completed, err := worker.Complete(ctx, *newerClaim.Lease, knowledgeworker.ExecutionResult{
		ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"version":2}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
	}, now.Add(time.Second)); err != nil || !completed {
		t.Fatalf("complete newer = %v err=%v", completed, err)
	}
	olderClaim, err := worker.Claim(ctx, olderTaskID, olderVersionID, "worker-older", now.Add(2*time.Second), now.Add(time.Minute))
	if err != nil || olderClaim.Lease == nil {
		t.Fatalf("claim older = %+v err=%v", olderClaim, err)
	}
	if completed, err := worker.Complete(ctx, *olderClaim.Lease, knowledgeworker.ExecutionResult{
		ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"version":1}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
	}, now.Add(3*time.Second)); err != nil || !completed {
		t.Fatalf("complete older = %v err=%v", completed, err)
	}
	var current struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT id FROM knowledge_document_versions WHERE document_id = ? AND is_current = true`, documentID).Scan(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.ID != newerVersionID {
		t.Fatalf("current version = %s, want newer %s", current.ID, newerVersionID)
	}
	cleanupDB := db.WithContext(context.Background())
	_ = cleanupDB.Exec("DELETE FROM outbox_events WHERE id IN (?, ?)", olderOutboxID, newerOutboxID).Error
	_ = cleanupDB.Exec("DELETE FROM knowledge_ingestion_events WHERE task_id IN (?, ?)", olderTaskID, newerTaskID).Error
	_ = cleanupDB.Exec("DELETE FROM knowledge_ingestion_tasks WHERE id IN (?, ?)", olderTaskID, newerTaskID).Error
	_ = cleanupDB.Exec("DELETE FROM knowledge_document_versions WHERE id IN (?, ?)", olderVersionID, newerVersionID).Error
	_ = cleanupDB.Exec("DELETE FROM knowledge_documents WHERE id = ?", documentID).Error
	_ = cleanupDB.Exec("DELETE FROM users WHERE id = ?", creatorID).Error
}

func prepareKnowledgeWorkerIntegration(t *testing.T) (*gorm.DB, context.Context, func()) {
	t.Helper()
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	return db, ctx, func() { cancel(); _ = sqlDB.Close() }
}

func insertQueuedKnowledgeTask(
	t *testing.T, db *gorm.DB, ctx context.Context, documentID *uuid.UUID, maxAttempts int,
) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	creatorID := uuid.New()
	if documentID == nil {
		value := uuid.New()
		documentID = &value
	}
	versionID, taskID, outboxID := uuid.New(), uuid.New(), uuid.New()
	mustExecKnowledgeTest(t, db, ctx, `
INSERT INTO users (id, username, display_name, password_hash, role)
VALUES (?, ?, 'Knowledge Worker Owner', 'test-hash', 'admin')`,
		creatorID, "knowledge_worker_"+uuid.NewString()[:8])
	repository := NewKnowledgeRepository(db)
	_, err := repository.QueueVersion(ctx, knowledge.QueueVersionInput{
		VersionID: versionID, TaskID: taskID, OutboxEventID: outboxID,
		CorrelationID: uuid.New(), DocumentID: *documentID, CreatedBy: creatorID,
		Source: objectstore.ObjectRef{
			Bucket:    objectstore.BucketKnowledgeSources,
			ObjectKey: "knowledge-source/integration/" + versionID.String(), ETag: "etag",
			SizeBytes: 7, SHA256: knowledge.SHA256Hex("content"), MediaType: "text/plain",
			OriginalName: "manual.txt",
		},
		PipelineVersion: "ingestion-v1", MaxAttempts: maxAttempts,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: knowledge.SHA256Hex("request-" + taskID.String()),
		NewDocument: &knowledge.CreateDocumentInput{
			ID: *documentID, Scope: knowledge.ScopeGlobal, Title: "Worker Integration Manual", CreatedBy: creatorID,
		},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("QueueVersion: %v", err)
	}
	return creatorID, *documentID, versionID, taskID, outboxID
}

func cleanupKnowledgeWorkerFacts(
	t *testing.T, db *gorm.DB, creatorID, documentID, versionID, taskID, outboxID uuid.UUID,
) {
	t.Helper()
	cleanup := db.WithContext(context.Background())
	_ = cleanup.Exec("DELETE FROM outbox_events WHERE id = ?", outboxID).Error
	_ = cleanup.Exec("DELETE FROM knowledge_ingestion_events WHERE task_id = ?", taskID).Error
	_ = cleanup.Exec("DELETE FROM knowledge_ingestion_tasks WHERE id = ?", taskID).Error
	_ = cleanup.Exec("DELETE FROM knowledge_chunks WHERE document_version_id = ?", versionID).Error
	_ = cleanup.Exec("DELETE FROM knowledge_document_versions WHERE id = ?", versionID).Error
	_ = cleanup.Exec("DELETE FROM knowledge_documents WHERE id = ?", documentID).Error
	_ = cleanup.Exec("DELETE FROM users WHERE id = ?", creatorID).Error
}

func knowledgeParsedResult(versionID uuid.UUID, content string) knowledgeworker.ExecutionResult {
	return knowledgeworker.ExecutionResult{
		ParserVersion: "parser-v1", ParserMetadata: json.RawMessage(`{"elements":1}`),
		Checkpoint: json.RawMessage(`{"indexed":true}`),
		Artifact: objectstore.ObjectRef{
			Bucket:    objectstore.BucketKnowledgeArtifacts,
			ObjectKey: "knowledge-artifact/integration/" + versionID.String() + "/" + knowledge.SHA256Hex(content),
			ETag:      "artifact-etag", SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex("artifact-" + content),
			MediaType: "application/json", OriginalName: "manual.elements.json",
		},
		Chunks: []knowledge.ChunkDraft{{
			ElementType: knowledge.ElementText, ContentText: content,
			SearchText: knowledge.NormalizeSearchText(content), ContentSHA256: knowledge.SHA256Hex(content),
		}},
	}
}
