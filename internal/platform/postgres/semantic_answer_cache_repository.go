package postgres

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

type SemanticAnswerCacheRepository struct {
	db             *gorm.DB
	maxRecords     int
	ttlJitterRatio float64
}

type SemanticAnswerCacheRepositoryConfig struct {
	MaxRecords     int
	TTLJitterRatio float64
}

var _ semanticcache.Provider = (*SemanticAnswerCacheRepository)(nil)

func NewSemanticAnswerCacheRepository(db *gorm.DB) *SemanticAnswerCacheRepository {
	repository, _ := NewSemanticAnswerCacheRepositoryWithConfig(db, SemanticAnswerCacheRepositoryConfig{
		MaxRecords: 1000, TTLJitterRatio: 0.1,
	})
	return repository
}

func NewSemanticAnswerCacheRepositoryWithConfig(
	db *gorm.DB,
	cfg SemanticAnswerCacheRepositoryConfig,
) (*SemanticAnswerCacheRepository, error) {
	if db == nil || cfg.MaxRecords < 1 || cfg.MaxRecords > 100_000 ||
		math.IsNaN(cfg.TTLJitterRatio) || math.IsInf(cfg.TTLJitterRatio, 0) ||
		cfg.TTLJitterRatio < 0 || cfg.TTLJitterRatio > 0.2 {
		return nil, errors.New("semantic answer cache repository configuration is invalid")
	}
	return &SemanticAnswerCacheRepository{
		db: db, maxRecords: cfg.MaxRecords, ttlJitterRatio: cfg.TTLJitterRatio,
	}, nil
}

type semanticAnswerCacheLookupRecord struct {
	Generation       int64      `gorm:"column:generation"`
	AnswerContent    *string    `gorm:"column:answer_content"`
	Citations        []byte     `gorm:"column:citations"`
	RetrievedSources []byte     `gorm:"column:retrieved_sources"`
	SourceRunID      *uuid.UUID `gorm:"column:source_run_id"`
	ModelProvider    *string    `gorm:"column:model_provider"`
	ModelID          *string    `gorm:"column:model_id"`
	PromptVersion    *string    `gorm:"column:prompt_version"`
	CreatedAt        *time.Time `gorm:"column:created_at"`
	ExpiresAt        *time.Time `gorm:"column:expires_at"`
	QuestionText     *string    `gorm:"column:question_text"`
	Similarity       float64    `gorm:"column:similarity"`
}

func (r *SemanticAnswerCacheRepository) Lookup(
	ctx context.Context,
	input semanticcache.LookupInput,
) (semanticcache.Answer, bool, error) {
	if r == nil || r.db == nil {
		return semanticcache.Answer{}, false, errors.New("semantic answer cache repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return semanticcache.Answer{}, false, err
	}
	var record semanticAnswerCacheLookupRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT generation.generation,
       cache.answer_content, cache.citations, cache.retrieved_sources,
       cache.source_run_id, cache.model_provider, cache.model_id, cache.prompt_version,
       cache.created_at, cache.expires_at
FROM global_knowledge_generation generation
LEFT JOIN semantic_answer_cache cache
  ON cache.question_hash = ? AND cache.generation = generation.generation AND cache.expires_at > ?
WHERE generation.singleton = 1`, input.QuestionHash, input.Now.UTC()).Scan(&record)
	if result.Error != nil {
		return semanticcache.Answer{}, false, TranslateError(result.Error)
	}
	if result.RowsAffected != 1 || record.Generation < 1 {
		return semanticcache.Answer{}, false, errors.New("global knowledge generation is unavailable")
	}
	if record.AnswerContent == nil {
		return semanticcache.Answer{}, false, nil
	}
	if record.SourceRunID == nil || record.ModelProvider == nil || record.ModelID == nil ||
		record.PromptVersion == nil || record.CreatedAt == nil || record.ExpiresAt == nil {
		return semanticcache.Answer{}, false, semanticcache.ErrInvalidRecord
	}
	var citations, retrievedSources []semanticcache.Source
	if err := decodeSemanticCacheJSON(record.Citations, &citations); err != nil {
		return semanticcache.Answer{}, false, err
	}
	if err := decodeSemanticCacheJSON(record.RetrievedSources, &retrievedSources); err != nil {
		return semanticcache.Answer{}, false, err
	}
	return semanticcache.Answer{
		Content: *record.AnswerContent, Citations: citations, RetrievedSources: retrievedSources,
		SourceRunID: *record.SourceRunID, ModelProvider: *record.ModelProvider, ModelID: *record.ModelID,
		PromptVersion: *record.PromptVersion, Generation: record.Generation,
		CreatedAt: record.CreatedAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(), Layer: semanticcache.LayerExact,
	}, true, nil
}

func (r *SemanticAnswerCacheRepository) LookupSemantic(
	ctx context.Context,
	input semanticcache.SemanticLookupInput,
) (semanticcache.Answer, bool, error) {
	if r == nil || r.db == nil {
		return semanticcache.Answer{}, false, errors.New("semantic answer cache repository is unavailable")
	}
	if err := input.Validate(1024, true); err != nil {
		return semanticcache.Answer{}, false, err
	}
	vector := pgvector.NewVector(input.Vector)
	var records []semanticAnswerCacheLookupRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT cache.generation, cache.answer_content, cache.citations, cache.retrieved_sources,
       cache.source_run_id, cache.model_provider, cache.model_id, cache.prompt_version,
       cache.created_at, cache.expires_at, cache.question_text,
       1 - (cache.question_embedding <=> ?) AS similarity
FROM global_knowledge_generation generation
JOIN semantic_answer_cache cache ON cache.generation = generation.generation
JOIN knowledge_embedding_profiles profile
  ON profile.id = cache.embedding_profile_id AND profile.status = 'active'
WHERE generation.singleton = 1
  AND cache.expires_at > ?
  AND cache.embedding_profile_id = ?
  AND cache.embedding_profile_fingerprint = ?
  AND profile.fingerprint = cache.embedding_profile_fingerprint
  AND cache.normalization_version = ?
  AND cache.question_embedding IS NOT NULL
  AND 1 - (cache.question_embedding <=> ?) >= ?
ORDER BY cache.question_embedding <=> ?, cache.created_at DESC, cache.question_hash
LIMIT ?`, vector, input.Now.UTC(), input.ProfileID, input.ProfileFingerprint,
		input.NormalizationVersion, vector, input.MinimumSimilarity, vector, input.CandidateLimit).Scan(&records)
	if result.Error != nil {
		return semanticcache.Answer{}, false, TranslateError(result.Error)
	}
	for _, record := range records {
		if record.QuestionText == nil || !semanticcache.CompareQuestions(input.Question, *record.QuestionText).Compatible {
			continue
		}
		answer, err := semanticAnswerFromRecord(record, semanticcache.LayerSemantic)
		if err != nil {
			return semanticcache.Answer{}, false, err
		}
		answer.Similarity = record.Similarity
		return answer, true, nil
	}
	return semanticcache.Answer{}, false, nil
}

func (r *SemanticAnswerCacheRepository) IndexSemantic(
	ctx context.Context,
	input semanticcache.SemanticIndexInput,
) error {
	if r == nil || r.db == nil {
		return errors.New("semantic answer cache repository is unavailable")
	}
	if err := input.Validate(1024, true); err != nil {
		return err
	}
	boundQuestionHash, err := semanticcache.ExactQuestionKey(input.Question)
	if err != nil || boundQuestionHash != input.QuestionHash {
		return semanticcache.ErrInvalidRecord
	}
	vector := pgvector.NewVector(input.Vector)
	return TranslateError(ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		generation, err := authorizeSemanticCacheIndex(tx, input)
		if err != nil {
			return err
		}
		result := tx.Exec(`
UPDATE semantic_answer_cache
SET question_text = ?, embedding_profile_id = ?, embedding_profile_fingerprint = ?,
    normalization_version = ?, question_embedding = ?
WHERE question_hash = ? AND generation = ? AND source_run_id = ?`,
			strings.TrimSpace(input.Question), input.ProfileID, input.ProfileFingerprint,
			input.NormalizationVersion, vector, input.QuestionHash, generation, input.SourceRunID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return semanticcache.ErrInvalidRecord
		}
		return nil
	}))
}

func semanticAnswerFromRecord(record semanticAnswerCacheLookupRecord, layer string) (semanticcache.Answer, error) {
	if record.AnswerContent == nil || record.SourceRunID == nil || record.ModelProvider == nil || record.ModelID == nil ||
		record.PromptVersion == nil || record.CreatedAt == nil || record.ExpiresAt == nil {
		return semanticcache.Answer{}, semanticcache.ErrInvalidRecord
	}
	var citations, retrievedSources []semanticcache.Source
	if err := decodeSemanticCacheJSON(record.Citations, &citations); err != nil {
		return semanticcache.Answer{}, err
	}
	if err := decodeSemanticCacheJSON(record.RetrievedSources, &retrievedSources); err != nil {
		return semanticcache.Answer{}, err
	}
	return semanticcache.Answer{
		Content: *record.AnswerContent, Citations: citations, RetrievedSources: retrievedSources,
		SourceRunID: *record.SourceRunID, ModelProvider: *record.ModelProvider, ModelID: *record.ModelID,
		PromptVersion: *record.PromptVersion, Generation: record.Generation,
		CreatedAt: record.CreatedAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(), Layer: layer,
	}, nil
}

func (r *SemanticAnswerCacheRepository) Put(ctx context.Context, input semanticcache.PutInput) error {
	if r == nil || r.db == nil {
		return errors.New("semantic answer cache repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return err
	}
	citations, err := json.Marshal(input.Answer.Citations)
	if err != nil {
		return fmt.Errorf("encode semantic cache citations: %w", err)
	}
	retrievedSources, err := json.Marshal(input.Answer.RetrievedSources)
	if err != nil {
		return fmt.Errorf("encode semantic cache retrieved sources: %w", err)
	}
	createdAt := input.Answer.CreatedAt.UTC()
	expiresAt := createdAt.Add(semanticCacheTTL(input.TTL, r.ttlJitterRatio, input.QuestionHash))
	return TranslateError(ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		generation, err := authorizeSemanticCachePut(tx, input)
		if err != nil {
			return err
		}
		result := tx.Exec(`
INSERT INTO semantic_answer_cache
    (question_hash, generation, answer_content, citations, retrieved_sources,
     source_run_id, model_provider, model_id, prompt_version, created_at, expires_at)
VALUES (?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?)
ON CONFLICT (question_hash, generation) DO UPDATE
SET answer_content = EXCLUDED.answer_content,
    citations = EXCLUDED.citations,
    retrieved_sources = EXCLUDED.retrieved_sources,
    source_run_id = EXCLUDED.source_run_id,
    model_provider = EXCLUDED.model_provider,
    model_id = EXCLUDED.model_id,
    prompt_version = EXCLUDED.prompt_version,
    created_at = EXCLUDED.created_at,
    expires_at = EXCLUDED.expires_at`,
			input.QuestionHash, generation, input.Answer.Content, string(citations), string(retrievedSources),
			input.Answer.SourceRunID, input.Answer.ModelProvider, input.Answer.ModelID,
			input.Answer.PromptVersion, createdAt, expiresAt)
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Exec("DELETE FROM semantic_answer_cache WHERE expires_at <= ?", createdAt).Error; err != nil {
			return err
		}
		return tx.Exec(`
DELETE FROM semantic_answer_cache
WHERE (question_hash, generation) IN (
    SELECT question_hash, generation
    FROM semantic_answer_cache
    ORDER BY created_at DESC, question_hash
    OFFSET ?
)`, r.maxRecords).Error
	}))
}

func semanticCacheTTL(ttl time.Duration, jitterRatio float64, questionHash string) time.Duration {
	if ttl <= 0 || jitterRatio <= 0 {
		return ttl
	}
	decoded, err := hex.DecodeString(questionHash)
	if err != nil || len(decoded) < 8 {
		return ttl
	}
	fraction := float64(binary.BigEndian.Uint64(decoded[:8])) / float64(^uint64(0))
	factor := 1 - jitterRatio + 2*jitterRatio*fraction
	return time.Duration(float64(ttl) * factor)
}

func validateSemanticCacheSourceRun(tx *gorm.DB, questionHash string, answer semanticcache.Answer) error {
	var record struct {
		AssistantMessageID  uuid.UUID `gorm:"column:assistant_message_id"`
		UserQuery           string    `gorm:"column:user_query"`
		Content             string    `gorm:"column:content"`
		ModelProvider       string    `gorm:"column:model_provider"`
		ModelID             string    `gorm:"column:model_id"`
		PromptVersion       string    `gorm:"column:prompt_version"`
		Outcome             string    `gorm:"column:outcome"`
		ExecutionPath       string    `gorm:"column:execution_path"`
		DegradedChannels    []byte    `gorm:"column:degraded_channels"`
		ContextReferences   int64     `gorm:"column:context_references"`
		AnswerCacheEligible bool      `gorm:"column:answer_cache_eligible"`
	}
	result := tx.Raw(`
SELECT turn_record.assistant_message_id, user_message.content AS user_query, message.content,
	   observation.model_provider, observation.model_id, observation.prompt_version,
	   observation.outcome, observation.execution_path, observation.degraded_channels,
	   observation.answer_cache_eligible,
	   (SELECT COUNT(*) FROM conversation_case_references reference
	    WHERE reference.message_id IN (turn_record.user_message_id, turn_record.assistant_message_id)) +
	   (SELECT COUNT(*) FROM conversation_task_references reference
	    WHERE reference.message_id IN (turn_record.user_message_id, turn_record.assistant_message_id)) +
	   (SELECT COUNT(*) FROM conversation_report_references reference
	    WHERE reference.message_id IN (turn_record.user_message_id, turn_record.assistant_message_id)) +
	   (SELECT COUNT(*) FROM conversation_message_attachments attachment
	    WHERE attachment.message_id = turn_record.user_message_id) AS context_references
FROM conversation_turns turn_record
JOIN conversation_messages message ON message.id = turn_record.assistant_message_id
JOIN conversation_messages user_message ON user_message.id = turn_record.user_message_id
JOIN conversation_turn_run_observations observation ON observation.turn_id = turn_record.id
WHERE turn_record.id = ? AND turn_record.status = 'completed'`, answer.SourceRunID).Scan(&record)
	if result.Error != nil {
		return result.Error
	}
	var degradedChannels []string
	if err := json.Unmarshal(record.DegradedChannels, &degradedChannels); err != nil {
		return semanticcache.ErrInvalidRecord
	}
	boundQuestionHash, err := semanticcache.ExactQuestionKey(record.UserQuery)
	if err != nil || boundQuestionHash != questionHash {
		return semanticcache.ErrInvalidRecord
	}
	if result.RowsAffected != 1 || record.Content != answer.Content || record.ModelProvider != answer.ModelProvider ||
		record.ModelID != answer.ModelID || record.PromptVersion != answer.PromptVersion ||
		record.Outcome != "answered" || record.ExecutionPath != "agent" || len(degradedChannels) != 0 ||
		record.ContextReferences != 0 || !record.AnswerCacheEligible {
		return semanticcache.ErrInvalidRecord
	}
	var citations []semanticcache.Source
	result = tx.Raw(`
SELECT position, source_type, source_ref, content_sha256
FROM conversation_message_citations
WHERE message_id = ?
ORDER BY position`, record.AssistantMessageID).Scan(&citations)
	if result.Error != nil {
		return result.Error
	}
	if !semanticSourcesEqual(citations, answer.Citations, true) {
		return semanticcache.ErrInvalidRecord
	}
	var retrieved []semanticcache.Source
	result = tx.Raw(`
SELECT position, source_type, source_ref, content_sha256
FROM conversation_turn_retrieved_sources
WHERE turn_id = ?
ORDER BY position`, answer.SourceRunID).Scan(&retrieved)
	if result.Error != nil {
		return result.Error
	}
	if !semanticSourcesEqual(retrieved, answer.RetrievedSources, false) {
		return semanticcache.ErrInvalidRecord
	}
	return nil
}

func semanticSourcesEqual(left, right []semanticcache.Source, comparePosition bool) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if (comparePosition && left[index].Position != right[index].Position) ||
			left[index].SourceType != right[index].SourceType || left[index].SourceRef != right[index].SourceRef ||
			left[index].ContentSHA256 != right[index].ContentSHA256 {
			return false
		}
	}
	return true
}

func validateCurrentGlobalKnowledgeSource(tx *gorm.DB, source semanticcache.Source) error {
	if source.SourceType != "knowledge_chunk" || len(source.ContentSHA256) != 64 {
		return semanticcache.ErrInvalidRecord
	}
	versionID, chunkID, ok := parseKnowledgeSourceRef(source.SourceRef)
	if !ok {
		return semanticcache.ErrInvalidRecord
	}
	var exists bool
	result := tx.Raw(`
SELECT true
FROM knowledge_chunks chunk
JOIN knowledge_document_versions version ON version.id = chunk.document_version_id
JOIN knowledge_documents document ON document.id = version.document_id
WHERE chunk.id = ? AND version.id = ? AND chunk.content_sha256 = ?
  AND version.is_current = true AND version.status = 'ready'
  AND document.scope = 'global' AND document.deleted_at IS NULL`,
		chunkID, versionID, source.ContentSHA256).Scan(&exists)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || !exists {
		return semanticcache.ErrInvalidRecord
	}
	return nil
}

func parseKnowledgeSourceRef(sourceRef string) (uuid.UUID, uuid.UUID, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(sourceRef), "knowledge:")
	versionText, chunkText, ok := strings.Cut(value, "/")
	if !ok || strings.Contains(chunkText, "/") || !strings.HasPrefix(sourceRef, "knowledge:") {
		return uuid.Nil, uuid.Nil, false
	}
	versionID, versionErr := uuid.Parse(versionText)
	chunkID, chunkErr := uuid.Parse(chunkText)
	return versionID, chunkID, versionErr == nil && chunkErr == nil
}

func validSemanticQuestionHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256Size
}

const sha256Size = 32

func decodeSemanticCacheJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.Join(semanticcache.ErrInvalidRecord, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return semanticcache.ErrInvalidRecord
	}
	return nil
}
