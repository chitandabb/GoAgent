package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

type KnowledgeRepository struct {
	db *gorm.DB
}

type knowledgeSearchRow struct {
	DocumentID        uuid.UUID
	DocumentVersionID uuid.UUID
	ChunkID           uuid.UUID
	Title             string
	Scope             knowledge.Scope
	Ordinal           int
	PageNumber        *int
	ElementType       knowledge.ElementType
	SectionPathJSON   string
	ContentText       string
	ContentSHA256     string
	Score             float64
}

func NewKnowledgeRepository(db *gorm.DB) *KnowledgeRepository {
	return &KnowledgeRepository{db: db}
}

var _ knowledge.Repository = (*KnowledgeRepository)(nil)
var _ knowledge.IngestionRepository = (*KnowledgeRepository)(nil)
var _ knowledge.IngestionTaskControlRepository = (*KnowledgeRepository)(nil)

func (r *KnowledgeRepository) CreateDocument(
	ctx context.Context,
	input knowledge.CreateDocumentInput,
) (knowledge.Document, error) {
	if r == nil || r.db == nil {
		return knowledge.Document{}, errors.New("knowledge repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return knowledge.Document{}, err
	}
	createdAt := time.Now().UTC()
	query := `
INSERT INTO knowledge_documents
    (id, scope, owner_user_id, title, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	if err := ResolveDB(ctx, r.db).Exec(
		query, input.ID, input.Scope, input.OwnerUserID, input.Title, input.CreatedBy, createdAt, createdAt,
	).Error; err != nil {
		return knowledge.Document{}, fmt.Errorf("create knowledge document: %w", TranslateError(err))
	}
	return knowledge.Document{
		ID: input.ID, Scope: input.Scope, OwnerUserID: input.OwnerUserID,
		Title: input.Title, CreatedBy: input.CreatedBy, CreatedAt: createdAt,
	}, nil
}

func (r *KnowledgeRepository) PublishVersion(
	ctx context.Context,
	input knowledge.PublishVersionInput,
) (knowledge.DocumentVersion, error) {
	if r == nil || r.db == nil {
		return knowledge.DocumentVersion{}, errors.New("knowledge repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return knowledge.DocumentVersion{}, err
	}
	parserMetadata := input.ParserMetadata
	if len(parserMetadata) == 0 {
		parserMetadata = json.RawMessage(`{}`)
	}
	completedAt := time.Now().UTC()
	var published knowledge.DocumentVersion
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var documentScope knowledge.Scope
		if err := tx.Raw(
			"SELECT scope FROM knowledge_documents WHERE id = ? AND deleted_at IS NULL FOR UPDATE",
			input.DocumentID,
		).Scan(&documentScope).Error; err != nil {
			return err
		}
		if documentScope != knowledge.ScopeGlobal && documentScope != knowledge.ScopePersonal {
			return gorm.ErrRecordNotFound
		}

		var version int
		if err := tx.Raw(
			"SELECT COALESCE(MAX(version), 0) + 1 FROM knowledge_document_versions WHERE document_id = ?",
			input.DocumentID,
		).Scan(&version).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
UPDATE knowledge_document_versions
SET is_current = false, status = 'retired'
WHERE document_id = ? AND is_current = true`, input.DocumentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO knowledge_document_versions
    (id, document_id, version, status, is_current, source_media_type,
     source_size_bytes, source_sha256, parser_version, parser_metadata,
     created_by, completed_at, created_at)
VALUES (?, ?, ?, 'ready', true, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`,
			input.ID, input.DocumentID, version, input.SourceMediaType,
			input.SourceSizeBytes, input.SourceSHA256, input.ParserVersion,
			string(parserMetadata), input.CreatedBy, completedAt, completedAt,
		).Error; err != nil {
			return err
		}
		for ordinal, chunk := range input.Chunks {
			sectionValues := chunk.SectionPath
			if sectionValues == nil {
				sectionValues = []string{}
			}
			sectionPath, err := json.Marshal(sectionValues)
			if err != nil {
				return err
			}
			metadata := chunk.Metadata
			if len(metadata) == 0 {
				metadata = json.RawMessage(`{}`)
			}
			if err := tx.Exec(`
INSERT INTO knowledge_chunks
    (id, document_version_id, ordinal, page_number, element_index, element_type,
     section_path, content_text, search_text, content_sha256, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?::jsonb)`,
				uuid.New(), input.ID, ordinal, chunk.PageNumber, chunk.ElementIndex,
				chunk.ElementType, string(sectionPath), chunk.ContentText, chunk.SearchText,
				chunk.ContentSHA256, string(metadata),
			).Error; err != nil {
				return err
			}
		}
		if documentScope == knowledge.ScopeGlobal {
			if err := incrementGlobalKnowledgeGeneration(tx, completedAt); err != nil {
				return err
			}
		}
		published = knowledge.DocumentVersion{
			ID: input.ID, DocumentID: input.DocumentID, Version: version, CreatedAt: completedAt,
		}
		return nil
	})
	if err != nil {
		return knowledge.DocumentVersion{}, fmt.Errorf("publish knowledge document version: %w", TranslateError(err))
	}
	return published, nil
}

func (r *KnowledgeRepository) QueueVersion(
	ctx context.Context,
	input knowledge.QueueVersionInput,
) (knowledge.QueueVersionResult, error) {
	if r == nil || r.db == nil {
		return knowledge.QueueVersionResult{}, errors.New("knowledge repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return knowledge.QueueVersionResult{}, err
	}
	createdAt := input.CreatedAt.UTC()
	var queued knowledge.QueueVersionResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		if input.NewDocument != nil {
			if err := tx.Exec(`
INSERT INTO knowledge_documents
    (id, scope, owner_user_id, title, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
				input.NewDocument.ID, input.NewDocument.Scope, input.NewDocument.OwnerUserID,
				input.NewDocument.Title, input.NewDocument.CreatedBy, createdAt, createdAt,
			).Error; err != nil {
				return err
			}
		} else {
			var documentExists bool
			if err := tx.Raw(
				"SELECT true FROM knowledge_documents WHERE id = ? AND scope = 'global' AND deleted_at IS NULL FOR UPDATE",
				input.DocumentID,
			).Scan(&documentExists).Error; err != nil {
				return err
			}
			if !documentExists {
				return gorm.ErrRecordNotFound
			}
		}

		var version int
		if err := tx.Raw(
			"SELECT COALESCE(MAX(version), 0) + 1 FROM knowledge_document_versions WHERE document_id = ?",
			input.DocumentID,
		).Scan(&version).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO knowledge_document_versions
    (id, document_id, version, status, is_current, source_media_type,
     source_size_bytes, source_sha256, source_bucket, source_object_key,
     source_object_version, source_etag, source_original_name, source_uploaded_at,
     pipeline_version, parser_version, parser_metadata, created_by, created_at)
VALUES (?, ?, ?, 'queued', false, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '{}'::jsonb, ?, ?)`,
			input.VersionID, input.DocumentID, version, input.Source.MediaType,
			input.Source.SizeBytes, input.Source.SHA256, input.Source.Bucket,
			input.Source.ObjectKey, nullableTrimmedString(input.Source.VersionID), input.Source.ETag,
			input.Source.OriginalName, createdAt, input.PipelineVersion,
			input.CreatedBy, createdAt,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO knowledge_ingestion_tasks
    (id, document_version_id, status, stage, attempt_count, max_attempts,
     available_at, checkpoint, progress_percent, created_by, idempotency_key,
     request_fingerprint, created_at, updated_at)
VALUES (?, ?, 'pending', 'uploaded', 0, ?, ?, '{}'::jsonb, 0, ?, ?, ?, ?, ?)`,
			input.TaskID, input.VersionID, input.MaxAttempts, createdAt,
			input.CreatedBy, input.IdempotencyKey, input.RequestFingerprint, createdAt, createdAt,
		).Error; err != nil {
			return err
		}
		eventPayload, err := json.Marshal(map[string]any{
			"taskId": input.TaskID.String(), "documentVersionId": input.VersionID.String(),
			"status": string(knowledge.IngestionPending), "stage": string(knowledge.IngestionStageUploaded),
		})
		if err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO knowledge_ingestion_events
    (task_id, seq, event_type, payload, payload_schema_version, created_at)
VALUES (?, 1, 'ingestion_queued', ?, 1, ?)`, input.TaskID, eventPayload, createdAt).Error; err != nil {
			return err
		}
		outboxPayload, err := json.Marshal(map[string]any{
			"taskId": input.TaskID.String(), "documentVersionId": input.VersionID.String(),
		})
		if err != nil {
			return err
		}
		if err := tx.Exec(`
INSERT INTO outbox_events
    (id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
     payload, payload_schema_version, attempt_count, available_at, requeue_count, created_at)
VALUES (?, 'knowledge.ingest', 'knowledge_ingestion_task', ?, ?, NULL, ?, 1, 0, ?, 0, ?)`,
			input.OutboxEventID, input.TaskID, input.CorrelationID,
			outboxPayload, createdAt, createdAt,
		).Error; err != nil {
			return err
		}
		queued = knowledge.QueueVersionResult{
			Version: knowledge.DocumentVersion{
				ID: input.VersionID, DocumentID: input.DocumentID,
				Version: version, CreatedAt: createdAt,
			},
			Task: knowledge.IngestionTask{
				ID: input.TaskID, DocumentVersionID: input.VersionID,
				Status: knowledge.IngestionPending, Stage: knowledge.IngestionStageUploaded,
				AttemptCount: 0, MaxAttempts: input.MaxAttempts, CreatedAt: createdAt,
			},
		}
		return nil
	})
	if err != nil {
		return knowledge.QueueVersionResult{}, fmt.Errorf("queue knowledge document version: %w", TranslateError(err))
	}
	return queued, nil
}

func nullableTrimmedString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func (r *KnowledgeRepository) FindQueuedVersionByIdempotency(
	ctx context.Context,
	createdBy uuid.UUID,
	idempotencyKey string,
) (knowledge.QueueVersionResult, string, error) {
	if r == nil || r.db == nil {
		return knowledge.QueueVersionResult{}, "", errors.New("knowledge repository is unavailable")
	}
	if createdBy == uuid.Nil {
		return knowledge.QueueVersionResult{}, "", errors.New("knowledge ingestion creator is required")
	}
	if _, err := uuid.Parse(strings.TrimSpace(idempotencyKey)); err != nil {
		return knowledge.QueueVersionResult{}, "", errors.New("knowledge ingestion idempotency key must be a UUID")
	}
	type row struct {
		VersionID          uuid.UUID
		DocumentID         uuid.UUID
		Version            int
		VersionCreatedAt   time.Time
		TaskID             uuid.UUID
		Status             knowledge.IngestionTaskStatus
		Stage              knowledge.IngestionStage
		AttemptCount       int
		MaxAttempts        int
		TaskCreatedAt      time.Time
		RequestFingerprint string
	}
	var found row
	result := ResolveDB(ctx, r.db).Raw(`
SELECT version.id AS version_id,
       version.document_id,
       version.version,
       version.created_at AS version_created_at,
       task.id AS task_id,
       task.status,
       task.stage,
       task.attempt_count,
       task.max_attempts,
       task.created_at AS task_created_at,
       task.request_fingerprint
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
WHERE task.created_by = ? AND task.idempotency_key = ?`, createdBy, idempotencyKey).Scan(&found)
	if result.Error != nil {
		return knowledge.QueueVersionResult{}, "", TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledge.QueueVersionResult{}, "", repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return knowledge.QueueVersionResult{
		Version: knowledge.DocumentVersion{
			ID: found.VersionID, DocumentID: found.DocumentID,
			Version: found.Version, CreatedAt: found.VersionCreatedAt,
		},
		Task: knowledge.IngestionTask{
			ID: found.TaskID, DocumentVersionID: found.VersionID,
			Status: found.Status, Stage: found.Stage, AttemptCount: found.AttemptCount,
			MaxAttempts: found.MaxAttempts, CreatedAt: found.TaskCreatedAt,
		},
	}, found.RequestFingerprint, nil
}

func (r *KnowledgeRepository) FindIngestionTask(
	ctx context.Context,
	taskID uuid.UUID,
) (knowledge.IngestionTaskDetail, error) {
	if r == nil || r.db == nil {
		return knowledge.IngestionTaskDetail{}, errors.New("knowledge repository is unavailable")
	}
	if taskID == uuid.Nil {
		return knowledge.IngestionTaskDetail{}, errors.New("knowledge ingestion task id is required")
	}
	var detail knowledge.IngestionTaskDetail
	result := ResolveDB(ctx, r.db).Raw(`
SELECT task.id, task.document_version_id, version.document_id, task.status, task.stage,
       task.attempt_count, task.max_attempts, task.progress_percent,
       task.cancel_requested_at, COALESCE(task.last_error_code, '') AS last_error_code,
       COALESCE(task.last_error_message, '') AS last_error_message,
       task.started_at, task.completed_at, task.created_at, task.updated_at
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
WHERE task.id = ?`, taskID).Scan(&detail)
	if result.Error != nil {
		return knowledge.IngestionTaskDetail{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledge.IngestionTaskDetail{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	if err := detail.Validate(); err != nil {
		return knowledge.IngestionTaskDetail{}, err
	}
	return detail, nil
}

func (r *KnowledgeRepository) RequestIngestionCancellation(
	ctx context.Context,
	taskID, requestedBy uuid.UUID,
	requestedAt time.Time,
) (knowledge.IngestionCancelResult, error) {
	if r == nil || r.db == nil {
		return knowledge.IngestionCancelResult{}, errors.New("knowledge repository is unavailable")
	}
	if taskID == uuid.Nil || requestedBy == uuid.Nil {
		return knowledge.IngestionCancelResult{}, errors.New("knowledge ingestion cancellation is invalid")
	}
	requestedAt = requestedAt.UTC()
	var cancelled knowledge.IngestionCancelResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var state struct {
			Status knowledge.IngestionTaskStatus `gorm:"column:status"`
		}
		result := tx.Raw(`
SELECT status FROM knowledge_ingestion_tasks WHERE id = ? FOR UPDATE`, taskID).Scan(&state)
		if result.Error != nil {
			return TranslateError(result.Error)
		}
		if result.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		switch state.Status {
		case knowledge.IngestionCancelRequested, knowledge.IngestionCancelled:
			detail, err := findIngestionTaskWithDB(tx, taskID)
			if err != nil {
				return err
			}
			cancelled = knowledge.IngestionCancelResult{Task: detail, Changed: false}
			return nil
		case knowledge.IngestionPending, knowledge.IngestionRunning, knowledge.IngestionRetryWait:
		case knowledge.IngestionSucceeded, knowledge.IngestionPartialSucceeded, knowledge.IngestionFailed:
			return knowledge.ErrIngestionTaskStateConflict
		default:
			return fmt.Errorf("unsupported knowledge ingestion task status %q", state.Status)
		}
		updated := tx.Exec(`
UPDATE knowledge_ingestion_tasks
SET status = 'cancel_requested', cancel_requested_at = ?, updated_at = ?
WHERE id = ? AND status = ?`, requestedAt, requestedAt, taskID, state.Status)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return knowledge.ErrIngestionTaskStateConflict
		}
		if err := appendKnowledgeIngestionEvent(tx, taskID, "ingestion_cancel_requested", map[string]any{
			"taskId": taskID.String(), "status": string(knowledge.IngestionCancelRequested),
			"requestedBy": requestedBy.String(),
		}, requestedAt); err != nil {
			return err
		}
		detail, err := findIngestionTaskWithDB(tx, taskID)
		if err != nil {
			return err
		}
		cancelled = knowledge.IngestionCancelResult{Task: detail, Changed: true}
		return nil
	})
	if err != nil {
		return knowledge.IngestionCancelResult{}, TranslateError(err)
	}
	return cancelled, nil
}

func findIngestionTaskWithDB(db *gorm.DB, taskID uuid.UUID) (knowledge.IngestionTaskDetail, error) {
	var detail knowledge.IngestionTaskDetail
	result := db.Raw(`
SELECT task.id, task.document_version_id, version.document_id, task.status, task.stage,
       task.attempt_count, task.max_attempts, task.progress_percent,
       task.cancel_requested_at, COALESCE(task.last_error_code, '') AS last_error_code,
       COALESCE(task.last_error_message, '') AS last_error_message,
       task.started_at, task.completed_at, task.created_at, task.updated_at
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
WHERE task.id = ?`, taskID).Scan(&detail)
	if result.Error != nil {
		return knowledge.IngestionTaskDetail{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return knowledge.IngestionTaskDetail{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	if err := detail.Validate(); err != nil {
		return knowledge.IngestionTaskDetail{}, err
	}
	return detail, nil
}

func (r *KnowledgeRepository) SearchFTS(
	ctx context.Context,
	actorID uuid.UUID,
	queryText string,
	limit int,
) ([]knowledge.SearchResult, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("knowledge repository is unavailable")
	}
	if actorID == uuid.Nil {
		return nil, errors.New("knowledge search actor is required")
	}
	if len([]rune(queryText)) > 512 {
		return nil, errors.New("knowledge search query is too long")
	}
	tsQuery, err := knowledge.BuildTSQuery(strings.TrimSpace(queryText))
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	var rows []knowledgeSearchRow
	searchSQL := `
WITH search_query AS (
    SELECT to_tsquery('simple', ?) AS query
)
SELECT d.id AS document_id, v.id AS document_version_id, c.id AS chunk_id,
       d.title, d.scope, c.ordinal, c.page_number, c.element_type,
	   c.section_path::text AS section_path_json, c.content_text, c.content_sha256,
       ts_rank_cd(c.search_vector, q.query) AS score
FROM knowledge_chunks AS c
JOIN knowledge_document_versions AS v ON v.id = c.document_version_id
JOIN knowledge_documents AS d ON d.id = v.document_id
CROSS JOIN search_query AS q
WHERE d.deleted_at IS NULL
  AND v.status IN ('ready', 'partial_ready')
  AND v.is_current = true
  AND (d.scope = 'global' OR (d.scope = 'personal' AND d.owner_user_id = ?))
  AND c.search_vector @@ q.query
ORDER BY score DESC, d.id, c.ordinal
LIMIT ?`
	if err := ResolveDB(ctx, r.db).Raw(searchSQL, tsQuery, actorID, limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search knowledge chunks: %w", TranslateError(err))
	}
	return mapKnowledgeSearchRows(rows)
}

func (r *KnowledgeRepository) SearchVector(
	ctx context.Context,
	actorID, profileID uuid.UUID,
	queryVector []float32,
	limit int,
) ([]knowledge.SearchResult, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("knowledge repository is unavailable")
	}
	if actorID == uuid.Nil || profileID == uuid.Nil {
		return nil, errors.New("knowledge vector search actor and profile are required")
	}
	if err := knowledge.ValidateEmbeddingVector(queryVector, 1024, true); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	vector := pgvector.NewVector(queryVector)
	var rows []knowledgeSearchRow
	searchSQL := `
SELECT d.id AS document_id, v.id AS document_version_id, c.id AS chunk_id,
       d.title, d.scope, c.ordinal, c.page_number, c.element_type,
       c.section_path::text AS section_path_json, c.content_text, c.content_sha256,
       1 - (embedding.embedding <=> ?) AS score
FROM knowledge_chunk_embeddings AS embedding
JOIN knowledge_embedding_profiles AS profile
  ON profile.id = embedding.profile_id AND profile.status = 'active'
JOIN knowledge_chunks AS c ON c.id = embedding.chunk_id
JOIN knowledge_document_versions AS v ON v.id = c.document_version_id
JOIN knowledge_documents AS d ON d.id = v.document_id
WHERE embedding.profile_id = ?
  AND embedding.content_sha256 = c.content_sha256
  AND d.deleted_at IS NULL
  AND v.status IN ('ready', 'partial_ready')
  AND v.is_current = true
  AND (d.scope = 'global' OR (d.scope = 'personal' AND d.owner_user_id = ?))
ORDER BY score DESC, d.id, c.ordinal
LIMIT ?`
	if err := ResolveDB(ctx, r.db).Raw(searchSQL, vector, profileID, actorID, limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("search knowledge chunk vectors: %w", TranslateError(err))
	}
	return mapKnowledgeSearchRows(rows)
}

func (r *KnowledgeRepository) ExpandContext(
	ctx context.Context,
	actorID uuid.UUID,
	hits []knowledge.SearchResult,
	window, maxRunes int,
) ([]knowledge.SearchContextGroup, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("knowledge repository is unavailable")
	}
	if actorID == uuid.Nil || len(hits) == 0 || len(hits) > knowledge.MaxKnowledgeSearchLimit ||
		window < 1 || window > 3 || maxRunes < 128 || maxRunes > 8000 {
		return nil, errors.New("knowledge context expansion request is invalid")
	}

	type groupKey struct {
		documentID uuid.UUID
		versionID  uuid.UUID
		section    string
	}
	type groupBuilder struct {
		group       knowledge.SearchContextGroup
		hitOrdinals []int
		hitSet      map[uuid.UUID]struct{}
		candidates  map[uuid.UUID]knowledge.SearchContextChunk
		truncated   bool
	}
	builders := make(map[groupKey]*groupBuilder, len(hits))
	order := make([]groupKey, 0, len(hits))
	clauses := make([]string, 0, len(hits))
	args := make([]any, 0, 1+len(hits)*4)
	args = append(args, actorID)
	for _, hit := range hits {
		if err := hit.Validate(); err != nil {
			return nil, errors.New("knowledge context expansion hit is invalid")
		}
		sectionJSON, err := json.Marshal(hit.SectionPath)
		if err != nil {
			return nil, errors.New("encode knowledge context section path")
		}
		key := groupKey{documentID: hit.DocumentID, versionID: hit.DocumentVersionID, section: string(sectionJSON)}
		builder := builders[key]
		if builder == nil {
			builder = &groupBuilder{
				group: knowledge.SearchContextGroup{
					DocumentID: hit.DocumentID, DocumentVersionID: hit.DocumentVersionID,
					SectionPath: append([]string(nil), hit.SectionPath...),
				},
				hitSet: make(map[uuid.UUID]struct{}), candidates: make(map[uuid.UUID]knowledge.SearchContextChunk),
			}
			builders[key] = builder
			order = append(order, key)
		}
		if _, exists := builder.hitSet[hit.ChunkID]; !exists {
			builder.hitSet[hit.ChunkID] = struct{}{}
			builder.group.HitChunkIDs = append(builder.group.HitChunkIDs, hit.ChunkID)
			builder.hitOrdinals = append(builder.hitOrdinals, hit.Ordinal)
		}
		lowerOrdinal := hit.Ordinal - window
		if lowerOrdinal < 0 {
			lowerOrdinal = 0
		}
		clauses = append(clauses, "(c.document_version_id = ? AND c.section_path = ?::jsonb AND c.ordinal BETWEEN ? AND ?)")
		args = append(args, hit.DocumentVersionID, string(sectionJSON), lowerOrdinal, hit.Ordinal+window)
	}

	query := `
SELECT d.id AS document_id, v.id AS document_version_id, c.id AS chunk_id,
       d.title, d.scope, c.ordinal, c.page_number, c.element_type,
       c.section_path::text AS section_path_json, c.content_text, c.content_sha256,
       0::double precision AS score
FROM knowledge_chunks AS c
JOIN knowledge_document_versions AS v ON v.id = c.document_version_id
JOIN knowledge_documents AS d ON d.id = v.document_id
WHERE d.deleted_at IS NULL
  AND v.status IN ('ready', 'partial_ready')
  AND v.is_current = true
  AND (d.scope = 'global' OR (d.scope = 'personal' AND d.owner_user_id = ?))
  AND (` + strings.Join(clauses, " OR ") + `)
ORDER BY d.id, v.id, c.ordinal, c.id`
	var rows []knowledgeSearchRow
	if err := ResolveDB(ctx, r.db).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("expand knowledge search context: %w", TranslateError(err))
	}
	for _, row := range rows {
		var sectionPath []string
		if err := json.Unmarshal([]byte(row.SectionPathJSON), &sectionPath); err != nil {
			return nil, fmt.Errorf("decode knowledge context section path: %w", err)
		}
		sectionJSON, err := json.Marshal(sectionPath)
		if err != nil {
			return nil, fmt.Errorf("encode knowledge context section path: %w", err)
		}
		key := groupKey{documentID: row.DocumentID, versionID: row.DocumentVersionID, section: string(sectionJSON)}
		builder := builders[key]
		if builder == nil {
			continue
		}
		if _, isHit := builder.hitSet[row.ChunkID]; isHit {
			continue
		}
		builder.candidates[row.ChunkID] = knowledge.SearchContextChunk{
			ChunkID: row.ChunkID, Ordinal: row.Ordinal, PageNumber: row.PageNumber,
			ElementType: row.ElementType, ContentText: row.ContentText, ContentSHA256: row.ContentSHA256,
		}
	}

	groups := make([]knowledge.SearchContextGroup, 0, len(order))
	for _, key := range order {
		builder := builders[key]
		candidates := make([]knowledge.SearchContextChunk, 0, len(builder.candidates))
		for _, candidate := range builder.candidates {
			candidates = append(candidates, candidate)
		}
		sort.SliceStable(candidates, func(left, right int) bool {
			leftDistance := nearestOrdinalDistance(candidates[left].Ordinal, builder.hitOrdinals)
			rightDistance := nearestOrdinalDistance(candidates[right].Ordinal, builder.hitOrdinals)
			if leftDistance != rightDistance {
				return leftDistance < rightDistance
			}
			if candidates[left].Ordinal != candidates[right].Ordinal {
				return candidates[left].Ordinal < candidates[right].Ordinal
			}
			return candidates[left].ChunkID.String() < candidates[right].ChunkID.String()
		})
		usedRunes := 0
		selected := make([]knowledge.SearchContextChunk, 0, len(candidates))
		for _, candidate := range candidates {
			candidateRunes := len([]rune(candidate.ContentText))
			if usedRunes+candidateRunes > maxRunes {
				builder.truncated = true
				continue
			}
			usedRunes += candidateRunes
			selected = append(selected, candidate)
		}
		if len(selected) == 0 {
			continue
		}
		sort.SliceStable(selected, func(left, right int) bool {
			if selected[left].Ordinal != selected[right].Ordinal {
				return selected[left].Ordinal < selected[right].Ordinal
			}
			return selected[left].ChunkID.String() < selected[right].ChunkID.String()
		})
		builder.group.Chunks = selected
		builder.group.Truncated = builder.truncated
		if err := builder.group.Validate(hits); err != nil {
			return nil, err
		}
		groups = append(groups, builder.group)
	}
	return groups, nil
}

func nearestOrdinalDistance(ordinal int, hits []int) int {
	best := int(^uint(0) >> 1)
	for _, hit := range hits {
		distance := ordinal - hit
		if distance < 0 {
			distance = -distance
		}
		if distance < best {
			best = distance
		}
	}
	return best
}

func mapKnowledgeSearchRows(rows []knowledgeSearchRow) ([]knowledge.SearchResult, error) {
	results := make([]knowledge.SearchResult, 0, len(rows))
	for _, row := range rows {
		var sectionPath []string
		if err := json.Unmarshal([]byte(row.SectionPathJSON), &sectionPath); err != nil {
			return nil, fmt.Errorf("decode knowledge section path: %w", err)
		}
		results = append(results, knowledge.SearchResult{
			DocumentID: row.DocumentID, DocumentVersionID: row.DocumentVersionID,
			ChunkID: row.ChunkID, Title: row.Title, Scope: row.Scope,
			Ordinal: row.Ordinal, PageNumber: row.PageNumber, ElementType: row.ElementType,
			SectionPath: sectionPath, ContentText: row.ContentText,
			ContentSHA256: row.ContentSHA256, Score: row.Score,
		})
	}
	return results, nil
}
