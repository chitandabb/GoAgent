package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

const defaultKnowledgeChunkWriteBatchSize = 100

type KnowledgeWorkerRepository struct {
	db                  *gorm.DB
	chunkWriteBatchSize int
}

func NewKnowledgeWorkerRepository(db *gorm.DB) *KnowledgeWorkerRepository {
	return &KnowledgeWorkerRepository{db: db, chunkWriteBatchSize: defaultKnowledgeChunkWriteBatchSize}
}

func NewKnowledgeWorkerRepositoryWithBatchSize(db *gorm.DB, batchSize int) (*KnowledgeWorkerRepository, error) {
	if batchSize < 1 || batchSize > 500 {
		return nil, errors.New("knowledge chunk write batch size must be between 1 and 500")
	}
	return &KnowledgeWorkerRepository{db: db, chunkWriteBatchSize: batchSize}, nil
}

type knowledgeChunkInsert struct {
	ID                uuid.UUID             `gorm:"column:id"`
	DocumentVersionID uuid.UUID             `gorm:"column:document_version_id"`
	Ordinal           int                   `gorm:"column:ordinal"`
	PageNumber        *int                  `gorm:"column:page_number"`
	ElementIndex      *int                  `gorm:"column:element_index"`
	ElementType       knowledge.ElementType `gorm:"column:element_type"`
	SectionPath       string                `gorm:"column:section_path;type:jsonb"`
	ContentText       string                `gorm:"column:content_text"`
	SearchText        string                `gorm:"column:search_text"`
	ContentSHA256     string                `gorm:"column:content_sha256"`
	Metadata          string                `gorm:"column:metadata;type:jsonb"`
}

func (knowledgeChunkInsert) TableName() string { return "knowledge_chunks" }

type knowledgeChunkEmbeddingInsert struct {
	ChunkID       uuid.UUID       `gorm:"column:chunk_id"`
	ProfileID     uuid.UUID       `gorm:"column:profile_id"`
	ContentSHA256 string          `gorm:"column:content_sha256"`
	Embedding     pgvector.Vector `gorm:"column:embedding;type:vector(1024)"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
}

func (knowledgeChunkEmbeddingInsert) TableName() string { return "knowledge_chunk_embeddings" }

var _ knowledgeworker.Repository = (*KnowledgeWorkerRepository)(nil)

func (r *KnowledgeWorkerRepository) EnsureEmbeddingProfile(
	ctx context.Context,
	profile knowledge.EmbeddingProfile,
) error {
	if r == nil || r.db == nil {
		return errors.New("knowledge worker repository is unavailable")
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('mesguard_knowledge_embedding_profile'))").Error; err != nil {
			return err
		}
		var active struct {
			ID          uuid.UUID
			Fingerprint string
		}
		query := tx.Raw(`
SELECT id, fingerprint
FROM knowledge_embedding_profiles
WHERE status = 'active'
FOR UPDATE`).Scan(&active)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 1 {
			if active.ID == profile.ID && active.Fingerprint == profile.Fingerprint {
				return nil
			}
			return errors.New("a different embedding profile is active; stage and backfill it before switching")
		}

		var existingStatus string
		existing := tx.Raw(
			"SELECT status FROM knowledge_embedding_profiles WHERE id = ? AND fingerprint = ? FOR UPDATE",
			profile.ID, profile.Fingerprint,
		).Scan(&existingStatus)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			return tx.Exec(`
UPDATE knowledge_embedding_profiles
SET status = 'active', activated_at = ?
WHERE id = ? AND fingerprint = ?`, now, profile.ID, profile.Fingerprint).Error
		}
		return tx.Exec(`
INSERT INTO knowledge_embedding_profiles
    (id, profile_key, provider, model, dimensions, distance_metric,
     query_input_type, document_input_type, normalized, config_version,
     fingerprint, status, activated_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
			profile.ID, profile.Key, profile.Provider, profile.Model, profile.Dimensions,
			profile.DistanceMetric, profile.QueryInputType, profile.DocumentInputType,
			profile.Normalize, profile.ConfigVersion, profile.Fingerprint, now, now,
		).Error
	})
	if err != nil {
		return fmt.Errorf("ensure active knowledge embedding profile: %w", TranslateError(err))
	}
	return nil
}

func (r *KnowledgeWorkerRepository) Claim(
	ctx context.Context,
	taskID, documentVersionID uuid.UUID,
	workerID string,
	claimedAt, leaseUntil time.Time,
) (knowledgeworker.ClaimResult, error) {
	if r == nil || r.db == nil {
		return knowledgeworker.ClaimResult{}, errors.New("knowledge worker repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	if taskID == uuid.Nil || documentVersionID == uuid.Nil || workerID == "" || len(workerID) > 128 ||
		!leaseUntil.After(claimedAt) {
		return knowledgeworker.ClaimResult{}, errors.New("knowledge worker claim is invalid")
	}
	claimedAt, leaseUntil = claimedAt.UTC(), leaseUntil.UTC()
	var claim knowledgeworker.ClaimResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		state, err := selectKnowledgeTaskForUpdate(tx, taskID, documentVersionID)
		if err != nil {
			return err
		}
		switch state.Status {
		case knowledge.IngestionSucceeded, knowledge.IngestionPartialSucceeded,
			knowledge.IngestionFailed, knowledge.IngestionCancelled:
			claim = knowledgeworker.ClaimResult{Disposition: knowledgeworker.ClaimTerminal, Status: state.Status}
			return nil
		case knowledge.IngestionRetryWait:
			if state.AvailableAt.After(claimedAt) {
				claim = knowledgeworker.ClaimResult{
					Disposition: knowledgeworker.ClaimDelayed, Status: state.Status,
					RetryAfter: state.AvailableAt.Sub(claimedAt),
				}
				return nil
			}
		case knowledge.IngestionRunning:
			if state.LeaseUntil != nil && state.LeaseUntil.After(claimedAt) {
				claim = knowledgeworker.ClaimResult{Disposition: knowledgeworker.ClaimLeaseHeld, Status: state.Status}
				return nil
			}
			if state.AttemptCount >= state.MaxAttempts {
				if err := failExpiredKnowledgeTask(tx, state, claimedAt); err != nil {
					return err
				}
				claim = knowledgeworker.ClaimResult{Disposition: knowledgeworker.ClaimTerminal, Status: knowledge.IngestionFailed}
				return nil
			}
		case knowledge.IngestionCancelRequested:
			if state.LeaseUntil != nil && state.LeaseUntil.After(claimedAt) {
				claim = knowledgeworker.ClaimResult{Disposition: knowledgeworker.ClaimLeaseHeld, Status: state.Status}
				return nil
			}
			lease := knowledgeworker.Lease{
				TaskID: taskID, DocumentVersionID: documentVersionID, ClaimOwner: workerID,
				AttemptCount: state.AttemptCount, MaxAttempts: state.MaxAttempts, LeaseUntil: leaseUntil,
			}
			updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET claim_owner = ?, claimed_at = ?, lease_until = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'cancel_requested'
  AND attempt_count = ?`, workerID, claimedAt, leaseUntil, claimedAt, claimedAt,
				taskID, documentVersionID, state.AttemptCount)
			if updated.Error != nil {
				return TranslateError(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return knowledgeworker.ErrLeaseLost
			}
			claim = knowledgeworker.ClaimResult{
				Disposition: knowledgeworker.ClaimCancellation, Status: state.Status, Lease: &lease,
			}
			return nil
		case knowledge.IngestionPending:
		default:
			return fmt.Errorf("unsupported knowledge ingestion task status %q", state.Status)
		}

		attemptCount := state.AttemptCount + 1
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = 'running', attempt_count = ?, claim_owner = ?, claimed_at = ?,
    lease_until = ?, heartbeat_at = ?, started_at = COALESCE(started_at, ?), updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = ? AND attempt_count = ?`,
			attemptCount, workerID, claimedAt, leaseUntil, claimedAt, claimedAt, claimedAt,
			taskID, documentVersionID, state.Status, state.AttemptCount)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return knowledgeworker.ErrLeaseLost
		}
		// A reclaimed task may still have its version in publishing after a
		// process interruption, before Complete commits the final publication.
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET status = 'processing'
WHERE id = ? AND status IN ('queued', 'processing', 'scanning', 'parsing', 'chunking', 'indexing', 'publishing')`,
			documentVersionID).Error; err != nil {
			return TranslateError(err)
		}
		eventType := "ingestion_started"
		if state.AttemptCount > 0 {
			eventType = "ingestion_reclaimed"
		}
		if err := appendKnowledgeIngestionEvent(tx, taskID, eventType, map[string]any{
			"taskId": taskID.String(), "documentVersionId": documentVersionID.String(),
			"status": string(knowledge.IngestionRunning), "attemptCount": attemptCount,
		}, claimedAt); err != nil {
			return err
		}
		lease := knowledgeworker.Lease{
			TaskID: taskID, DocumentVersionID: documentVersionID, ClaimOwner: workerID,
			AttemptCount: attemptCount, MaxAttempts: state.MaxAttempts, LeaseUntil: leaseUntil,
		}
		claim = knowledgeworker.ClaimResult{
			Disposition: knowledgeworker.ClaimAcquired, Status: knowledge.IngestionRunning, Lease: &lease,
		}
		return nil
	})
	if err != nil {
		return knowledgeworker.ClaimResult{}, TranslateError(err)
	}
	return claim, nil
}

func (r *KnowledgeWorkerRepository) Renew(
	ctx context.Context,
	lease knowledgeworker.Lease,
	renewedAt, newLeaseUntil time.Time,
) (knowledgeworker.RenewalResult, error) {
	if r == nil || r.db == nil {
		return knowledgeworker.RenewalResult{}, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil || !newLeaseUntil.After(renewedAt) {
		return knowledgeworker.RenewalResult{}, errors.New("knowledge worker lease renewal is invalid")
	}
	var row struct {
		Status knowledge.IngestionTaskStatus `gorm:"column:status"`
	}
	result := ResolveDB(ctx, r.db).Raw(`
UPDATE knowledge_ingestion_tasks
SET lease_until = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status IN ('running', 'cancel_requested')
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?
RETURNING status`, newLeaseUntil.UTC(), renewedAt.UTC(), renewedAt.UTC(),
		lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner, lease.AttemptCount, renewedAt.UTC()).Scan(&row)
	if result.Error != nil {
		return knowledgeworker.RenewalResult{}, TranslateError(result.Error)
	}
	return knowledgeworker.RenewalResult{
		Owned:                 result.RowsAffected == 1,
		CancellationRequested: result.RowsAffected == 1 && row.Status == knowledge.IngestionCancelRequested,
	}, nil
}

func (r *KnowledgeWorkerRepository) LoadTask(
	ctx context.Context,
	lease knowledgeworker.Lease,
	now time.Time,
) (knowledgeworker.Task, error) {
	if r == nil || r.db == nil {
		return knowledgeworker.Task{}, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return knowledgeworker.Task{}, err
	}
	type row struct {
		TaskID              uuid.UUID                `gorm:"column:task_id"`
		DocumentVersionID   uuid.UUID                `gorm:"column:document_version_id"`
		DocumentID          uuid.UUID                `gorm:"column:document_id"`
		CreatedBy           uuid.UUID                `gorm:"column:created_by"`
		Stage               knowledge.IngestionStage `gorm:"column:stage"`
		AttemptCount        int                      `gorm:"column:attempt_count"`
		MaxAttempts         int                      `gorm:"column:max_attempts"`
		Checkpoint          []byte                   `gorm:"column:checkpoint"`
		ProgressPercent     int                      `gorm:"column:progress_percent"`
		PipelineVersion     string                   `gorm:"column:pipeline_version"`
		SourceBucket        objectstore.Bucket       `gorm:"column:source_bucket"`
		SourceObjectKey     string                   `gorm:"column:source_object_key"`
		SourceObjectVersion string                   `gorm:"column:source_object_version"`
		SourceETag          string                   `gorm:"column:source_etag"`
		SourceSizeBytes     int64                    `gorm:"column:source_size_bytes"`
		SourceSHA256        string                   `gorm:"column:source_sha256"`
		SourceMediaType     string                   `gorm:"column:source_media_type"`
		SourceOriginalName  string                   `gorm:"column:source_original_name"`
	}
	var found row
	result := ResolveDB(ctx, r.db).Raw(`
SELECT task.id AS task_id, task.document_version_id, version.document_id,
       task.created_by, task.stage, task.attempt_count, task.max_attempts,
       task.checkpoint, task.progress_percent, version.pipeline_version,
       version.source_bucket, version.source_object_key,
       COALESCE(version.source_object_version, '') AS source_object_version,
       version.source_etag, version.source_size_bytes, version.source_sha256,
       version.source_media_type, version.source_original_name
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
JOIN knowledge_documents document ON document.id = version.document_id
WHERE task.id = ? AND task.document_version_id = ? AND task.status = 'running'
  AND task.claim_owner = ? AND task.attempt_count = ? AND task.lease_until > ?
  AND document.deleted_at IS NULL`, lease.TaskID, lease.DocumentVersionID,
		lease.ClaimOwner, lease.AttemptCount, now.UTC()).Scan(&found)
	if result.Error != nil {
		return knowledgeworker.Task{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledgeworker.Task{}, knowledgeworker.ErrLeaseLost
	}
	source := objectstore.ObjectRef{
		Bucket: found.SourceBucket, ObjectKey: found.SourceObjectKey,
		VersionID: found.SourceObjectVersion, ETag: found.SourceETag,
		SizeBytes: found.SourceSizeBytes, SHA256: found.SourceSHA256,
		MediaType: found.SourceMediaType, OriginalName: found.SourceOriginalName,
	}
	if err := source.Validate(); err != nil || strings.TrimSpace(found.PipelineVersion) == "" {
		return knowledgeworker.Task{}, fmt.Errorf("%w: source reference or pipeline version", knowledgeworker.ErrPermanentInput)
	}
	var checkpoint map[string]any
	if len(found.Checkpoint) == 0 || json.Unmarshal(found.Checkpoint, &checkpoint) != nil || checkpoint == nil {
		return knowledgeworker.Task{}, fmt.Errorf("%w: checkpoint", knowledgeworker.ErrPermanentInput)
	}
	return knowledgeworker.Task{
		ID: found.TaskID, DocumentVersionID: found.DocumentVersionID, DocumentID: found.DocumentID,
		CreatedBy: found.CreatedBy, Stage: found.Stage, AttemptCount: found.AttemptCount,
		MaxAttempts: found.MaxAttempts, Checkpoint: json.RawMessage(found.Checkpoint),
		ProgressPercent: found.ProgressPercent, PipelineVersion: found.PipelineVersion, Source: source,
	}, nil
}

func (r *KnowledgeWorkerRepository) SaveCheckpoint(
	ctx context.Context,
	lease knowledgeworker.Lease,
	update knowledgeworker.CheckpointUpdate,
	updatedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return false, err
	}
	versionStatus := documentVersionStatusForStage(update.Stage)
	saved := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedKnowledgeTask(tx, lease, updatedAt)
		if err != nil || !owned {
			return err
		}
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET stage = ?, checkpoint = ?::jsonb, progress_percent = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'running'
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?`,
			update.Stage, string(update.Checkpoint), update.ProgressPercent, updatedAt.UTC(), updatedAt.UTC(),
			lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner, lease.AttemptCount, updatedAt.UTC())
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return knowledgeworker.ErrLeaseLost
		}
		if err := tx.Exec("UPDATE knowledge_document_versions SET status = ? WHERE id = ?",
			versionStatus, lease.DocumentVersionID).Error; err != nil {
			return TranslateError(err)
		}
		if err := appendKnowledgeIngestionEvent(tx, lease.TaskID, "ingestion_checkpointed", map[string]any{
			"taskId": lease.TaskID.String(), "documentVersionId": lease.DocumentVersionID.String(),
			"stage": string(update.Stage), "progressPercent": update.ProgressPercent,
			"attemptCount": lease.AttemptCount,
		}, updatedAt); err != nil {
			return err
		}
		saved = true
		return nil
	})
	if errors.Is(err, knowledgeworker.ErrLeaseLost) {
		return false, nil
	}
	return saved, TranslateError(err)
}

func (r *KnowledgeWorkerRepository) SaveParsedResult(
	ctx context.Context,
	lease knowledgeworker.Lease,
	result knowledgeworker.ExecutionResult,
	updatedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return false, err
	}
	if err := validateParsedExecutionResult(result); err != nil {
		return false, err
	}
	saved := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedKnowledgeTask(tx, lease, updatedAt)
		if err != nil || !owned {
			return err
		}
		if err := tx.Exec(
			"DELETE FROM knowledge_chunks WHERE document_version_id = ?", lease.DocumentVersionID,
		).Error; err != nil {
			return TranslateError(err)
		}
		chunkRows, embeddingRows, err := knowledgeWriteRows(lease, result, updatedAt)
		if err != nil {
			return err
		}
		if err := tx.CreateInBatches(&chunkRows, r.chunkWriteBatchSize).Error; err != nil {
			return TranslateError(err)
		}
		if len(embeddingRows) > 0 {
			if err := tx.CreateInBatches(&embeddingRows, r.chunkWriteBatchSize).Error; err != nil {
				return TranslateError(err)
			}
		}
		artifact := result.Artifact
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET parser_version = ?, parser_metadata = ?::jsonb,
    element_artifact_bucket = ?, element_artifact_object_key = ?,
    element_artifact_object_version = ?, element_artifact_etag = ?,
    element_artifact_size_bytes = ?, element_artifact_sha256 = ?
WHERE id = ?`,
			result.ParserVersion, string(result.ParserMetadata), artifact.Bucket, artifact.ObjectKey,
			nullableTrimmedString(artifact.VersionID), artifact.ETag, artifact.SizeBytes,
			artifact.SHA256, lease.DocumentVersionID,
		).Error; err != nil {
			return TranslateError(err)
		}
		if err := appendKnowledgeIngestionEvent(tx, lease.TaskID, "ingestion_result_staged", map[string]any{
			"taskId": lease.TaskID.String(), "documentVersionId": lease.DocumentVersionID.String(),
			"artifactSha256": artifact.SHA256, "chunkCount": len(result.Chunks),
			"attemptCount": lease.AttemptCount,
		}, updatedAt); err != nil {
			return err
		}
		saved = true
		return nil
	})
	if errors.Is(err, knowledgeworker.ErrLeaseLost) {
		return false, nil
	}
	return saved, TranslateError(err)
}

func knowledgeWriteRows(
	lease knowledgeworker.Lease,
	result knowledgeworker.ExecutionResult,
	createdAt time.Time,
) ([]knowledgeChunkInsert, []knowledgeChunkEmbeddingInsert, error) {
	chunks := make([]knowledgeChunkInsert, 0, len(result.Chunks))
	embeddings := make([]knowledgeChunkEmbeddingInsert, 0, len(result.Embeddings))
	for ordinal, chunk := range result.Chunks {
		sectionPath, err := json.Marshal(chunk.SectionPath)
		if err != nil {
			return nil, nil, err
		}
		if chunk.SectionPath == nil {
			sectionPath = json.RawMessage(`[]`)
		}
		metadata := chunk.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		chunkID := uuid.New()
		chunks = append(chunks, knowledgeChunkInsert{
			ID: chunkID, DocumentVersionID: lease.DocumentVersionID, Ordinal: ordinal,
			PageNumber: chunk.PageNumber, ElementIndex: chunk.ElementIndex, ElementType: chunk.ElementType,
			SectionPath: string(sectionPath), ContentText: chunk.ContentText, SearchText: chunk.SearchText,
			ContentSHA256: chunk.ContentSHA256, Metadata: string(metadata),
		})
		if result.EmbeddingProfile != nil {
			embedding := result.Embeddings[ordinal]
			embeddings = append(embeddings, knowledgeChunkEmbeddingInsert{
				ChunkID: chunkID, ProfileID: result.EmbeddingProfile.ID,
				ContentSHA256: embedding.ContentSHA256, Embedding: pgvector.NewVector(embedding.Vector),
				CreatedAt: createdAt.UTC(),
			})
		}
	}
	return chunks, embeddings, nil
}

func (r *KnowledgeWorkerRepository) Complete(
	ctx context.Context,
	lease knowledgeworker.Lease,
	result knowledgeworker.ExecutionResult,
	completedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return false, err
	}
	parserVersion := strings.TrimSpace(result.ParserVersion)
	if parserVersion == "" || len(parserVersion) > 128 || !validJSONObject(result.ParserMetadata) ||
		!validJSONObject(result.Checkpoint) {
		return false, errors.New("knowledge ingestion result is invalid")
	}
	taskStatus, versionStatus := knowledge.IngestionSucceeded, "ready"
	if result.Partial {
		taskStatus, versionStatus = knowledge.IngestionPartialSucceeded, "partial_ready"
	}
	completed := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedKnowledgeTask(tx, lease, completedAt)
		if err != nil || !owned {
			return err
		}
		publishCurrent := false
		if !result.Partial {
			var publication struct {
				DocumentID       uuid.UUID `gorm:"column:document_id"`
				CandidateVersion int       `gorm:"column:candidate_version"`
				CurrentVersion   int       `gorm:"column:current_version"`
			}
			locked := tx.Raw(`
SELECT document.id AS document_id,
       version.version AS candidate_version,
       COALESCE((
           SELECT MAX(current_version.version)
           FROM knowledge_document_versions current_version
           WHERE current_version.document_id = document.id AND current_version.is_current = true
       ), 0) AS current_version
FROM knowledge_documents document
JOIN knowledge_document_versions version ON version.document_id = document.id
WHERE version.id = ? AND document.deleted_at IS NULL
FOR UPDATE OF document`, lease.DocumentVersionID).Scan(&publication)
			if locked.Error != nil {
				return TranslateError(locked.Error)
			}
			if locked.RowsAffected != 1 {
				return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
			}
			publishCurrent = publication.CandidateVersion > publication.CurrentVersion
			if publishCurrent {
				if err := tx.Exec(`
UPDATE knowledge_document_versions
SET is_current = false, status = 'retired'
WHERE document_id = ? AND is_current = true AND id <> ?`,
					publication.DocumentID, lease.DocumentVersionID).Error; err != nil {
					return TranslateError(err)
				}
			}
		}
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = ?, stage = 'completed', checkpoint = ?::jsonb, progress_percent = 100,
    claim_owner = NULL, claimed_at = NULL, lease_until = NULL, heartbeat_at = NULL,
    last_error_code = NULL, last_error_message = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'running'
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?`,
			taskStatus, string(result.Checkpoint), completedAt.UTC(), completedAt.UTC(),
			lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner, lease.AttemptCount, completedAt.UTC())
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return knowledgeworker.ErrLeaseLost
		}
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET status = ?, is_current = ?, parser_version = ?, parser_metadata = ?::jsonb,
    completed_at = ?, error_code = NULL, error_message = NULL
WHERE id = ?`, versionStatus, publishCurrent, parserVersion, string(result.ParserMetadata),
			completedAt.UTC(), lease.DocumentVersionID).Error; err != nil {
			return TranslateError(err)
		}
		if err := appendKnowledgeIngestionEvent(tx, lease.TaskID, "ingestion_completed", map[string]any{
			"taskId": lease.TaskID.String(), "documentVersionId": lease.DocumentVersionID.String(),
			"status": string(taskStatus), "versionStatus": versionStatus,
			"attemptCount": lease.AttemptCount, "publishedCurrent": publishCurrent,
		}, completedAt); err != nil {
			return err
		}
		completed = true
		return nil
	})
	if errors.Is(err, knowledgeworker.ErrLeaseLost) {
		return false, nil
	}
	return completed, TranslateError(err)
}

func (r *KnowledgeWorkerRepository) ReleaseForRetry(
	ctx context.Context,
	lease knowledgeworker.Lease,
	code, message string,
	releasedAt, availableAt time.Time,
) (bool, error) {
	if !availableAt.After(releasedAt) {
		return false, errors.New("knowledge ingestion retry time is invalid")
	}
	return r.finishAttempt(ctx, lease, knowledge.IngestionRetryWait, code, message, releasedAt, availableAt)
}

func (r *KnowledgeWorkerRepository) Fail(
	ctx context.Context,
	lease knowledgeworker.Lease,
	code, message string,
	failedAt time.Time,
) (bool, error) {
	return r.finishAttempt(ctx, lease, knowledge.IngestionFailed, code, message, failedAt, failedAt)
}

func (r *KnowledgeWorkerRepository) finishAttempt(
	ctx context.Context,
	lease knowledgeworker.Lease,
	status knowledge.IngestionTaskStatus,
	code, message string,
	finishedAt, availableAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return false, err
	}
	code, message = strings.TrimSpace(code), truncateKnowledgeWorkerValue(message, 1000)
	if code == "" || len(code) > 128 || message == "" {
		return false, errors.New("knowledge ingestion failure is invalid")
	}
	changed := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedKnowledgeTask(tx, lease, finishedAt)
		if err != nil || !owned {
			return err
		}
		completedAt := any(nil)
		eventType := "ingestion_retry_scheduled"
		if status == knowledge.IngestionFailed {
			completedAt, eventType = finishedAt.UTC(), "ingestion_failed"
		}
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = ?, claim_owner = NULL, claimed_at = NULL, lease_until = NULL,
    heartbeat_at = NULL, available_at = ?, last_error_code = ?, last_error_message = ?,
    completed_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'running'
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?`,
			status, availableAt.UTC(), code, message, completedAt, finishedAt.UTC(),
			lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner, lease.AttemptCount, finishedAt.UTC())
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return knowledgeworker.ErrLeaseLost
		}
		if status == knowledge.IngestionFailed {
			if err := tx.Exec(`
UPDATE knowledge_document_versions
SET status = 'failed', completed_at = ?, error_code = ?, error_message = ?
WHERE id = ?`, finishedAt.UTC(), code, message, lease.DocumentVersionID).Error; err != nil {
				return TranslateError(err)
			}
		}
		if err := appendKnowledgeIngestionEvent(tx, lease.TaskID, eventType, map[string]any{
			"taskId": lease.TaskID.String(), "documentVersionId": lease.DocumentVersionID.String(),
			"status": string(status), "attemptCount": lease.AttemptCount, "errorCode": code,
			"availableAt": availableAt.UTC().Format(time.RFC3339Nano),
		}, finishedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if errors.Is(err, knowledgeworker.ErrLeaseLost) {
		return false, nil
	}
	return changed, TranslateError(err)
}

func (r *KnowledgeWorkerRepository) FinalizeCancellation(
	ctx context.Context,
	lease knowledgeworker.Lease,
	completedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("knowledge worker repository is unavailable")
	}
	if err := validateKnowledgeLease(lease); err != nil {
		return false, err
	}
	changed := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var owned struct {
			ID uuid.UUID `gorm:"column:id"`
		}
		result := tx.Raw(`
SELECT id FROM knowledge_ingestion_tasks
WHERE id = ? AND document_version_id = ? AND status = 'cancel_requested'
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?
FOR UPDATE`, lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner,
			lease.AttemptCount, completedAt.UTC()).Scan(&owned)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		if result.RowsAffected != 1 {
			return knowledgeworker.ErrLeaseLost
		}
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = 'cancelled', stage = 'completed', claim_owner = NULL, claimed_at = NULL,
    lease_until = NULL, heartbeat_at = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'cancel_requested'`,
			completedAt.UTC(), completedAt.UTC(), lease.TaskID, lease.DocumentVersionID)
		if updated.Error != nil || updated.RowsAffected != 1 {
			if updated.Error != nil {
				return TranslateError(updated.Error)
			}
			return knowledgeworker.ErrLeaseLost
		}
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET status = 'cancelled', completed_at = ?, error_code = NULL, error_message = NULL
WHERE id = ?`, completedAt.UTC(), lease.DocumentVersionID).Error; err != nil {
			return TranslateError(err)
		}
		if err := appendKnowledgeIngestionEvent(tx, lease.TaskID, "ingestion_cancelled", map[string]any{
			"taskId": lease.TaskID.String(), "documentVersionId": lease.DocumentVersionID.String(),
			"status": string(knowledge.IngestionCancelled), "attemptCount": lease.AttemptCount,
		}, completedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if errors.Is(err, knowledgeworker.ErrLeaseLost) {
		return false, nil
	}
	return changed, TranslateError(err)
}

type knowledgeTaskState struct {
	ID                uuid.UUID
	DocumentVersionID uuid.UUID
	Status            knowledge.IngestionTaskStatus
	AttemptCount      int
	MaxAttempts       int
	AvailableAt       time.Time
	LeaseUntil        *time.Time
}

func selectKnowledgeTaskForUpdate(db *gorm.DB, taskID, versionID uuid.UUID) (knowledgeTaskState, error) {
	var state knowledgeTaskState
	result := db.Raw(`
SELECT id, document_version_id, status, attempt_count, max_attempts, available_at, lease_until
FROM knowledge_ingestion_tasks
WHERE id = ? AND document_version_id = ?
FOR UPDATE`, taskID, versionID).Scan(&state)
	if result.Error != nil {
		return knowledgeTaskState{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledgeTaskState{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return state, nil
}

func lockOwnedKnowledgeTask(db *gorm.DB, lease knowledgeworker.Lease, now time.Time) (bool, error) {
	var owned struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	result := db.Raw(`
SELECT id FROM knowledge_ingestion_tasks
WHERE id = ? AND document_version_id = ? AND status = 'running'
  AND claim_owner = ? AND attempt_count = ? AND lease_until > ?
FOR UPDATE`, lease.TaskID, lease.DocumentVersionID, lease.ClaimOwner,
		lease.AttemptCount, now.UTC()).Scan(&owned)
	if result.Error != nil {
		return false, TranslateError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func appendKnowledgeIngestionEvent(
	db *gorm.DB,
	taskID uuid.UUID,
	eventType string,
	payload map[string]any,
	createdAt time.Time,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return TranslateError(db.Exec(`
INSERT INTO knowledge_ingestion_events
    (task_id, seq, event_type, payload, payload_schema_version, created_at)
SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?::jsonb, 1, ?
FROM knowledge_ingestion_events
WHERE task_id = ?`, taskID, eventType, string(encoded), createdAt.UTC(), taskID).Error)
}

func failExpiredKnowledgeTask(db *gorm.DB, state knowledgeTaskState, failedAt time.Time) error {
	const code = "ingestion_lease_expired_after_retry_budget"
	const message = "knowledge ingestion lease expired after the retry budget was exhausted"
	if err := db.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = 'failed', claim_owner = NULL, claimed_at = NULL, lease_until = NULL,
    heartbeat_at = NULL, last_error_code = ?, last_error_message = ?, completed_at = ?, updated_at = ?
WHERE id = ? AND document_version_id = ? AND status = 'running'`,
		code, message, failedAt.UTC(), failedAt.UTC(), state.ID, state.DocumentVersionID).Error; err != nil {
		return TranslateError(err)
	}
	if err := db.Exec(`
UPDATE knowledge_document_versions
SET status = 'failed', completed_at = ?, error_code = ?, error_message = ?
WHERE id = ?`, failedAt.UTC(), code, message, state.DocumentVersionID).Error; err != nil {
		return TranslateError(err)
	}
	return appendKnowledgeIngestionEvent(db, state.ID, "ingestion_failed", map[string]any{
		"taskId": state.ID.String(), "documentVersionId": state.DocumentVersionID.String(),
		"status": string(knowledge.IngestionFailed), "attemptCount": state.AttemptCount,
		"errorCode": code,
	}, failedAt)
}

func validateKnowledgeLease(lease knowledgeworker.Lease) error {
	if lease.TaskID == uuid.Nil || lease.DocumentVersionID == uuid.Nil ||
		strings.TrimSpace(lease.ClaimOwner) == "" || len(lease.ClaimOwner) > 128 ||
		lease.AttemptCount < 0 || lease.MaxAttempts < 1 || lease.AttemptCount > lease.MaxAttempts {
		return errors.New("knowledge worker lease is invalid")
	}
	return nil
}

func validateParsedExecutionResult(result knowledgeworker.ExecutionResult) error {
	if strings.TrimSpace(result.ParserVersion) == "" || len(result.ParserVersion) > 128 ||
		!validJSONObject(result.ParserMetadata) || !validJSONObject(result.Checkpoint) {
		return errors.New("knowledge parsed result metadata is invalid")
	}
	if result.Artifact.Bucket != objectstore.BucketKnowledgeArtifacts {
		return errors.New("knowledge parsed result artifact bucket is invalid")
	}
	if err := result.Artifact.Validate(); err != nil {
		return err
	}
	if len(result.Chunks) == 0 || len(result.Chunks) > 10000 {
		return errors.New("knowledge parsed result chunks are required and bounded")
	}
	for _, chunk := range result.Chunks {
		if err := chunk.Validate(); err != nil {
			return err
		}
	}
	if result.EmbeddingProfile == nil {
		if len(result.Embeddings) != 0 || result.EmbeddingUsage.TotalTokens != 0 {
			return errors.New("knowledge parsed result has embeddings without a profile")
		}
		return nil
	}
	if err := result.EmbeddingProfile.Validate(); err != nil {
		return err
	}
	if len(result.Embeddings) != len(result.Chunks) || result.EmbeddingUsage.TotalTokens < 0 {
		return errors.New("knowledge parsed result embedding count is invalid")
	}
	for ordinal, embedding := range result.Embeddings {
		if embedding.ChunkOrdinal != ordinal || embedding.ContentSHA256 != result.Chunks[ordinal].ContentSHA256 {
			return errors.New("knowledge parsed result embedding does not match its chunk")
		}
		if err := embedding.Validate(*result.EmbeddingProfile); err != nil {
			return err
		}
	}
	return nil
}

// documentVersionStatusForStage projects the fine-grained task stage onto the
// knowledge_document_versions lifecycle. Publishing remains non-terminal until
// Complete atomically promotes the version to ready or partial_ready.
func documentVersionStatusForStage(stage knowledge.IngestionStage) string {
	switch stage {
	case knowledge.IngestionStageScanning:
		return "scanning"
	case knowledge.IngestionStageParsing:
		return "parsing"
	case knowledge.IngestionStageChunking:
		return "chunking"
	case knowledge.IngestionStageIndexing:
		return "indexing"
	case knowledge.IngestionStagePublishing:
		return "publishing"
	default:
		return "processing"
	}
}

func validJSONObject(raw json.RawMessage) bool {
	var object map[string]any
	return len(raw) > 0 && json.Unmarshal(raw, &object) == nil && object != nil
}

func truncateKnowledgeWorkerValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "?")
}
