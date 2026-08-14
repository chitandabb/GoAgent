package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"
	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ConversationMemoryJobRepository struct {
	db *gorm.DB
}

var _ conversationmemoryworker.Repository = (*ConversationMemoryJobRepository)(nil)

func NewConversationMemoryJobRepository(db *gorm.DB) *ConversationMemoryJobRepository {
	return &ConversationMemoryJobRepository{db: db}
}

func (r *ConversationMemoryJobRepository) Claim(
	ctx context.Context,
	jobID, conversationID uuid.UUID,
	workerID string,
	now, leaseUntil time.Time,
) (conversationmemoryworker.ClaimResult, error) {
	if r == nil || r.db == nil {
		return conversationmemoryworker.ClaimResult{}, errors.New("conversation memory job repository is unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	if jobID == uuid.Nil || conversationID == uuid.Nil || workerID == "" || len(workerID) > 128 ||
		now.IsZero() || !leaseUntil.After(now) {
		return conversationmemoryworker.ClaimResult{}, conversation.ErrInvalidMessage
	}
	var result conversationmemoryworker.ClaimResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		job, err := loadConversationMemoryJobForUpdate(tx, jobID)
		if err != nil {
			return err
		}
		if job.ConversationID != conversationID {
			return repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
		result.Status = job.Status
		switch job.Status {
		case conversationmemoryworker.JobSucceeded, conversationmemoryworker.JobFailed:
			result.Disposition = conversationmemoryworker.ClaimTerminal
			return nil
		case conversationmemoryworker.JobRunning:
			if job.LeaseUntil != nil && job.LeaseUntil.After(now) {
				result.Disposition = conversationmemoryworker.ClaimLeaseHeld
				result.RetryAfter = job.LeaseUntil.Sub(now)
				return nil
			}
		case conversationmemoryworker.JobPending, conversationmemoryworker.JobRetryWait:
			if job.AvailableAt.After(now) {
				result.Disposition = conversationmemoryworker.ClaimDelayed
				result.RetryAfter = job.AvailableAt.Sub(now)
				return nil
			}
		default:
			return errors.New("persisted conversation memory job status is invalid")
		}
		if job.AttemptCount >= job.MaxAttempts {
			failed := tx.Exec(`
UPDATE conversation_memory_jobs
SET status = ?, claim_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
    failure_code = ?, failure_summary = ?, completed_at = ?, updated_at = ?
WHERE id = ?`, conversationmemoryworker.JobFailed, "memory_compaction_lease_exhausted",
				"conversation memory job exhausted attempts after its lease expired", now, now, job.ID)
			if failed.Error != nil {
				return TranslateError(failed.Error)
			}
			result.Disposition = conversationmemoryworker.ClaimTerminal
			result.Status = conversationmemoryworker.JobFailed
			return nil
		}
		var claimed conversationMemoryJobRecord
		claim := tx.Raw(`
UPDATE conversation_memory_jobs
SET status = ?, attempt_count = attempt_count + 1,
    claim_owner = ?, lease_until = ?, heartbeat_at = ?, fencing_token = fencing_token + 1,
    started_at = COALESCE(started_at, ?), failure_code = NULL, failure_summary = NULL, updated_at = ?
WHERE id = ?
RETURNING id, conversation_id, source_turn_id, requested_through_seq, base_snapshot_id,
          status, attempt_count, max_attempts, claim_owner, lease_until, heartbeat_at,
          fencing_token, available_at, activated_snapshot_id, activation_result,
          failure_code, failure_summary, started_at, completed_at, created_at, updated_at`,
			conversationmemoryworker.JobRunning, workerID, leaseUntil, now, now, now, job.ID).Scan(&claimed)
		if claim.Error != nil {
			return TranslateError(claim.Error)
		}
		if claim.RowsAffected != 1 || claimed.ClaimOwner == nil || claimed.LeaseUntil == nil {
			return errors.New("claim conversation memory job returned no lease")
		}
		lease := conversationmemoryworker.Lease{
			JobID: claimed.ID, ConversationID: claimed.ConversationID, ClaimOwner: *claimed.ClaimOwner,
			AttemptCount: claimed.AttemptCount, MaxAttempts: claimed.MaxAttempts,
			FencingToken: claimed.FencingToken, LeaseUntil: claimed.LeaseUntil.UTC(),
		}
		if err := lease.Validate(); err != nil {
			return err
		}
		result = conversationmemoryworker.ClaimResult{
			Disposition: conversationmemoryworker.ClaimAcquired,
			Status:      conversationmemoryworker.JobRunning,
			Lease:       &lease,
		}
		return nil
	})
	if err != nil {
		return conversationmemoryworker.ClaimResult{}, err
	}
	return result, nil
}

func (r *ConversationMemoryJobRepository) Renew(
	ctx context.Context,
	lease conversationmemoryworker.Lease,
	now, leaseUntil time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("conversation memory job repository is unavailable")
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	if lease.Validate() != nil || now.IsZero() || !leaseUntil.After(now) {
		return false, conversation.ErrInvalidMessage
	}
	updated := ResolveDB(ctx, r.db).Exec(`
UPDATE conversation_memory_jobs
SET lease_until = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND conversation_id = ? AND status = ? AND claim_owner = ? AND fencing_token = ?
  AND lease_until > ?`, leaseUntil, now, now, lease.JobID, lease.ConversationID,
		conversationmemoryworker.JobRunning, lease.ClaimOwner, lease.FencingToken, now)
	if updated.Error != nil {
		return false, TranslateError(updated.Error)
	}
	return updated.RowsAffected == 1, nil
}

func (r *ConversationMemoryJobRepository) LoadTask(
	ctx context.Context,
	lease conversationmemoryworker.Lease,
	now time.Time,
) (conversationmemoryworker.Task, error) {
	if r == nil || r.db == nil {
		return conversationmemoryworker.Task{}, errors.New("conversation memory job repository is unavailable")
	}
	now = now.UTC()
	if lease.Validate() != nil || now.IsZero() {
		return conversationmemoryworker.Task{}, conversation.ErrInvalidMessage
	}
	db := ResolveDB(ctx, r.db)
	var job conversationMemoryJobRecord
	loadedJob := db.Raw(`
	SELECT id, conversation_id, requested_through_seq, attempt_count
FROM conversation_memory_jobs
WHERE id = ? AND conversation_id = ? AND status = ? AND claim_owner = ? AND fencing_token = ?
  AND lease_until > ?`, lease.JobID, lease.ConversationID, conversationmemoryworker.JobRunning,
		lease.ClaimOwner, lease.FencingToken, now).Scan(&job)
	if loadedJob.Error != nil {
		return conversationmemoryworker.Task{}, TranslateError(loadedJob.Error)
	}
	if loadedJob.RowsAffected != 1 {
		return conversationmemoryworker.Task{}, conversationmemoryworker.ErrLeaseLost
	}
	var records []messageRecord
	loadedMessages := db.Raw(`
SELECT id, conversation_id, seq, role, content, content_schema_version, created_at
FROM conversation_messages
WHERE conversation_id = ? AND seq <= ?
ORDER BY seq ASC`, job.ConversationID, job.RequestedThroughSeq).Scan(&records)
	if loadedMessages.Error != nil {
		return conversationmemoryworker.Task{}, TranslateError(loadedMessages.Error)
	}
	messages := make([]conversation.Message, 0, len(records))
	for _, record := range records {
		messages = append(messages, messageFromRecord(record))
	}
	if err := loadConversationReferences(db, messages); err != nil {
		return conversationmemoryworker.Task{}, err
	}
	task := conversationmemoryworker.Task{
		JobID: job.ID, ConversationID: job.ConversationID,
		RequestedThroughSeq: job.RequestedThroughSeq, AttemptCount: job.AttemptCount,
		CompletedMessages: messages,
	}
	if err := task.Validate(); err != nil {
		return conversationmemoryworker.Task{}, err
	}
	return task, nil
}

func (r *ConversationMemoryJobRepository) Complete(
	ctx context.Context,
	lease conversationmemoryworker.Lease,
	execution conversationmemoryworker.ExecutionResult,
	completedAt time.Time,
) (conversationmemoryworker.CompletionResult, error) {
	if r == nil || r.db == nil {
		return conversationmemoryworker.CompletionResult{}, errors.New("conversation memory job repository is unavailable")
	}
	completedAt = completedAt.UTC()
	if lease.Validate() != nil || execution.Validate() != nil || completedAt.IsZero() {
		return conversationmemoryworker.CompletionResult{}, conversation.ErrInvalidMessage
	}
	var result conversationmemoryworker.CompletionResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		job, err := loadConversationMemoryJobForUpdate(tx, lease.JobID)
		if err != nil {
			return err
		}
		if !conversationMemoryLeaseOwned(job, lease, completedAt) {
			return conversationmemoryworker.ErrLeaseLost
		}
		if execution.ThroughSeq < job.RequestedThroughSeq {
			return conversation.ErrInvalidMessage
		}
		active, err := loadConversationMemorySnapshotWithDB(
			tx, conversationMemoryActiveSnapshotQuery+"\nFOR UPDATE", job.ConversationID,
		)
		if err != nil || !currentSummaryCompletesExecution(active, execution, job.RequestedThroughSeq) {
			if err == nil {
				err = conversationmemory.ErrSnapshotActivationConflict
			}
			return err
		}
		activationResult := conversationmemoryworker.ActivationAlreadyCurrent
		updated := tx.Exec(`
UPDATE conversation_memory_jobs
SET status = ?, claim_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
    activated_snapshot_id = ?, activation_result = ?, failure_code = NULL, failure_summary = NULL,
    completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND claim_owner = ? AND fencing_token = ? AND lease_until > ?`,
			conversationmemoryworker.JobSucceeded, active.ID, activationResult, completedAt, completedAt,
			job.ID, conversationmemoryworker.JobRunning, lease.ClaimOwner, lease.FencingToken, completedAt)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return conversationmemoryworker.ErrLeaseLost
		}
		result = conversationmemoryworker.CompletionResult{
			Committed: true, ActivationResult: activationResult, ActiveSnapshotID: active.ID,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, conversationmemoryworker.ErrLeaseLost) {
			return conversationmemoryworker.CompletionResult{}, conversationmemoryworker.ErrLeaseLost
		}
		return conversationmemoryworker.CompletionResult{}, err
	}
	return result, nil
}

func currentSummaryCompletesExecution(
	active conversationmemory.Snapshot,
	execution conversationmemoryworker.ExecutionResult,
	requestedThroughSeq int64,
) bool {
	if active.ThroughSeq < requestedThroughSeq {
		return false
	}
	if active.ID == execution.CurrentSnapshotID {
		return active.ThroughSeq == execution.ThroughSeq
	}
	return active.ThroughSeq > execution.ThroughSeq
}

func (r *ConversationMemoryJobRepository) ReleaseForRetry(
	ctx context.Context,
	lease conversationmemoryworker.Lease,
	code, summary string,
	releasedAt, retryAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("conversation memory job repository is unavailable")
	}
	code, summary = boundedFailure(code, summary)
	releasedAt, retryAt = releasedAt.UTC(), retryAt.UTC()
	if lease.Validate() != nil || code == "" || summary == "" || releasedAt.IsZero() || !retryAt.After(releasedAt) {
		return false, conversation.ErrInvalidMessage
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return false, fmt.Errorf("generate conversation memory retry outbox id: %w", err)
	}
	released := false
	err = ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var updated conversationMemoryJobRecord
		query := tx.Raw(`
UPDATE conversation_memory_jobs
SET status = ?, claim_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
    available_at = ?, failure_code = ?, failure_summary = ?, updated_at = ?
WHERE id = ? AND conversation_id = ? AND status = ? AND claim_owner = ? AND fencing_token = ?
  AND lease_until > ?
RETURNING id, conversation_id, source_turn_id`, conversationmemoryworker.JobRetryWait, retryAt, code, summary,
			releasedAt, lease.JobID, lease.ConversationID, conversationmemoryworker.JobRunning,
			lease.ClaimOwner, lease.FencingToken, releasedAt).Scan(&updated)
		if query.Error != nil {
			return TranslateError(query.Error)
		}
		if query.RowsAffected != 1 {
			return nil
		}
		if err := insertConversationMemoryOutbox(tx, outboxID, updated.ID, updated.ConversationID,
			updated.SourceTurnID, retryAt, releasedAt); err != nil {
			return err
		}
		released = true
		return nil
	})
	return released, err
}

func (r *ConversationMemoryJobRepository) Fail(
	ctx context.Context,
	lease conversationmemoryworker.Lease,
	code, summary string,
	failedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("conversation memory job repository is unavailable")
	}
	code, summary = boundedFailure(code, summary)
	failedAt = failedAt.UTC()
	if lease.Validate() != nil || code == "" || summary == "" || failedAt.IsZero() {
		return false, conversation.ErrInvalidMessage
	}
	updated := ResolveDB(ctx, r.db).Exec(`
UPDATE conversation_memory_jobs
SET status = ?, claim_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
    activated_snapshot_id = NULL, activation_result = NULL,
    failure_code = ?, failure_summary = ?, completed_at = ?, updated_at = ?
WHERE id = ? AND conversation_id = ? AND status = ? AND claim_owner = ? AND fencing_token = ?
  AND lease_until > ?`, conversationmemoryworker.JobFailed, code, summary, failedAt, failedAt,
		lease.JobID, lease.ConversationID, conversationmemoryworker.JobRunning,
		lease.ClaimOwner, lease.FencingToken, failedAt)
	if updated.Error != nil {
		return false, TranslateError(updated.Error)
	}
	return updated.RowsAffected == 1, nil
}

func loadConversationMemoryJobForUpdate(tx *gorm.DB, jobID uuid.UUID) (conversationMemoryJobRecord, error) {
	var job conversationMemoryJobRecord
	loaded := tx.Raw(`
SELECT id, conversation_id, source_turn_id, requested_through_seq, base_snapshot_id,
       status, attempt_count, max_attempts, claim_owner, lease_until, heartbeat_at,
       fencing_token, available_at, activated_snapshot_id, activation_result,
       failure_code, failure_summary, started_at, completed_at, created_at, updated_at
FROM conversation_memory_jobs
WHERE id = ?
FOR UPDATE`, jobID).Scan(&job)
	if loaded.Error != nil {
		return conversationMemoryJobRecord{}, TranslateError(loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return conversationMemoryJobRecord{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return job, nil
}

func conversationMemoryLeaseOwned(
	job conversationMemoryJobRecord,
	lease conversationmemoryworker.Lease,
	now time.Time,
) bool {
	return job.ID == lease.JobID && job.ConversationID == lease.ConversationID &&
		job.Status == conversationmemoryworker.JobRunning && job.ClaimOwner != nil &&
		*job.ClaimOwner == lease.ClaimOwner && job.FencingToken == lease.FencingToken &&
		job.LeaseUntil != nil && job.LeaseUntil.After(now)
}

func boundedFailure(code, summary string) (string, string) {
	code = strings.TrimSpace(code)
	summary = strings.TrimSpace(summary)
	if len(code) > 128 {
		code = code[:128]
	}
	if len(summary) > 1000 {
		summary = strings.ToValidUTF8(summary[:1000], "?")
	}
	return code, summary
}

func insertConversationMemoryOutbox(
	tx *gorm.DB,
	outboxID, jobID, conversationID, correlationID uuid.UUID,
	availableAt, createdAt time.Time,
) error {
	payload, err := json.Marshal(map[string]string{
		"jobId": jobID.String(), "conversationId": conversationID.String(),
	})
	if err != nil {
		return fmt.Errorf("marshal conversation memory outbox payload: %w", err)
	}
	inserted := tx.Exec(`
INSERT INTO outbox_events (
    id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
    payload, payload_schema_version, attempt_count, available_at, created_at
)
VALUES (?, ?, ?, ?, ?, NULL, CAST(? AS jsonb), 1, 0, ?, ?)`, outboxID,
		messaging.EventTypeConversationMemoryCompact, messaging.AggregateTypeConversationMemoryJob,
		jobID, correlationID,
		string(payload), availableAt.UTC(), createdAt.UTC())
	if inserted.Error != nil {
		return TranslateError(inserted.Error)
	}
	return nil
}

type conversationMemoryJobRecord struct {
	ID                  uuid.UUID                                  `gorm:"column:id"`
	ConversationID      uuid.UUID                                  `gorm:"column:conversation_id"`
	SourceTurnID        uuid.UUID                                  `gorm:"column:source_turn_id"`
	RequestedThroughSeq int64                                      `gorm:"column:requested_through_seq"`
	BaseSnapshotID      *uuid.UUID                                 `gorm:"column:base_snapshot_id"`
	Status              conversationmemoryworker.JobStatus         `gorm:"column:status"`
	AttemptCount        int                                        `gorm:"column:attempt_count"`
	MaxAttempts         int                                        `gorm:"column:max_attempts"`
	ClaimOwner          *string                                    `gorm:"column:claim_owner"`
	LeaseUntil          *time.Time                                 `gorm:"column:lease_until"`
	HeartbeatAt         *time.Time                                 `gorm:"column:heartbeat_at"`
	FencingToken        int64                                      `gorm:"column:fencing_token"`
	AvailableAt         time.Time                                  `gorm:"column:available_at"`
	ActivatedSnapshotID *uuid.UUID                                 `gorm:"column:activated_snapshot_id"`
	ActivationResult    *conversationmemoryworker.ActivationResult `gorm:"column:activation_result"`
	FailureCode         *string                                    `gorm:"column:failure_code"`
	FailureSummary      *string                                    `gorm:"column:failure_summary"`
	StartedAt           *time.Time                                 `gorm:"column:started_at"`
	CompletedAt         *time.Time                                 `gorm:"column:completed_at"`
	CreatedAt           time.Time                                  `gorm:"column:created_at"`
	UpdatedAt           time.Time                                  `gorm:"column:updated_at"`
}
