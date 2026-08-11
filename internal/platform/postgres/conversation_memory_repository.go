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

var _ conversationmemory.Repository = (*ConversationMemoryRepository)(nil)

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
			ID         uuid.UUID `gorm:"column:id"`
			Version    int64     `gorm:"column:snapshot_version"`
			FromSeq    int64     `gorm:"column:from_seq"`
			ThroughSeq int64     `gorm:"column:through_seq"`
		}
		loadedLatest := tx.Raw(`
SELECT id, snapshot_version, from_seq, through_seq
FROM conversation_memory_snapshots
WHERE conversation_id = ?
ORDER BY snapshot_version DESC
LIMIT 1`, candidate.ConversationID).Scan(&latest)
		if loadedLatest.Error != nil {
			return TranslateError(loadedLatest.Error)
		}
		version := int64(1)
		if loadedLatest.RowsAffected == 0 {
			if candidate.SupersedesSnapshotID != nil {
				return conversationmemory.ErrInvalidSnapshot
			}
		} else {
			if candidate.SupersedesSnapshotID == nil || *candidate.SupersedesSnapshotID != latest.ID ||
				candidate.FromSeq != latest.FromSeq || candidate.ThroughSeq <= latest.ThroughSeq {
				return conversationmemory.ErrInvalidSnapshot
			}
			version = latest.Version + 1
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
			candidate.SummaryModelProfile, candidate.SummaryModelProvider, candidate.SummaryModelID, candidate.PromptVersion,
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
	var record conversationMemorySnapshotRecord
	loaded := ResolveDB(ctx, r.db).Raw(`
SELECT id, conversation_id, snapshot_version, supersedes_snapshot_id,
       from_seq, through_seq, schema_version,
       summary_model_profile, summary_model_provider, summary_model_id, prompt_version,
       payload::text AS payload, payload_sha256,
       prompt_tokens, completion_tokens, total_tokens, cached_tokens,
       status, created_at, activated_at
FROM conversation_memory_snapshots
WHERE conversation_id = ?
ORDER BY snapshot_version DESC
LIMIT 1`, conversationID).Scan(&record)
	if loaded.Error != nil {
		return nil, TranslateError(loaded.Error)
	}
	if loaded.RowsAffected == 0 {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	snapshot, err := snapshotFromRecord(record)
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
	var record conversationMemorySnapshotRecord
	loaded := ResolveDB(ctx, r.db).Raw(`
SELECT id, conversation_id, snapshot_version, supersedes_snapshot_id,
       from_seq, through_seq, schema_version,
       summary_model_profile, summary_model_provider, summary_model_id, prompt_version,
       payload::text AS payload, payload_sha256,
       prompt_tokens, completion_tokens, total_tokens, cached_tokens,
       status, created_at, activated_at
FROM conversation_memory_snapshots
WHERE id = ?`, snapshotID).Scan(&record)
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
		SummaryModelProfile: record.SummaryModelProfile, SummaryModelProvider: record.SummaryModelProvider,
		SummaryModelID: record.SummaryModelID, PromptVersion: record.PromptVersion,
		Payload: payload, PayloadSHA256: record.PayloadSHA256,
		Usage: conversationmemory.SummaryUsage{
			PromptTokens: record.PromptTokens, CompletionTokens: record.CompletionTokens,
			TotalTokens: record.TotalTokens, CachedTokens: record.CachedTokens,
		},
		Status: record.Status, CreatedAt: record.CreatedAt.UTC(), ActivatedAt: record.ActivatedAt,
	}
	if err := candidate.Validate(); err != nil {
		return conversationmemory.Snapshot{}, fmt.Errorf("validate persisted conversation memory snapshot: %w", err)
	}
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: record.Version}
	if err := snapshot.Validate(); err != nil {
		return conversationmemory.Snapshot{}, err
	}
	return snapshot, nil
}
