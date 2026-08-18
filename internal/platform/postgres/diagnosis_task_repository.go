package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DiagnosisTaskRepository 将任务创建所需的快照、任务、TaskEvent 和 Outbox
// 保存在同一个 PostgreSQL 事务中。
type DiagnosisTaskRepository struct {
	db *gorm.DB
}

var _ diagnosis.TaskRepository = (*DiagnosisTaskRepository)(nil)
var _ diagnosis.TaskExecutionRepository = (*DiagnosisTaskRepository)(nil)

func NewDiagnosisTaskRepository(db *gorm.DB) *DiagnosisTaskRepository {
	return &DiagnosisTaskRepository{db: db}
}

func (r *DiagnosisTaskRepository) CreateTask(
	ctx context.Context,
	input diagnosis.CreateTaskRecord,
) (diagnosis.TaskCreateResult, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskCreateResult{}, errors.New("diagnosis task repository is unavailable")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	var result diagnosis.TaskCreateResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, transactionContextKey{}, tx)
		created, err := r.createTaskInTx(txCtx, input)
		if err != nil {
			return err
		}
		result = created
		return nil
	})
	if err != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(err)
	}
	return result, nil
}

func (r *DiagnosisTaskRepository) createTaskInTx(
	ctx context.Context,
	input diagnosis.CreateTaskRecord,
) (diagnosis.TaskCreateResult, error) {
	db := ResolveDB(ctx, r.db)

	// 先做 Policy fail-closed 校验：新任务必须携带完整 Policy（非空 payload、
	// 非零且与 payload 内 schemaVersion 一致的列版本、严格 codec），任何缺失
	// 都在触碰任何表之前拒绝。旧授权体系（legacy/mode/request_scope）已硬切
	// 删除，不存在降级路径。
	if err := validateTaskInvestigationPolicy(input.InvestigationPolicy, input.InvestigationPolicySchemaVersion); err != nil {
		return diagnosis.TaskCreateResult{}, err
	}

	// 先读已有幂等事实，常见重放不创建新的快照。并发首次请求由
	// INSERT ... ON CONFLICT DO NOTHING 兜底，避免唯一键错误中止事务。
	var existing diagnosisTaskRecord
	lookup := db.Where("created_by = ? AND idempotency_key = ?", input.CreatedBy, input.IdempotencyKey).Take(&existing)
	if lookup.Error == nil {
		return r.replayOrConflict(ctx, existing, input.RequestFingerprint)
	}
	if err := TranslateError(lookup.Error); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return diagnosis.TaskCreateResult{}, err
	}

	// 锁住稳定的 external_cases 身份，保证同一工单的 snapshot_no 分配不会并发冲突。
	var externalCaseMarker struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	if err := db.Raw(`SELECT id FROM external_cases WHERE id = ? FOR UPDATE`, input.ExternalCaseID).Scan(&externalCaseMarker).Error; err != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(err)
	}
	if externalCaseMarker.ID == uuid.Nil {
		return diagnosis.TaskCreateResult{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}

	snapshotID, err := uuid.NewV7()
	if err != nil {
		return diagnosis.TaskCreateResult{}, fmt.Errorf("generate case snapshot id: %w", err)
	}
	taskID, err := uuid.NewV7()
	if err != nil {
		return diagnosis.TaskCreateResult{}, fmt.Errorf("generate diagnosis task id: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return diagnosis.TaskCreateResult{}, fmt.Errorf("generate outbox event id: %w", err)
	}

	var snapshotNo int
	if err := db.Raw(`
SELECT COALESCE(MAX(snapshot_no), 0) + 1
FROM case_snapshots
WHERE external_case_id = ?`, input.ExternalCaseID).Scan(&snapshotNo).Error; err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	if snapshotNo < 1 {
		snapshotNo = 1
	}

	snapshotSchemaVersion := input.Snapshot.PayloadSchemaVersion
	if snapshotSchemaVersion == 0 {
		snapshotSchemaVersion = 1
	}
	if err := db.Exec(`
INSERT INTO case_snapshots
    (id, external_case_id, snapshot_no, payload, payload_schema_version,
     content_hash, source_read_at, redaction_status, truncation_status, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshotID, input.ExternalCaseID, snapshotNo, input.Snapshot.Payload, snapshotSchemaVersion,
		input.Snapshot.ContentHash, input.Snapshot.SourceReadAt, input.Snapshot.RedactionStatus,
		input.Snapshot.TruncationStatus, input.CreatedBy, input.CreatedAt,
	).Error; err != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(err)
	}

	var insertedID uuid.UUID
	row := db.Raw(`
INSERT INTO diagnosis_tasks
    (id, created_by, external_case_id, case_snapshot_id, retry_of, idempotency_key,
     request_fingerprint, request_text,
     investigation_policy, investigation_policy_schema_version,
     status, attempt_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)
ON CONFLICT (created_by, idempotency_key) DO NOTHING
RETURNING id`,
		taskID, input.CreatedBy, input.ExternalCaseID, snapshotID, input.RetryOfTaskID,
		input.IdempotencyKey, input.RequestFingerprint, input.RequestText,
		input.InvestigationPolicy, input.InvestigationPolicySchemaVersion,
		input.CreatedAt, input.CreatedAt,
	).Row()
	scanErr := row.Scan(&insertedID)
	if errors.Is(scanErr, sql.ErrNoRows) {
		if err := db.Exec(`DELETE FROM case_snapshots WHERE id = ?`, snapshotID).Error; err != nil {
			return diagnosis.TaskCreateResult{}, err
		}
		var concurrent diagnosisTaskRecord
		if err := db.Where("created_by = ? AND idempotency_key = ?", input.CreatedBy, input.IdempotencyKey).Take(&concurrent).Error; err != nil {
			return diagnosis.TaskCreateResult{}, TranslateError(err)
		}
		return r.replayOrConflict(ctx, concurrent, input.RequestFingerprint)
	}
	if scanErr != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(scanErr)
	}
	if insertedID != taskID {
		return diagnosis.TaskCreateResult{}, errors.New("inserted diagnosis task id mismatch")
	}

	// 工单快照的数据源是每次诊断必然使用的 case source，不能只记录用户额外
	// 选择的证据库。00008 会为迁移前创建的任务做同样的回填。
	caseSourceResult := db.Exec(`
INSERT INTO diagnosis_task_data_sources
    (task_id, data_source_id, catalog_version_id, access_scope,
     access_scope_schema_version, confirmed_by, confirmed_at)
SELECT ?, external_case.data_source_id,
       (SELECT version.id
          FROM schema_catalog_versions version
         WHERE version.data_source_id = external_case.data_source_id
           AND version.status = 'published'
         ORDER BY version.version DESC
         LIMIT 1),
       '{}'::jsonb, 1, ?, ?
FROM external_cases external_case
JOIN data_sources source ON source.id = external_case.data_source_id
WHERE external_case.id = ? AND source.status = 'active'
ON CONFLICT (task_id, data_source_id) DO NOTHING`,
		taskID, input.CreatedBy, input.CreatedAt, input.ExternalCaseID,
	)
	if caseSourceResult.Error != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(caseSourceResult.Error)
	}
	if caseSourceResult.RowsAffected != 1 {
		return diagnosis.TaskCreateResult{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}

	for _, dataSourceID := range input.EvidenceDataSourceIDs {
		result := db.Exec(`
INSERT INTO diagnosis_task_data_sources
    (task_id, data_source_id, catalog_version_id, access_scope,
     access_scope_schema_version, confirmed_by, confirmed_at)
SELECT ?, ds.id,
       (SELECT version.id
          FROM schema_catalog_versions version
         WHERE version.data_source_id = ds.id
           AND version.status = 'published'
         ORDER BY version.version DESC
         LIMIT 1),
       '{}'::jsonb, 1, ?, ?
FROM data_sources ds
WHERE ds.id = ? AND ds.status = 'active'
ON CONFLICT (task_id, data_source_id) DO NOTHING`,
			taskID, input.CreatedBy, input.CreatedAt, dataSourceID,
		)
		if result.Error != nil {
			return diagnosis.TaskCreateResult{}, TranslateError(result.Error)
		}
		if result.RowsAffected == 0 {
			var associated bool
			if err := db.Raw(`
SELECT EXISTS (
    SELECT 1
    FROM diagnosis_task_data_sources task_source
    JOIN data_sources source ON source.id = task_source.data_source_id
    WHERE task_source.task_id = ? AND task_source.data_source_id = ? AND source.status = 'active'
)`, taskID, dataSourceID).Scan(&associated).Error; err != nil {
				return diagnosis.TaskCreateResult{}, TranslateError(err)
			}
			if associated {
				continue
			}
			return diagnosis.TaskCreateResult{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
		}
	}

	if len(input.Attachments) > 0 {
		if input.AttachmentSource == nil {
			return diagnosis.TaskCreateResult{}, diagnosis.ErrAttachmentContextRequired
		}
		for _, current := range input.Attachments {
			result := db.Exec(`
INSERT INTO diagnosis_task_attachments
    (task_id, attachment_id, source_message_id, purpose, created_at)
SELECT ?, item.id, message.id, ?, ?
FROM conversation_message_attachments reference
JOIN conversation_messages message
  ON message.id = reference.message_id AND message.conversation_id = reference.conversation_id
JOIN conversations conversation
  ON conversation.id = message.conversation_id AND conversation.user_id = ?
JOIN attachments item
  ON item.id = reference.attachment_id AND item.owner_user_id = conversation.user_id
WHERE reference.conversation_id = ? AND reference.message_id = ? AND reference.attachment_id = ?
  AND message.role = 'user' AND item.scope = 'session'
	  AND message.id = (
	      SELECT latest.id
	      FROM conversation_messages latest
	      WHERE latest.conversation_id = conversation.id
	      ORDER BY latest.seq DESC
	      LIMIT 1
	  )
  AND item.conversation_id = conversation.id AND item.processing_status = 'uploaded'`,
				taskID, current.Purpose, input.CreatedAt, input.CreatedBy,
				input.AttachmentSource.ConversationID, input.AttachmentSource.MessageID, current.AttachmentID,
			)
			if result.Error != nil {
				return diagnosis.TaskCreateResult{}, TranslateError(result.Error)
			}
			if result.RowsAffected != 1 {
				return diagnosis.TaskCreateResult{}, diagnosis.ErrTaskAttachmentForbidden
			}
		}
	}

	taskCreatedPayload, err := json.Marshal(map[string]any{
		"taskId":         taskID.String(),
		"caseSnapshotId": snapshotID.String(),
		"status":         string(diagnosis.TaskPending),
	})
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	if err := db.Exec(`
INSERT INTO task_events
    (task_id, seq, event_type, payload, payload_schema_version, created_at)
VALUES (?, 1, ?, ?, 1, ?)`, taskID, diagnosis.TaskEventCreated, taskCreatedPayload, input.CreatedAt).Error; err != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(err)
	}

	outboxPayload, err := json.Marshal(map[string]any{"taskId": taskID.String()})
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	if err := db.Exec(`
INSERT INTO outbox_events
    (id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
     payload, payload_schema_version, attempt_count, available_at, requeue_count, created_at)
VALUES (?, 'diagnosis.execute', 'diagnosis_task', ?, ?, NULL, ?, 1, 0, ?, 0, ?)`,
		outboxID, taskID, input.CorrelationID, outboxPayload, input.CreatedAt, input.CreatedAt,
	).Error; err != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(err)
	}

	task, err := diagnosisTaskFromRecord(diagnosisTaskRecord{
		ID: taskID, CreatedBy: input.CreatedBy, ExternalCaseID: input.ExternalCaseID,
		CaseSnapshotID: snapshotID, RetryOfTaskID: input.RetryOfTaskID,
		RequestText: input.RequestText,
		Status:      diagnosis.TaskPending, AttemptCount: 0,
		CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt,
	})
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	task.Attachments, err = r.loadTaskAttachments(ctx, taskID)
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	return diagnosis.TaskCreateResult{Task: task, Replayed: false}, nil
}

func (r *DiagnosisTaskRepository) replayOrConflict(
	ctx context.Context,
	record diagnosisTaskRecord,
	requestFingerprint string,
) (diagnosis.TaskCreateResult, error) {
	if record.RequestFingerprint != requestFingerprint {
		return diagnosis.TaskCreateResult{}, diagnosis.ErrIdempotencyConflict
	}
	task, err := diagnosisTaskFromRecord(record)
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	task.Attachments, err = r.loadTaskAttachments(ctx, record.ID)
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	return diagnosis.TaskCreateResult{Task: task, Replayed: true}, nil
}

func (r *DiagnosisTaskRepository) GetTask(ctx context.Context, taskID uuid.UUID) (diagnosis.DiagnosisTask, error) {
	if r == nil || r.db == nil {
		return diagnosis.DiagnosisTask{}, errors.New("diagnosis task repository is unavailable")
	}
	if taskID == uuid.Nil {
		return diagnosis.DiagnosisTask{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	var record diagnosisTaskRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT task.id, task.created_by, task.external_case_id, task.case_snapshot_id,
       task.retry_of, task.request_text, task.status, task.attempt_count,
       task.last_error_code, task.last_error_message, task.started_at,
       task.completed_at, task.created_at, task.updated_at, report.id AS report_id,
       task.request_fingerprint
FROM diagnosis_tasks task
LEFT JOIN diagnosis_reports report ON report.task_id = task.id
WHERE task.id = ?`, taskID).Scan(&record)
	if result.Error != nil {
		return diagnosis.DiagnosisTask{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return diagnosis.DiagnosisTask{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	task, err := diagnosisTaskFromRecord(record)
	if err != nil {
		return diagnosis.DiagnosisTask{}, err
	}
	task.Attachments, err = r.loadTaskAttachments(ctx, taskID)
	if err != nil {
		return diagnosis.DiagnosisTask{}, err
	}
	return task, nil
}

func (r *DiagnosisTaskRepository) loadTaskAttachments(
	ctx context.Context,
	taskID uuid.UUID,
) ([]diagnosis.TaskAttachmentSummary, error) {
	var records []diagnosisTaskAttachmentRecord
	if err := ResolveDB(ctx, r.db).Raw(`
SELECT reference.attachment_id, reference.source_message_id, reference.purpose,
       item.original_filename, item.content_type, item.size_bytes, item.content_sha256
FROM diagnosis_task_attachments reference
JOIN attachments item ON item.id = reference.attachment_id
WHERE reference.task_id = ?
ORDER BY reference.created_at, reference.attachment_id`, taskID).Scan(&records).Error; err != nil {
		return nil, TranslateError(err)
	}
	result := make([]diagnosis.TaskAttachmentSummary, 0, len(records))
	for _, record := range records {
		result = append(result, diagnosis.TaskAttachmentSummary{
			AttachmentID: record.AttachmentID, SourceMessageID: record.SourceMessageID,
			Purpose: record.Purpose, OriginalName: record.OriginalName, MediaType: record.MediaType,
			SizeBytes: record.SizeBytes, ContentSHA256: record.ContentSHA256,
		})
	}
	return result, nil
}

func (r *DiagnosisTaskRepository) ListTaskEvents(
	ctx context.Context,
	taskID uuid.UUID,
	afterSeq int64,
	limit int,
) (diagnosis.TaskEventPage, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskEventPage{}, errors.New("diagnosis task repository is unavailable")
	}
	if taskID == uuid.Nil || afterSeq < 0 || limit < 1 || limit > diagnosis.MaxTaskEventLimit {
		return diagnosis.TaskEventPage{}, diagnosis.ErrInvalidTask
	}
	var records []taskEventRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT task_id, seq, event_type, payload, payload_schema_version, created_at
FROM task_events
WHERE task_id = ? AND seq > ?
ORDER BY seq ASC
LIMIT ?`, taskID, afterSeq, limit+1).Scan(&records)
	if result.Error != nil {
		return diagnosis.TaskEventPage{}, TranslateError(result.Error)
	}

	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	items := make([]diagnosis.TaskEvent, 0, len(records))
	nextAfterSeq := afterSeq
	for _, record := range records {
		item, err := taskEventFromRecord(record)
		if err != nil {
			return diagnosis.TaskEventPage{}, err
		}
		items = append(items, item)
		nextAfterSeq = item.Seq
	}
	return diagnosis.TaskEventPage{
		Items: items, AfterSeq: afterSeq, NextAfterSeq: nextAfterSeq, HasMore: hasMore,
	}, nil
}

func (r *DiagnosisTaskRepository) CancelTask(
	ctx context.Context,
	taskID, requestedBy uuid.UUID,
	requestedAt time.Time,
) (diagnosis.TaskCancelResult, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskCancelResult{}, errors.New("diagnosis task repository is unavailable")
	}
	if taskID == uuid.Nil || requestedBy == uuid.Nil {
		return diagnosis.TaskCancelResult{}, diagnosis.ErrInvalidTask
	}
	requestedAt = requestedAt.UTC()
	var result diagnosis.TaskCancelResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		record, err := selectDiagnosisTaskForUpdate(tx, taskID)
		if err != nil {
			return err
		}
		switch record.Status {
		case diagnosis.TaskCancelRequested, diagnosis.TaskCancelled:
			task, err := diagnosisTaskFromRecord(record)
			if err != nil {
				return err
			}
			result = diagnosis.TaskCancelResult{Task: task, Changed: false}
			return nil
		case diagnosis.TaskSucceeded, diagnosis.TaskFailed:
			return diagnosis.ErrTaskStateConflict
		case diagnosis.TaskPending, diagnosis.TaskRunning:
		default:
			return fmt.Errorf("unsupported diagnosis task status %q", record.Status)
		}

		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = 'cancel_requested', cancel_requested_at = ?, updated_at = ?
WHERE id = ? AND status = ?`, requestedAt, requestedAt, taskID, record.Status)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosis.ErrTaskStateConflict
		}
		if err := appendTaskEvent(tx, taskID, diagnosis.TaskEventCancelRequested, map[string]any{
			"taskId": taskID.String(), "status": string(diagnosis.TaskCancelRequested),
			"requestedBy": requestedBy.String(),
		}, requestedAt); err != nil {
			return err
		}

		record.Status = diagnosis.TaskCancelRequested
		record.UpdatedAt = requestedAt
		task, err := diagnosisTaskFromRecord(record)
		if err != nil {
			return err
		}
		result = diagnosis.TaskCancelResult{Task: task, Changed: true}
		return nil
	})
	if err != nil {
		return diagnosis.TaskCancelResult{}, TranslateError(err)
	}
	return result, nil
}

func (r *DiagnosisTaskRepository) ClaimTask(ctx context.Context, input diagnosis.TaskClaimRecord) (diagnosis.TaskClaimResult, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskClaimResult{}, errors.New("diagnosis task repository is unavailable")
	}
	if input.TaskID == uuid.Nil || input.ClaimOwner == "" || !input.LeaseUntil.After(input.ClaimedAt) {
		return diagnosis.TaskClaimResult{}, diagnosis.ErrInvalidTask
	}
	input.ClaimedAt = input.ClaimedAt.UTC()
	input.LeaseUntil = input.LeaseUntil.UTC()

	var result diagnosis.TaskClaimResult
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		state, err := selectTaskClaimStateForUpdate(tx, input.TaskID)
		if err != nil {
			return err
		}
		switch state.Status {
		case diagnosis.TaskSucceeded, diagnosis.TaskFailed, diagnosis.TaskCancelled:
			result = diagnosis.TaskClaimResult{Disposition: diagnosis.TaskClaimTerminal, Status: state.Status}
			return nil
		case diagnosis.TaskCancelRequested:
			result = diagnosis.TaskClaimResult{Disposition: diagnosis.TaskClaimCancellationRequested, Status: state.Status}
			return nil
		case diagnosis.TaskRunning:
			if state.LeaseUntil != nil && state.LeaseUntil.After(input.ClaimedAt) {
				result = diagnosis.TaskClaimResult{Disposition: diagnosis.TaskClaimLeaseHeld, Status: state.Status}
				return nil
			}
		case diagnosis.TaskPending:
		default:
			return fmt.Errorf("unsupported diagnosis task status %q", state.Status)
		}

		attemptCount := state.AttemptCount + 1
		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = 'running', attempt_count = ?, claim_owner = ?, claimed_at = ?,
    lease_until = ?, started_at = COALESCE(started_at, ?), updated_at = ?
WHERE id = ? AND status = ? AND attempt_count = ?`,
			attemptCount, input.ClaimOwner, input.ClaimedAt, input.LeaseUntil,
			input.ClaimedAt, input.ClaimedAt, input.TaskID, state.Status, state.AttemptCount,
		)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosis.ErrTaskStateConflict
		}
		eventType := diagnosis.TaskEventStarted
		if state.AttemptCount > 0 {
			eventType = diagnosis.TaskEventReclaimed
		}
		if err := appendTaskEvent(tx, input.TaskID, eventType, map[string]any{
			"taskId": input.TaskID.String(), "status": string(diagnosis.TaskRunning),
			"attemptCount": attemptCount,
		}, input.ClaimedAt); err != nil {
			return err
		}
		lease := diagnosis.TaskLease{
			TaskID: input.TaskID, ClaimOwner: input.ClaimOwner,
			AttemptCount: attemptCount, LeaseUntil: input.LeaseUntil,
		}
		result = diagnosis.TaskClaimResult{
			Disposition: diagnosis.TaskClaimAcquired, Status: diagnosis.TaskRunning, Lease: &lease,
		}
		return nil
	})
	if err != nil {
		return diagnosis.TaskClaimResult{}, TranslateError(err)
	}
	return result, nil
}

func (r *DiagnosisTaskRepository) RenewTaskLease(ctx context.Context, input diagnosis.TaskLeaseRenewal) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("diagnosis task repository is unavailable")
	}
	if input.TaskID == uuid.Nil || input.ClaimOwner == "" || input.AttemptCount < 1 ||
		!input.NewLeaseUntil.After(input.RenewedAt) {
		return false, diagnosis.ErrInvalidTask
	}
	result := ResolveDB(ctx, r.db).Exec(`
UPDATE diagnosis_tasks
SET lease_until = ?, updated_at = ?
WHERE id = ? AND status = 'running' AND claim_owner = ? AND attempt_count = ?
  AND lease_until > ?`, input.NewLeaseUntil.UTC(), input.RenewedAt.UTC(), input.TaskID,
		input.ClaimOwner, input.AttemptCount, input.RenewedAt.UTC())
	if result.Error != nil {
		return false, TranslateError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func selectDiagnosisTaskForUpdate(db *gorm.DB, taskID uuid.UUID) (diagnosisTaskRecord, error) {
	var record diagnosisTaskRecord
	result := db.Raw(`
SELECT id, created_by, external_case_id, case_snapshot_id, retry_of,
       request_fingerprint, request_text,
       status, attempt_count, last_error_code, last_error_message, started_at,
       completed_at, created_at, updated_at
FROM diagnosis_tasks
WHERE id = ?
FOR UPDATE`, taskID).Scan(&record)
	if result.Error != nil {
		return diagnosisTaskRecord{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return diagnosisTaskRecord{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return record, nil
}

type taskClaimStateRecord struct {
	ID           uuid.UUID            `gorm:"column:id"`
	Status       diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount int                  `gorm:"column:attempt_count"`
	LeaseUntil   *time.Time           `gorm:"column:lease_until"`
}

func selectTaskClaimStateForUpdate(db *gorm.DB, taskID uuid.UUID) (taskClaimStateRecord, error) {
	var record taskClaimStateRecord
	result := db.Raw(`
SELECT id, status, attempt_count, lease_until
FROM diagnosis_tasks
WHERE id = ?
FOR UPDATE`, taskID).Scan(&record)
	if result.Error != nil {
		return taskClaimStateRecord{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return taskClaimStateRecord{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
	}
	return record, nil
}

func appendTaskEvent(db *gorm.DB, taskID uuid.UUID, eventType diagnosis.TaskEventType, payload map[string]any, createdAt time.Time) error {
	_, err := appendTaskEventWithSeq(db, taskID, eventType, payload, createdAt)
	return err
}

func appendTaskEventWithSeq(db *gorm.DB, taskID uuid.UUID, eventType diagnosis.TaskEventType, payload map[string]any, createdAt time.Time) (int64, error) {
	var nextSeq int64
	if err := db.Raw(`
SELECT COALESCE(MAX(seq), 0) + 1
FROM task_events
WHERE task_id = ?`, taskID).Scan(&nextSeq).Error; err != nil {
		return 0, TranslateError(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal task event payload: %w", err)
	}
	if err := db.Exec(`
INSERT INTO task_events
    (task_id, seq, event_type, payload, payload_schema_version, created_at)
VALUES (?, ?, ?, ?, 1, ?)`, taskID, nextSeq, eventType, encoded, createdAt.UTC()).Error; err != nil {
		return 0, TranslateError(err)
	}
	return nextSeq, nil
}

type taskEventRecord struct {
	TaskID               uuid.UUID               `gorm:"column:task_id"`
	Seq                  int64                   `gorm:"column:seq"`
	EventType            diagnosis.TaskEventType `gorm:"column:event_type"`
	Payload              []byte                  `gorm:"column:payload"`
	PayloadSchemaVersion int                     `gorm:"column:payload_schema_version"`
	CreatedAt            time.Time               `gorm:"column:created_at"`
}

func taskEventFromRecord(record taskEventRecord) (diagnosis.TaskEvent, error) {
	payload := map[string]any{}
	if len(record.Payload) > 0 {
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return diagnosis.TaskEvent{}, fmt.Errorf("decode task event payload: %w", err)
		}
	}
	return diagnosis.TaskEvent{
		TaskID: record.TaskID, Seq: record.Seq, EventType: record.EventType,
		Payload: payload, PayloadSchemaVersion: record.PayloadSchemaVersion,
		CreatedAt: record.CreatedAt,
	}, nil
}

// taskListItemRecord 是任务列表查询的行映射，额外携带快照中的工单身份。
// 注意：不能以内嵌 diagnosisTaskRecord 的方式扫描——GORM 对带 TableName()
// 方法的匿名内嵌结构会按关联关系处理而不是平铺字段，导致 Raw().Scan()
// 后任务侧字段全部为空；因此这里显式平铺所有列。
type taskListItemRecord struct {
	ID                uuid.UUID            `gorm:"column:id"`
	CreatedBy         uuid.UUID            `gorm:"column:created_by"`
	ExternalCaseID    uuid.UUID            `gorm:"column:external_case_id"`
	CaseSnapshotID    uuid.UUID            `gorm:"column:case_snapshot_id"`
	RetryOfTaskID     *uuid.UUID           `gorm:"column:retry_of"`
	RequestText       string               `gorm:"column:request_text"`
	Status            diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount      int                  `gorm:"column:attempt_count"`
	LastErrorCode     string               `gorm:"column:last_error_code"`
	LastErrorMessage  string               `gorm:"column:last_error_message"`
	StartedAt         *time.Time           `gorm:"column:started_at"`
	CompletedAt       *time.Time           `gorm:"column:completed_at"`
	CreatedAt         time.Time            `gorm:"column:created_at"`
	UpdatedAt         time.Time            `gorm:"column:updated_at"`
	ReportID          string               `gorm:"column:report_id"`
	ExternalCaseKey   string               `gorm:"column:external_case_key"`
	ExternalCaseTitle string               `gorm:"column:external_case_title"`
}

// toDiagnosisTask 将平铺的任务列表行转换为领域摘要。
func (r taskListItemRecord) toDiagnosisTask() (diagnosis.DiagnosisTask, error) {
	return diagnosisTaskFromRecord(diagnosisTaskRecord{
		ID: r.ID, CreatedBy: r.CreatedBy, ExternalCaseID: r.ExternalCaseID,
		CaseSnapshotID: r.CaseSnapshotID, RetryOfTaskID: r.RetryOfTaskID,
		RequestText: r.RequestText, Status: r.Status,
		AttemptCount: r.AttemptCount, LastErrorCode: r.LastErrorCode,
		LastErrorMessage: r.LastErrorMessage, StartedAt: r.StartedAt,
		CompletedAt: r.CompletedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		ReportID: r.ReportID,
	})
}

const taskListSelectColumns = `
SELECT task.id, task.created_by, task.external_case_id, task.case_snapshot_id,
       task.retry_of, task.request_text, task.status, task.attempt_count,
       task.last_error_code, task.last_error_message, task.started_at,
       task.completed_at, task.created_at, task.updated_at, report.id AS report_id,
       snapshot.payload->>'externalCaseKey' AS external_case_key,
       snapshot.payload->>'title' AS external_case_title
FROM diagnosis_tasks task
JOIN case_snapshots snapshot ON snapshot.id = task.case_snapshot_id
LEFT JOIN diagnosis_reports report ON report.task_id = task.id`

const taskListCountQuery = `
SELECT count(*)
FROM diagnosis_tasks task
JOIN case_snapshots snapshot ON snapshot.id = task.case_snapshot_id
LEFT JOIN diagnosis_reports report ON report.task_id = task.id`

// ListTasks 返回当前 Actor 可见的分页任务列表。
// 非管理员强制按创建人过滤；管理员可查看全部任务。按创建时间倒序。
func (r *DiagnosisTaskRepository) ListTasks(
	ctx context.Context,
	query diagnosis.TaskListQuery,
) (diagnosis.TaskListPage, error) {
	if r == nil || r.db == nil {
		return diagnosis.TaskListPage{}, errors.New("diagnosis task repository is unavailable")
	}
	if query.Actor.UserID == uuid.Nil {
		return diagnosis.TaskListPage{}, diagnosis.ErrTaskForbidden
	}
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = diagnosis.DefaultTaskListPageSize
	} else if pageSize > diagnosis.MaxTaskListPageSize {
		pageSize = diagnosis.MaxTaskListPageSize
	}

	where := " WHERE 1 = 1"
	args := make([]any, 0, 3)
	if !query.Actor.IsAdmin {
		where += " AND task.created_by = ?"
		args = append(args, query.Actor.UserID)
	}
	if query.Status != nil {
		where += " AND task.status = ?"
		args = append(args, *query.Status)
	}
	if query.ExternalCaseID != nil && *query.ExternalCaseID != uuid.Nil {
		where += " AND task.external_case_id = ?"
		args = append(args, *query.ExternalCaseID)
	}

	db := ResolveDB(ctx, r.db)
	var total int64
	if err := db.Raw(taskListCountQuery+where, args...).Scan(&total).Error; err != nil {
		return diagnosis.TaskListPage{}, TranslateError(err)
	}

	var records []taskListItemRecord
	if err := db.Raw(
		taskListSelectColumns+where+" ORDER BY task.created_at DESC LIMIT ? OFFSET ?",
		append(args, pageSize, (page-1)*pageSize)...,
	).Scan(&records).Error; err != nil {
		return diagnosis.TaskListPage{}, TranslateError(err)
	}

	items := make([]diagnosis.TaskListItem, 0, len(records))
	for _, record := range records {
		task, err := record.toDiagnosisTask()
		if err != nil {
			return diagnosis.TaskListPage{}, err
		}
		items = append(items, diagnosis.TaskListItem{
			Task: task, ExternalCaseKey: record.ExternalCaseKey, ExternalCaseTitle: record.ExternalCaseTitle,
		})
	}
	return diagnosis.TaskListPage{
		Items: items, Total: total, Page: page, PageSize: pageSize,
	}, nil
}

// diagnosisTaskRecord 是 PostgreSQL 行映射，避免领域包依赖 GORM 标签和数据库类型。
type diagnosisTaskRecord struct {
	ID                 uuid.UUID            `gorm:"column:id"`
	CreatedBy          uuid.UUID            `gorm:"column:created_by"`
	ExternalCaseID     uuid.UUID            `gorm:"column:external_case_id"`
	CaseSnapshotID     uuid.UUID            `gorm:"column:case_snapshot_id"`
	RetryOfTaskID      *uuid.UUID           `gorm:"column:retry_of"`
	IdempotencyKey     string               `gorm:"column:idempotency_key"`
	RequestFingerprint string               `gorm:"column:request_fingerprint"`
	RequestText        string               `gorm:"column:request_text"`
	Status             diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount       int                  `gorm:"column:attempt_count"`
	LastErrorCode      string               `gorm:"column:last_error_code"`
	LastErrorMessage   string               `gorm:"column:last_error_message"`
	StartedAt          *time.Time           `gorm:"column:started_at"`
	CompletedAt        *time.Time           `gorm:"column:completed_at"`
	CreatedAt          time.Time            `gorm:"column:created_at"`
	UpdatedAt          time.Time            `gorm:"column:updated_at"`
	ReportID           string               `gorm:"column:report_id"`
}

type diagnosisTaskAttachmentRecord struct {
	AttachmentID    uuid.UUID `gorm:"column:attachment_id"`
	SourceMessageID uuid.UUID `gorm:"column:source_message_id"`
	Purpose         string    `gorm:"column:purpose"`
	OriginalName    string    `gorm:"column:original_filename"`
	MediaType       string    `gorm:"column:content_type"`
	SizeBytes       int64     `gorm:"column:size_bytes"`
	ContentSHA256   string    `gorm:"column:content_sha256"`
}

func (diagnosisTaskRecord) TableName() string { return "diagnosis_tasks" }

// diagnosisTaskFromRecord 将数据库行转换为领域摘要。
func diagnosisTaskFromRecord(record diagnosisTaskRecord) (diagnosis.DiagnosisTask, error) {
	var reportID *uuid.UUID
	if record.ReportID != "" {
		if parsed, err := uuid.Parse(record.ReportID); err == nil {
			reportID = &parsed
		}
	}
	return diagnosis.DiagnosisTask{
		ID: record.ID, CreatedBy: record.CreatedBy, ExternalCaseID: record.ExternalCaseID,
		CaseSnapshotID: record.CaseSnapshotID, RetryOfTaskID: record.RetryOfTaskID,
		RequestText: record.RequestText, Status: record.Status,
		AttemptCount: record.AttemptCount, LastErrorCode: record.LastErrorCode,
		LastErrorMessage: record.LastErrorMessage, StartedAt: record.StartedAt,
		CompletedAt: record.CompletedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		ReportID: reportID,
	}, nil
}
