package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ConversationMemoryRepository persists immutable structured Snapshot content.
// Version allocation is serialized by locking the parent Conversation row.
type ConversationMemoryRepository struct {
	db *gorm.DB
}

var _ conversationmemory.ActivationRepository = (*ConversationMemoryRepository)(nil)

func NewConversationMemoryRepository(db *gorm.DB) *ConversationMemoryRepository {
	return &ConversationMemoryRepository{db: db}
}

func (r *ConversationMemoryRepository) Save(
	ctx context.Context,
	candidate conversationmemory.CandidateSnapshot,
) (conversationmemory.Snapshot, error) {
	if r == nil || r.db == nil {
		return conversationmemory.Snapshot{}, errors.New("conversation memory repository is unavailable")
	}
	if err := candidate.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	payload, err := json.Marshal(candidate.Payload)
	if err != nil {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}
	var result conversationmemory.Snapshot
	err = ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var lockedConversation struct {
			ID uuid.UUID `gorm:"column:id"`
		}
		locked := tx.Raw(`SELECT id FROM conversations WHERE id = ? FOR UPDATE`, candidate.ConversationID).Scan(&lockedConversation)
		if locked.Error != nil {
			return TranslateError(locked.Error)
		}
		if locked.RowsAffected == 0 {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		var latest struct {
			Version int64 `gorm:"column:snapshot_version"`
		}
		loadedLatest := tx.Raw(`
SELECT snapshot_version
FROM conversation_memory_snapshots
WHERE conversation_id = ?
ORDER BY snapshot_version DESC
LIMIT 1`, candidate.ConversationID).Scan(&latest)
		if loadedLatest.Error != nil {
			return TranslateError(loadedLatest.Error)
		}
		version := int64(1)
		if loadedLatest.RowsAffected > 0 {
			version = latest.Version + 1
		}
		if candidate.SupersedesSnapshotID == nil {
			var activeCount int64
			if err := tx.Raw(`
SELECT COUNT(*)
FROM conversation_memory_snapshots
WHERE conversation_id = ? AND status = ?`, candidate.ConversationID, conversationmemory.SnapshotStatusActive).
				Scan(&activeCount).Error; err != nil {
				return TranslateError(err)
			}
			if activeCount != 0 {
				return conversationmemory.ErrInvalidSnapshot
			}
		} else {
			var predecessor struct {
				ConversationID uuid.UUID `gorm:"column:conversation_id"`
				FromSeq        int64     `gorm:"column:from_seq"`
				ThroughSeq     int64     `gorm:"column:through_seq"`
			}
			loadedPredecessor := tx.Raw(`
SELECT conversation_id, from_seq, through_seq
FROM conversation_memory_snapshots
WHERE id = ?`, *candidate.SupersedesSnapshotID).Scan(&predecessor)
			if loadedPredecessor.Error != nil {
				return TranslateError(loadedPredecessor.Error)
			}
			if loadedPredecessor.RowsAffected != 1 || predecessor.ConversationID != candidate.ConversationID ||
				predecessor.FromSeq != candidate.FromSeq || predecessor.ThroughSeq >= candidate.ThroughSeq {
				return conversationmemory.ErrInvalidSnapshot
			}
		}
		inserted := tx.Exec(`
INSERT INTO conversation_memory_snapshots (
    id, conversation_id, snapshot_version, supersedes_snapshot_id,
    from_seq, through_seq, schema_version,
    summary_model_profile, summary_model_provider, summary_model_id, prompt_version,
    payload, payload_sha256,
    prompt_tokens, completion_tokens, total_tokens, cached_tokens,
    status, created_at, activated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS jsonb), ?, ?, ?, ?, ?, ?, ?, NULL)`,
			candidate.ID, candidate.ConversationID, version, candidate.SupersedesSnapshotID,
			candidate.FromSeq, candidate.ThroughSeq, candidate.SchemaVersion,
			candidate.Provenance.ModelProfile, candidate.Provenance.ModelProvider,
			candidate.Provenance.ModelID, candidate.Provenance.PromptVersion,
			string(payload), candidate.PayloadSHA256,
			candidate.Usage.PromptTokens, candidate.Usage.CompletionTokens, candidate.Usage.TotalTokens, candidate.Usage.CachedTokens,
			candidate.Status, candidate.CreatedAt,
		)
		if inserted.Error != nil {
			return TranslateError(inserted.Error)
		}
		result = conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: version}
		return nil
	})
	if err != nil {
		return conversationmemory.Snapshot{}, err
	}
	return result, nil
}

func (r *ConversationMemoryRepository) Active(
	ctx context.Context,
	conversationID uuid.UUID,
) (*conversationmemory.Snapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("conversation memory repository is unavailable")
	}
	if conversationID == uuid.Nil {
		return nil, conversationmemory.ErrInvalidSnapshot
	}
	snapshot, err := r.loadSnapshot(ctx, conversationMemoryActiveSnapshotQuery, conversationID)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// ActiveIdentity is the lightweight authority check used before reading an
// immutable Snapshot payload from Redis. It deliberately excludes the JSONB
// payload while preserving PostgreSQL as the Active publication source.
func (r *ConversationMemoryRepository) ActiveIdentity(
	ctx context.Context,
	conversationID uuid.UUID,
) (conversationmemory.ActiveSnapshotIdentity, error) {
	if r == nil || r.db == nil {
		return conversationmemory.ActiveSnapshotIdentity{}, errors.New("conversation memory repository is unavailable")
	}
	if conversationID == uuid.Nil {
		return conversationmemory.ActiveSnapshotIdentity{}, conversationmemory.ErrInvalidSnapshot
	}
	var identity conversationmemory.ActiveSnapshotIdentity
	loaded := ResolveDB(ctx, r.db).Raw(`
SELECT conversation_id, id AS snapshot_id, snapshot_version AS version, payload_sha256
FROM conversation_memory_snapshots
WHERE conversation_id = ? AND status = 'active'
LIMIT 1`, conversationID).Scan(&identity)
	if loaded.Error != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, TranslateError(loaded.Error)
	}
	if loaded.RowsAffected == 0 {
		return conversationmemory.ActiveSnapshotIdentity{}, conversationmemory.ErrSnapshotNotFound
	}
	if err := identity.Validate(); err != nil {
		return conversationmemory.ActiveSnapshotIdentity{}, err
	}
	return identity, nil
}

func (r *ConversationMemoryRepository) Activate(
	ctx context.Context,
	request conversationmemory.ActivationRequest,
) (conversationmemory.Snapshot, error) {
	if r == nil || r.db == nil {
		return conversationmemory.Snapshot{}, errors.New("conversation memory repository is unavailable")
	}
	if err := request.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	request.ActivatedAt = request.ActivatedAt.UTC()
	var activated conversationmemory.Snapshot
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var err error
		activated, err = activateConversationMemorySnapshotWithDB(tx, request)
		return err
	})
	if err != nil {
		return conversationmemory.Snapshot{}, err
	}
	return activated, nil
}

// activateConversationMemorySnapshotWithDB owns the Active Snapshot CAS SQL so
// synchronous publication and fenced async Job completion use identical
// lifecycle semantics inside their respective transactions.
func activateConversationMemorySnapshotWithDB(
	tx *gorm.DB,
	request conversationmemory.ActivationRequest,
) (conversationmemory.Snapshot, error) {
	if tx == nil || request.Validate() != nil {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}
	request.ActivatedAt = request.ActivatedAt.UTC()
	var lockedConversation struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	loadedConversation := tx.Raw(`SELECT id FROM conversations WHERE id = ? FOR UPDATE`, request.ConversationID).
		Scan(&lockedConversation)
	if loadedConversation.Error != nil {
		return conversationmemory.Snapshot{}, TranslateError(loadedConversation.Error)
	}
	if loadedConversation.RowsAffected != 1 {
		return conversationmemory.Snapshot{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}

	candidate, err := loadConversationMemorySnapshotWithDB(
		tx, conversationMemorySnapshotByIDForUpdateQuery, request.CandidateSnapshotID,
	)
	if err != nil {
		return conversationmemory.Snapshot{}, err
	}
	if candidate.ConversationID != request.ConversationID ||
		candidate.Status != conversationmemory.SnapshotStatusCandidate ||
		request.ActivatedAt.Before(candidate.CreatedAt) {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}

	var currentActive struct {
		ID         uuid.UUID `gorm:"column:id"`
		ThroughSeq int64     `gorm:"column:through_seq"`
	}
	loadedActive := tx.Raw(`
SELECT id, through_seq
FROM conversation_memory_snapshots
WHERE conversation_id = ? AND status = ?
FOR UPDATE`, request.ConversationID, conversationmemory.SnapshotStatusActive).Scan(&currentActive)
	if loadedActive.Error != nil {
		return conversationmemory.Snapshot{}, TranslateError(loadedActive.Error)
	}
	if request.ExpectedActiveSnapshotID == nil {
		if loadedActive.RowsAffected != 0 || candidate.SupersedesSnapshotID != nil {
			return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
		}
	} else {
		if loadedActive.RowsAffected != 1 || currentActive.ID != *request.ExpectedActiveSnapshotID ||
			candidate.SupersedesSnapshotID == nil || *candidate.SupersedesSnapshotID != currentActive.ID ||
			candidate.ThroughSeq <= currentActive.ThroughSeq {
			return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
		}
		updatedPrevious := tx.Exec(`
UPDATE conversation_memory_snapshots
SET status = ?
WHERE id = ? AND status = ?`, conversationmemory.SnapshotStatusSuperseded,
			currentActive.ID, conversationmemory.SnapshotStatusActive)
		if updatedPrevious.Error != nil {
			return conversationmemory.Snapshot{}, TranslateError(updatedPrevious.Error)
		}
		if updatedPrevious.RowsAffected != 1 {
			return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
		}
	}
	updatedCandidate := tx.Exec(`
UPDATE conversation_memory_snapshots
SET status = ?, activated_at = ?
WHERE id = ? AND status = ?`, conversationmemory.SnapshotStatusActive, request.ActivatedAt,
		candidate.ID, conversationmemory.SnapshotStatusCandidate)
	if updatedCandidate.Error != nil {
		return conversationmemory.Snapshot{}, TranslateError(updatedCandidate.Error)
	}
	if updatedCandidate.RowsAffected != 1 {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotActivationConflict
	}
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = &request.ActivatedAt
	if err := candidate.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	return candidate, nil
}

func (r *ConversationMemoryRepository) Latest(
	ctx context.Context,
	conversationID uuid.UUID,
) (*conversationmemory.Snapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("conversation memory repository is unavailable")
	}
	if conversationID == uuid.Nil {
		return nil, conversationmemory.ErrInvalidSnapshot
	}
	snapshot, err := r.loadSnapshot(ctx, conversationMemoryLatestSnapshotQuery, conversationID)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *ConversationMemoryRepository) Get(
	ctx context.Context,
	snapshotID uuid.UUID,
) (conversationmemory.Snapshot, error) {
	if r == nil || r.db == nil {
		return conversationmemory.Snapshot{}, errors.New("conversation memory repository is unavailable")
	}
	if snapshotID == uuid.Nil {
		return conversationmemory.Snapshot{}, conversationmemory.ErrInvalidSnapshot
	}
	return r.loadSnapshot(ctx, conversationMemorySnapshotByIDQuery, snapshotID)
}

const conversationMemorySnapshotProjection = `
SELECT id, conversation_id, snapshot_version, supersedes_snapshot_id,
       from_seq, through_seq, schema_version,
       summary_model_profile, summary_model_provider, summary_model_id, prompt_version,
       payload::text AS payload, payload_sha256,
       prompt_tokens, completion_tokens, total_tokens, cached_tokens,
       status, created_at, activated_at
FROM conversation_memory_snapshots`

const conversationMemoryLatestSnapshotQuery = conversationMemorySnapshotProjection + `
WHERE conversation_id = ?
ORDER BY snapshot_version DESC
LIMIT 1`

const conversationMemorySnapshotByIDQuery = conversationMemorySnapshotProjection + `
WHERE id = ?`

const conversationMemorySnapshotByIDForUpdateQuery = conversationMemorySnapshotByIDQuery + `
FOR UPDATE`

const conversationMemoryActiveSnapshotQuery = conversationMemorySnapshotProjection + `
WHERE conversation_id = ? AND status = 'active'
LIMIT 1`

func (r *ConversationMemoryRepository) loadSnapshot(
	ctx context.Context,
	query string,
	args ...any,
) (conversationmemory.Snapshot, error) {
	return loadConversationMemorySnapshotWithDB(ResolveDB(ctx, r.db), query, args...)
}

func loadConversationMemorySnapshotWithDB(
	db *gorm.DB,
	query string,
	args ...any,
) (conversationmemory.Snapshot, error) {
	var record conversationMemorySnapshotRecord
	loaded := db.Raw(query, args...).Scan(&record)
	if loaded.Error != nil {
		return conversationmemory.Snapshot{}, TranslateError(loaded.Error)
	}
	if loaded.RowsAffected == 0 {
		return conversationmemory.Snapshot{}, conversationmemory.ErrSnapshotNotFound
	}
	return snapshotFromRecord(record)
}

type conversationMemorySnapshotRecord struct {
	ID                   uuid.UUID                         `gorm:"column:id"`
	ConversationID       uuid.UUID                         `gorm:"column:conversation_id"`
	Version              int64                             `gorm:"column:snapshot_version"`
	SupersedesSnapshotID *uuid.UUID                        `gorm:"column:supersedes_snapshot_id"`
	FromSeq              int64                             `gorm:"column:from_seq"`
	ThroughSeq           int64                             `gorm:"column:through_seq"`
	SchemaVersion        int                               `gorm:"column:schema_version"`
	SummaryModelProfile  string                            `gorm:"column:summary_model_profile"`
	SummaryModelProvider string                            `gorm:"column:summary_model_provider"`
	SummaryModelID       string                            `gorm:"column:summary_model_id"`
	PromptVersion        string                            `gorm:"column:prompt_version"`
	Payload              []byte                            `gorm:"column:payload"`
	PayloadSHA256        string                            `gorm:"column:payload_sha256"`
	PromptTokens         int                               `gorm:"column:prompt_tokens"`
	CompletionTokens     int                               `gorm:"column:completion_tokens"`
	TotalTokens          int                               `gorm:"column:total_tokens"`
	CachedTokens         int                               `gorm:"column:cached_tokens"`
	Status               conversationmemory.SnapshotStatus `gorm:"column:status"`
	CreatedAt            time.Time                         `gorm:"column:created_at"`
	ActivatedAt          *time.Time                        `gorm:"column:activated_at"`
}

func snapshotFromRecord(record conversationMemorySnapshotRecord) (conversationmemory.Snapshot, error) {
	payload, err := conversationmemory.DecodePayload(record.Payload)
	if err != nil {
		return conversationmemory.Snapshot{}, fmt.Errorf("decode conversation memory snapshot payload: %w", err)
	}
	candidate := conversationmemory.CandidateSnapshot{
		ID: record.ID, ConversationID: record.ConversationID,
		SupersedesSnapshotID: record.SupersedesSnapshotID,
		FromSeq:              record.FromSeq, ThroughSeq: record.ThroughSeq, SchemaVersion: record.SchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: record.SummaryModelProfile, ModelProvider: record.SummaryModelProvider,
			ModelID: record.SummaryModelID, PromptVersion: record.PromptVersion,
		},
		Payload: payload, PayloadSHA256: record.PayloadSHA256,
		Usage: conversationmemory.SummaryUsage{
			PromptTokens: record.PromptTokens, CompletionTokens: record.CompletionTokens,
			TotalTokens: record.TotalTokens, CachedTokens: record.CachedTokens,
		},
		Status: record.Status, CreatedAt: record.CreatedAt.UTC(), ActivatedAt: record.ActivatedAt,
	}
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: record.Version}
	if err := snapshot.Validate(); err != nil {
		return conversationmemory.Snapshot{}, fmt.Errorf("validate persisted conversation memory snapshot: %w", err)
	}
	return snapshot, nil
}
