package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const databaseAblationVersion = "db-staging-ablation-v1"

type stagingDocument struct {
	definition corpusDocument
	result     knowledgeworker.ExecutionResult
	elements   int
}

type stagingTask struct {
	documentID uuid.UUID
	versionID  uuid.UUID
	taskID     uuid.UUID
	lease      knowledgeworker.Lease
	result     knowledgeworker.ExecutionResult
}

func runDatabaseAblation(
	ctx context.Context,
	cfg config.Config,
	datasetVersion string,
	documents []loadedDocument,
	options commandOptions,
	log *zap.Logger,
) error {
	if !cfg.Models.Embedding.Enabled {
		return errors.New("database ablation requires an enabled embedding profile but does not call its provider")
	}
	if cfg.Knowledge.ChunkWriteBatchSize == 1 {
		return errors.New("database ablation requires an experiment chunk write batch size greater than one")
	}
	runCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(runCtx, cfg.Postgres, log.Named("postgres"))
	if err != nil {
		return err
	}
	defer closeDB()

	actorID := uuid.New()
	username := "ingestion-db-ablation-" + strings.ReplaceAll(actorID.String(), "-", "")
	if err := db.WithContext(runCtx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, must_change_password)
VALUES (?, ?, 'Ingestion DB Ablation', 'evaluation-only-not-a-login-secret', 'admin', false)`,
		actorID, username,
	).Error; err != nil {
		return fmt.Errorf("create database ablation actor: %w", err)
	}
	defer cleanupEvaluationActor(db, actorID)

	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return err
	}
	if err := platformpostgres.NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(runCtx, profile); err != nil {
		return err
	}
	parser, err := buildParser(cfg.Knowledge)
	if err != nil {
		return err
	}
	prepared, err := prepareStagingDocuments(runCtx, parser, cfg, profile, documents)
	if err != nil {
		return err
	}

	ablationDataset := datasetVersion + "-" + databaseAblationVersion
	corpusHash := corpusFingerprint(ablationDataset, documents)
	environmentHash := knowledge.SHA256Hex(buildEnvironmentFingerprint(cfg, profile) + "|" + databaseAblationVersion)
	baseline := variantConfig{
		variant: knowledgeingestion.ThroughputBaseline, documentConcurrency: 1,
		embeddingBatchSize:     cfg.Models.Embedding.BatchSize,
		embeddingMaxConcurrent: cfg.Models.Embedding.MaxConcurrent, chunkWriteBatchSize: 1,
	}
	experiment := baseline
	experiment.variant = knowledgeingestion.ThroughputExperiment
	experiment.chunkWriteBatchSize = cfg.Knowledge.ChunkWriteBatchSize

	observations := make([]knowledgeingestion.ThroughputObservation, 0, options.repetitions*2)
	for repetition := 1; repetition <= options.repetitions; repetition++ {
		variants := []variantConfig{baseline, experiment}
		if repetition%2 == 0 {
			slices.Reverse(variants)
		}
		for _, variant := range variants {
			observation, runErr := runDatabaseStagingVariant(
				runCtx, db, cfg, ablationDataset, corpusHash, environmentHash,
				actorID, prepared, repetition, variant,
			)
			if runErr != nil {
				return fmt.Errorf("run database ablation repetition %d variant %s: %w", repetition, variant.variant, runErr)
			}
			observations = append(observations, observation)
			log.Info("knowledge ingestion database ablation variant completed",
				zap.Int("repetition", repetition), zap.String("variant", string(variant.variant)),
				zap.Int64("duration_ms", observation.DurationMillis),
				zap.Int("chunk_insert_batches", observation.ChunkInsertBatches),
				zap.Int("embedding_insert_batches", observation.EmbeddingInsertBatches))
		}
	}
	slices.SortFunc(observations, func(left, right knowledgeingestion.ThroughputObservation) int {
		if left.Repetition != right.Repetition {
			return left.Repetition - right.Repetition
		}
		return strings.Compare(string(left.Variant), string(right.Variant))
	})
	if err := writeObservations(options.outputPath, observations); err != nil {
		return err
	}
	summary, err := knowledgeingestion.EvaluateThroughput(observations, 40)
	if err != nil {
		return err
	}
	fmt.Printf(
		"dataset=%s pairs=%d database_only=true median_staging_throughput_increase=%.2f%% duration_reduction=%.2f%% output=%s\n",
		summary.DatasetVersion, summary.Pairs, summary.MedianThroughputIncreasePercent,
		summary.MedianDurationReductionPercent, options.outputPath,
	)
	return nil
}

func prepareStagingDocuments(
	ctx context.Context,
	parser knowledgeingestion.Parser,
	cfg config.Config,
	profile knowledge.EmbeddingProfile,
	documents []loadedDocument,
) ([]stagingDocument, error) {
	prepared := make([]stagingDocument, 0, len(documents))
	for _, document := range documents {
		parsed, err := parser.Parse(ctx, knowledgeparser.Input{
			MediaType: document.definition.MediaType, OriginalName: document.definition.FileName,
			Content: document.content,
		})
		if err != nil {
			return nil, fmt.Errorf("parse staging document %s: %w", document.definition.DocumentID, err)
		}
		chunks, err := knowledge.ChunkElements(parsed.Elements, knowledge.TextChunkOptions{
			MaxRunes: cfg.Knowledge.ChunkMaxRunes, OverlapRunes: cfg.Knowledge.ChunkOverlapRunes,
		})
		if err != nil {
			return nil, fmt.Errorf("chunk staging document %s: %w", document.definition.DocumentID, err)
		}
		if len(chunks) == 0 {
			return nil, fmt.Errorf("staging document %s produced no searchable chunks", document.definition.DocumentID)
		}
		embeddings := make([]knowledge.ChunkEmbeddingDraft, len(chunks))
		for ordinal, chunk := range chunks {
			embeddings[ordinal] = knowledge.ChunkEmbeddingDraft{
				ChunkOrdinal: ordinal, ContentSHA256: chunk.ContentSHA256,
				Vector: deterministicStagingVector(chunk.ContentText, profile.Dimensions),
			}
		}
		profileCopy := profile
		prepared = append(prepared, stagingDocument{
			definition: document.definition, elements: len(parsed.Elements),
			result: knowledgeworker.ExecutionResult{
				ParserVersion: parsed.ParserVersion, ParserMetadata: parsed.Metadata,
				Checkpoint: json.RawMessage(`{"mode":"database_ablation"}`), Chunks: chunks,
				EmbeddingProfile: &profileCopy, Embeddings: embeddings,
			},
		})
	}
	return prepared, nil
}

func runDatabaseStagingVariant(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	datasetVersion, corpusHash, environmentHash string,
	actorID uuid.UUID,
	documents []stagingDocument,
	repetition int,
	variant variantConfig,
) (observation knowledgeingestion.ThroughputObservation, err error) {
	repository, err := platformpostgres.NewKnowledgeWorkerRepositoryWithBatchSize(db, variant.chunkWriteBatchSize)
	if err != nil {
		return observation, err
	}
	tasks := make([]stagingTask, 0, len(documents))
	defer func() {
		err = errors.Join(err, cleanupStagingTasks(db, tasks))
	}()
	for _, document := range documents {
		task, queueErr := queueStagingTask(ctx, db, repository, cfg, actorID, document, repetition, variant.variant)
		if queueErr != nil {
			return observation, queueErr
		}
		tasks = append(tasks, task)
	}

	startedAt := time.Now()
	for _, task := range tasks {
		saved, saveErr := repository.SaveParsedResult(ctx, task.lease, task.result, time.Now().UTC())
		if saveErr != nil {
			return observation, saveErr
		}
		if !saved {
			return observation, errors.New("database ablation lost the staging lease")
		}
	}
	duration := time.Since(startedAt)
	observation = knowledgeingestion.ThroughputObservation{
		DatasetVersion: datasetVersion, RunID: "db-ablation-" + uuid.NewString(), Repetition: repetition,
		Variant: variant.variant, CorpusFingerprint: corpusHash, EnvironmentFingerprint: environmentHash,
		Documents: len(documents), FormatCount: stagingFormatCount(documents), SucceededDocuments: len(documents),
		DurationMillis: max(1, duration.Milliseconds()), ProcessDurationMillis: duration.Milliseconds(),
		DocumentConcurrency: 1, EmbeddingBatchSize: variant.embeddingBatchSize,
		EmbeddingMaxConcurrent: variant.embeddingMaxConcurrent, ChunkWriteBatchSize: variant.chunkWriteBatchSize,
	}
	for _, document := range documents {
		observation.SourceBytes += document.definition.SizeBytes
		observation.Pages += document.definition.PageCount
		observation.Elements += document.elements
		observation.Chunks += len(document.result.Chunks)
		observation.ChunkInsertBatches += batches(len(document.result.Chunks), variant.chunkWriteBatchSize)
		observation.EmbeddingInsertBatches += batches(len(document.result.Embeddings), variant.chunkWriteBatchSize)
	}
	return observation, observation.Validate()
}

func queueStagingTask(
	ctx context.Context,
	db *gorm.DB,
	repository *platformpostgres.KnowledgeWorkerRepository,
	cfg config.Config,
	actorID uuid.UUID,
	document stagingDocument,
	repetition int,
	variant knowledgeingestion.ThroughputVariant,
) (task stagingTask, err error) {
	createdAt := time.Now().UTC()
	documentID, versionID, taskID := uuid.New(), uuid.New(), uuid.New()
	sourceKey, err := objectstore.NewObjectKey(objectstore.BucketKnowledgeSources, versionID, createdAt)
	if err != nil {
		return stagingTask{}, err
	}
	source := objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: sourceKey, ETag: "db-ablation",
		SizeBytes: document.definition.SizeBytes, SHA256: document.definition.SHA256,
		MediaType: document.definition.MediaType, OriginalName: document.definition.FileName,
	}
	queued, err := platformpostgres.NewKnowledgeRepository(db).QueueVersion(ctx, knowledge.QueueVersionInput{
		VersionID: versionID, TaskID: taskID, OutboxEventID: uuid.New(), CorrelationID: uuid.New(),
		DocumentID: documentID, CreatedBy: actorID, Source: source,
		PipelineVersion: cfg.Knowledge.PipelineVersion, MaxAttempts: cfg.Knowledge.MaxAttempts,
		IdempotencyKey: uuid.NewString(), RequestFingerprint: knowledge.SHA256Hex(fmt.Sprintf(
			"%s/%s/%d/%s", databaseAblationVersion, document.definition.DocumentID, repetition, variant,
		)),
		NewDocument: &knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal,
			Title: fmt.Sprintf("[db-ablation:%s] %s", variant, document.definition.Title), CreatedBy: actorID,
		},
		CreatedAt: createdAt,
	})
	if err != nil {
		return stagingTask{}, err
	}
	task = stagingTask{documentID: documentID, versionID: versionID, taskID: taskID}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanupStagingTasks(db, []stagingTask{task}))
		}
	}()
	workerID := "db-ablation-" + uuid.NewString()
	claim, err := repository.Claim(ctx, queued.Task.ID, queued.Version.ID, workerID, createdAt, createdAt.Add(5*time.Minute))
	if err != nil {
		return task, err
	}
	if claim.Disposition != knowledgeworker.ClaimAcquired || claim.Lease == nil {
		return task, fmt.Errorf("database ablation claim disposition is %s", claim.Disposition)
	}
	artifactKey, err := objectstore.NewObjectKey(objectstore.BucketKnowledgeArtifacts, versionID, createdAt)
	if err != nil {
		return task, err
	}
	result := document.result
	result.Artifact = objectstore.ObjectRef{
		Bucket: objectstore.BucketKnowledgeArtifacts, ObjectKey: artifactKey, ETag: "db-ablation",
		SizeBytes: 1, SHA256: knowledge.SHA256Hex(versionID.String()), MediaType: "application/json",
		OriginalName: document.definition.FileName + ".elements.json",
	}
	task = stagingTask{
		documentID: documentID, versionID: versionID, taskID: taskID, lease: *claim.Lease, result: result,
	}
	return task, nil
}

func deterministicStagingVector(text string, dimensions int) []float32 {
	digest := sha256.Sum256([]byte(text))
	vector := make([]float32, dimensions)
	for index, value := range digest {
		position := (index*257 + int(value)) % dimensions
		vector[position] += float32(int(value) + 1)
	}
	var squaredNorm float64
	for _, value := range vector {
		squaredNorm += float64(value) * float64(value)
	}
	norm := float32(math.Sqrt(squaredNorm))
	for index := range vector {
		vector[index] /= norm
	}
	return vector
}

func stagingFormatCount(documents []stagingDocument) int {
	formats := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		formats[document.definition.FormatClass] = struct{}{}
	}
	return len(formats)
}

func cleanupStagingTasks(db *gorm.DB, tasks []stagingTask) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cleanupErr error
	for _, task := range tasks {
		cleanupErr = errors.Join(cleanupErr, db.WithContext(cleanupCtx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("DELETE FROM outbox_events WHERE aggregate_id = ?", task.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_ingestion_events WHERE task_id = ?", task.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_chunks WHERE document_version_id = ?", task.versionID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_ingestion_tasks WHERE id = ?", task.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_document_versions WHERE id = ?", task.versionID).Error; err != nil {
				return err
			}
			return tx.Exec("DELETE FROM knowledge_documents WHERE id = ?", task.documentID).Error
		}))
	}
	return cleanupErr
}

func cleanupEvaluationActor(db *gorm.DB, actorID uuid.UUID) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = db.WithContext(cleanupCtx).Exec("DELETE FROM users WHERE id = ?", actorID).Error
}
