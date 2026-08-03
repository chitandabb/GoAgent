package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DiagnosisTaskRecoveryRepository 原子恢复可重试的失败任务、原 Outbox 和审计事实。
type DiagnosisTaskRecoveryRepository struct {
	db *gorm.DB
}

var _ diagnosis.TaskRecoveryRepository = (*DiagnosisTaskRecoveryRepository)(nil)

func NewDiagnosisTaskRecoveryRepository(db *gorm.DB) *DiagnosisTaskRecoveryRepository {
	return &DiagnosisTaskRecoveryRepository{db: db}
}

func (r *DiagnosisTaskRecoveryRepository) RecoverFailedTask(
	ctx context.Context,
	input diagnosis.TaskRecoveryRecord,
) (diagnosis.TaskRecoveryResult, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskRecoveryResult{}, errors.New("diagnosis task recovery repository is unavailable")
	}
	if input.ID == uuid.Nil || input.TaskID == uuid.Nil || input.RecoveredBy == uuid.Nil ||
		strings.TrimSpace(input.IdempotencyKey) == "" || strings.TrimSpace(input.Reason) == "" ||
		input.RecoveredAt.IsZero() {
		return diagnosis.TaskRecoveryResult{}, diagnosis.ErrInvalidTaskRecovery
	}
	input.RecoveredAt = input.RecoveredAt.UTC()

	var result diagnosis.TaskRecoveryResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		task, err := selectTaskRecoveryStateForUpdate(tx, input.TaskID)
		if err != nil {
			return err
		}
		existing, found, err := findTaskRecovery(tx, input.TaskID, input.RecoveredBy, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if existing.Reason != input.Reason {
				return diagnosis.ErrIdempotencyConflict
			}
			result = diagnosis.TaskRecoveryResult{Recovery: existing.toDomain(), Replayed: true}
			return nil
		}
		if task.Status != diagnosis.TaskFailed || task.ReportID != nil ||
			task.ClaimOwner != nil || task.ClaimedAt != nil || task.LeaseUntil != nil ||
			task.CancelRequestedAt != nil ||
			task.AttemptCount < 1 || !diagnosis.IsRecoverableTaskFailure(task.LastErrorCode) ||
			strings.TrimSpace(task.LastErrorMessage) == "" || task.CreatorStatus != "active" {
			return diagnosis.ErrTaskStateConflict
		}
		available, err := taskRecoveryDataSourcesAvailable(tx, input.TaskID)
		if err != nil {
			return err
		}
		if !available {
			return diagnosis.ErrTaskStateConflict
		}
		outbox, err := selectTaskRecoveryOutboxForUpdate(tx, input.TaskID)
		if err != nil {
			return err
		}

		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = 'pending', claim_owner = NULL, claimed_at = NULL, lease_until = NULL,
    completed_at = NULL, last_error_code = NULL, last_error_message = NULL,
    updated_at = ?
WHERE id = ? AND status = 'failed' AND attempt_count = ?`,
			input.RecoveredAt, input.TaskID, task.AttemptCount,
		)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosis.ErrTaskStateConflict
		}
		eventSeq, err := appendTaskEventWithSeq(tx, input.TaskID, diagnosis.TaskEventRequeued, map[string]any{
			"taskId": input.TaskID.String(), "status": string(diagnosis.TaskPending),
			"recoveryId": input.ID.String(), "recoveredBy": input.RecoveredBy.String(),
			"previousAttemptCount": task.AttemptCount,
			"previousErrorCode":    task.LastErrorCode,
		}, input.RecoveredAt)
		if err != nil {
			return err
		}
		reopened := tx.Exec(`
UPDATE outbox_events
SET published_at = NULL, attempt_count = 0, available_at = ?,
    locked_at = NULL, locked_by = NULL, locked_until = NULL,
    requeue_count = requeue_count + 1, last_requeued_at = ?,
    last_requeued_by = ?, last_error = NULL
WHERE id = ? AND event_type = 'diagnosis.execute'
  AND aggregate_type = 'diagnosis_task' AND aggregate_id = ?`,
			input.RecoveredAt, input.RecoveredAt, input.RecoveredBy, outbox.ID, input.TaskID,
		)
		if reopened.Error != nil {
			return TranslateError(reopened.Error)
		}
		if reopened.RowsAffected != 1 {
			return diagnosis.ErrTaskStateConflict
		}

		record := diagnosisTaskRecoveryRecord{
			ID: input.ID, TaskID: input.TaskID, RecoveredBy: input.RecoveredBy,
			IdempotencyKey: input.IdempotencyKey, Reason: input.Reason,
			PreviousErrorCode: task.LastErrorCode, PreviousErrorMessage: task.LastErrorMessage,
			PreviousAttemptCount: task.AttemptCount, TaskEventSeq: eventSeq,
			OutboxEventID: outbox.ID, CreatedAt: input.RecoveredAt,
		}
		if err := tx.Create(&record).Error; err != nil {
			return TranslateError(err)
		}
		result = diagnosis.TaskRecoveryResult{Recovery: record.toDomain(), Replayed: false}
		return nil
	})
	if err != nil {
		return diagnosis.TaskRecoveryResult{}, TranslateError(err)
	}
	return result, nil
}

type taskRecoveryStateRecord struct {
	ID                uuid.UUID            `gorm:"column:id"`
	Status            diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount      int                  `gorm:"column:attempt_count"`
	ClaimOwner        *string              `gorm:"column:claim_owner"`
	ClaimedAt         *time.Time           `gorm:"column:claimed_at"`
	LeaseUntil        *time.Time           `gorm:"column:lease_until"`
	CancelRequestedAt *time.Time           `gorm:"column:cancel_requested_at"`
	LastErrorCode     string               `gorm:"column:last_error_code"`
	LastErrorMessage  string               `gorm:"column:last_error_message"`
	ReportID          *uuid.UUID           `gorm:"column:report_id"`
	CreatorStatus     string               `gorm:"column:creator_status"`
}

func selectTaskRecoveryStateForUpdate(db *gorm.DB, taskID uuid.UUID) (taskRecoveryStateRecord, error) {
	var record taskRecoveryStateRecord
	result := db.Raw(`
SELECT task.id, task.status, task.attempt_count, task.claim_owner, task.claimed_at,
       task.lease_until, task.cancel_requested_at, task.last_error_code, task.last_error_message,
       report.id AS report_id, creator.status AS creator_status
FROM diagnosis_tasks task
JOIN users creator ON creator.id = task.created_by
LEFT JOIN diagnosis_reports report ON report.task_id = task.id
WHERE task.id = ?
FOR UPDATE OF task`, taskID).Scan(&record)
	if result.Error != nil {
		return taskRecoveryStateRecord{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 || record.ID == uuid.Nil {
		return taskRecoveryStateRecord{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return record, nil
}

func taskRecoveryDataSourcesAvailable(db *gorm.DB, taskID uuid.UUID) (bool, error) {
	var record struct {
		Total    int64 `gorm:"column:total"`
		Inactive int64 `gorm:"column:inactive"`
	}
	if err := db.Raw(`
SELECT COUNT(*) AS total,
       COUNT(*) FILTER (WHERE source.status <> 'active') AS inactive
FROM diagnosis_task_data_sources task_source
JOIN data_sources source ON source.id = task_source.data_source_id
WHERE task_source.task_id = ?`, taskID).Scan(&record).Error; err != nil {
		return false, TranslateError(err)
	}
	return record.Total > 0 && record.Inactive == 0, nil
}

type taskRecoveryOutboxRecord struct {
	ID uuid.UUID `gorm:"column:id"`
}

func selectTaskRecoveryOutboxForUpdate(db *gorm.DB, taskID uuid.UUID) (taskRecoveryOutboxRecord, error) {
	var records []taskRecoveryOutboxRecord
	result := db.Raw(`
SELECT id
FROM outbox_events
WHERE aggregate_type = 'diagnosis_task' AND aggregate_id = ?
  AND event_type = 'diagnosis.execute'
ORDER BY created_at, id
FOR UPDATE`, taskID).Scan(&records)
	if result.Error != nil {
		return taskRecoveryOutboxRecord{}, TranslateError(result.Error)
	}
	if len(records) != 1 || records[0].ID == uuid.Nil {
		return taskRecoveryOutboxRecord{}, fmt.Errorf("diagnosis task recovery requires exactly one execution outbox")
	}
	return records[0], nil
}

type diagnosisTaskRecoveryRecord struct {
	ID                   uuid.UUID `gorm:"column:id"`
	TaskID               uuid.UUID `gorm:"column:task_id"`
	RecoveredBy          uuid.UUID `gorm:"column:recovered_by"`
	IdempotencyKey       string    `gorm:"column:idempotency_key"`
	Reason               string    `gorm:"column:reason"`
	PreviousErrorCode    string    `gorm:"column:previous_error_code"`
	PreviousErrorMessage string    `gorm:"column:previous_error_message"`
	PreviousAttemptCount int       `gorm:"column:previous_attempt_count"`
	TaskEventSeq         int64     `gorm:"column:task_event_seq"`
	OutboxEventID        uuid.UUID `gorm:"column:outbox_event_id"`
	CreatedAt            time.Time `gorm:"column:created_at"`
}

func (diagnosisTaskRecoveryRecord) TableName() string { return "diagnosis_task_recoveries" }

func findTaskRecovery(
	db *gorm.DB,
	taskID, recoveredBy uuid.UUID,
	idempotencyKey string,
) (diagnosisTaskRecoveryRecord, bool, error) {
	var record diagnosisTaskRecoveryRecord
	result := db.Where(
		"task_id = ? AND recovered_by = ? AND idempotency_key = ?",
		taskID, recoveredBy, idempotencyKey,
	).Take(&record)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return diagnosisTaskRecoveryRecord{}, false, nil
	}
	if result.Error != nil {
		return diagnosisTaskRecoveryRecord{}, false, TranslateError(result.Error)
	}
	return record, true, nil
}

func (r diagnosisTaskRecoveryRecord) toDomain() diagnosis.TaskRecovery {
	return diagnosis.TaskRecovery{
		ID: r.ID, TaskID: r.TaskID, RecoveredBy: r.RecoveredBy,
		IdempotencyKey: r.IdempotencyKey, Reason: r.Reason,
		PreviousErrorCode: r.PreviousErrorCode, PreviousErrorMessage: r.PreviousErrorMessage,
		PreviousAttemptCount: r.PreviousAttemptCount, TaskEventSeq: r.TaskEventSeq,
		OutboxEventID: r.OutboxEventID, CreatedAt: r.CreatedAt.UTC(),
	}
}
