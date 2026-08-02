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

	// 先读已有幂等事实，常见重放不创建新的快照。并发首次请求由
	// INSERT ... ON CONFLICT DO NOTHING 兜底，避免唯一键错误中止事务。
	var existing diagnosisTaskRecord
	lookup := db.Where("created_by = ? AND idempotency_key = ?", input.CreatedBy, input.IdempotencyKey).Take(&existing)
	if lookup.Error == nil {
		return replayOrConflict(existing, input.RequestFingerprint)
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
     request_fingerprint, request_text, request_scope, request_scope_schema_version,
     status, attempt_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)
ON CONFLICT (created_by, idempotency_key) DO NOTHING
RETURNING id`,
		taskID, input.CreatedBy, input.ExternalCaseID, snapshotID, input.RetryOfTaskID,
		input.IdempotencyKey, input.RequestFingerprint, input.RequestText, input.RequestScope,
		input.RequestScopeSchemaVersion, input.CreatedAt, input.CreatedAt,
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
		return replayOrConflict(concurrent, input.RequestFingerprint)
	}
	if scanErr != nil {
		return diagnosis.TaskCreateResult{}, TranslateError(scanErr)
	}
	if insertedID != taskID {
		return diagnosis.TaskCreateResult{}, errors.New("inserted diagnosis task id mismatch")
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
WHERE ds.id = ? AND ds.status = 'active'`,
			taskID, input.CreatedBy, input.CreatedAt, dataSourceID,
		)
		if result.Error != nil {
			return diagnosis.TaskCreateResult{}, TranslateError(result.Error)
		}
		if result.RowsAffected != 1 {
			return diagnosis.TaskCreateResult{}, repository.Wrap(repository.ErrNotFound, gorm.ErrRecordNotFound)
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
VALUES (?, 1, 'task_created', ?, 1, ?)`, taskID, taskCreatedPayload, input.CreatedAt).Error; err != nil {
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
		RequestText: input.RequestText, RequestScope: input.RequestScope,
		RequestScopeSchemaVersion: input.RequestScopeSchemaVersion,
		Status:                    diagnosis.TaskPending, AttemptCount: 0,
		CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt,
	})
	if err != nil {
		return diagnosis.TaskCreateResult{}, err
	}
	return diagnosis.TaskCreateResult{Task: task, Replayed: false}, nil
}

func replayOrConflict(record diagnosisTaskRecord, requestFingerprint string) (diagnosis.TaskCreateResult, error) {
	if record.RequestFingerprint != requestFingerprint {
		return diagnosis.TaskCreateResult{}, diagnosis.ErrIdempotencyConflict
	}
	task, err := diagnosisTaskFromRecord(record)
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
       task.retry_of, task.request_text, task.request_scope,
       task.request_scope_schema_version, task.status, task.attempt_count,
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
	return diagnosisTaskFromRecord(record)
}

// diagnosisTaskRecord 是 PostgreSQL 行映射，避免领域包依赖 GORM 标签和数据库类型。
type diagnosisTaskRecord struct {
	ID                        uuid.UUID            `gorm:"column:id"`
	CreatedBy                 uuid.UUID            `gorm:"column:created_by"`
	ExternalCaseID            uuid.UUID            `gorm:"column:external_case_id"`
	CaseSnapshotID            uuid.UUID            `gorm:"column:case_snapshot_id"`
	RetryOfTaskID             *uuid.UUID           `gorm:"column:retry_of"`
	IdempotencyKey            string               `gorm:"column:idempotency_key"`
	RequestFingerprint        string               `gorm:"column:request_fingerprint"`
	RequestText               string               `gorm:"column:request_text"`
	RequestScope              []byte               `gorm:"column:request_scope"`
	RequestScopeSchemaVersion int                  `gorm:"column:request_scope_schema_version"`
	Status                    diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount              int                  `gorm:"column:attempt_count"`
	LastErrorCode             string               `gorm:"column:last_error_code"`
	LastErrorMessage          string               `gorm:"column:last_error_message"`
	StartedAt                 *time.Time           `gorm:"column:started_at"`
	CompletedAt               *time.Time           `gorm:"column:completed_at"`
	CreatedAt                 time.Time            `gorm:"column:created_at"`
	UpdatedAt                 time.Time            `gorm:"column:updated_at"`
	ReportID                  string               `gorm:"column:report_id"`
}

func (diagnosisTaskRecord) TableName() string { return "diagnosis_tasks" }

// diagnosisTaskFromRecord 将数据库行转换为领域摘要，并对 JSONB 解码失败 fail closed。
func diagnosisTaskFromRecord(record diagnosisTaskRecord) (diagnosis.DiagnosisTask, error) {
	scope := map[string]any{}
	if len(record.RequestScope) > 0 {
		if err := json.Unmarshal(record.RequestScope, &scope); err != nil {
			return diagnosis.DiagnosisTask{}, fmt.Errorf("decode diagnosis task request scope: %w", err)
		}
	}
	var reportID *uuid.UUID
	if record.ReportID != "" {
		if parsed, err := uuid.Parse(record.ReportID); err == nil {
			reportID = &parsed
		}
	}
	return diagnosis.DiagnosisTask{
		ID: record.ID, CreatedBy: record.CreatedBy, ExternalCaseID: record.ExternalCaseID,
		CaseSnapshotID: record.CaseSnapshotID, RetryOfTaskID: record.RetryOfTaskID,
		RequestText: record.RequestText, RequestScope: scope,
		RequestScopeSchemaVersion: record.RequestScopeSchemaVersion, Status: record.Status,
		AttemptCount: record.AttemptCount, LastErrorCode: record.LastErrorCode,
		LastErrorMessage: record.LastErrorMessage, StartedAt: record.StartedAt,
		CompletedAt: record.CompletedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		ReportID: reportID,
	}, nil
}
