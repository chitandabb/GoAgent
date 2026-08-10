package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/diagnosisworker"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DiagnosisWorkerRepository struct {
	db *gorm.DB
}

var _ diagnosisworker.Repository = (*DiagnosisWorkerRepository)(nil)

func NewDiagnosisWorkerRepository(db *gorm.DB) *DiagnosisWorkerRepository {
	return &DiagnosisWorkerRepository{db: db}
}

func (r *DiagnosisWorkerRepository) LoadTask(
	ctx context.Context,
	lease diagnosis.TaskLease,
	now time.Time,
) (diagnosisworker.Task, error) {
	if r == nil || r.db == nil {
		return diagnosisworker.Task{}, errors.New("diagnosis worker repository is unavailable")
	}
	var record workerTaskRecord
	result := ResolveDB(ctx, r.db).Raw(`
SELECT task.id, task.created_by, task.request_text, task.request_scope,
       task.external_case_id, snapshot.payload AS case_snapshot,
       user_record.role, user_record.status AS user_status
FROM diagnosis_tasks task
JOIN users user_record ON user_record.id = task.created_by
JOIN case_snapshots snapshot ON snapshot.id = task.case_snapshot_id
WHERE task.id = ? AND task.status = 'running' AND task.claim_owner = ?
  AND task.attempt_count = ? AND task.lease_until > ?`,
		lease.TaskID, lease.ClaimOwner, lease.AttemptCount, now.UTC(),
	).Scan(&record)
	if result.Error != nil {
		return diagnosisworker.Task{}, TranslateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return diagnosisworker.Task{}, diagnosisworker.ErrLeaseLost
	}
	if record.UserStatus != string(auth.UserStatusActive) || !record.Role.Valid() {
		return diagnosisworker.Task{}, diagnosis.ErrTaskForbidden
	}
	requestScope := map[string]any{}
	if err := json.Unmarshal(record.RequestScope, &requestScope); err != nil {
		return diagnosisworker.Task{}, fmt.Errorf("decode worker request scope: %w", diagnosis.ErrInvalidTask)
	}
	caseSnapshot, err := diagnosis.DecodeCaseSnapshot(record.CaseSnapshot)
	if err != nil {
		return diagnosisworker.Task{}, err
	}
	if caseSnapshot.ID != record.ExternalCaseID {
		return diagnosisworker.Task{}, diagnosis.ErrInvalidTaskSnapshot
	}

	var sourceRecords []workerDataSourceRecord
	if err := ResolveDB(ctx, r.db).Raw(`
SELECT source.id, source.source_role, source.safety_mode
FROM diagnosis_task_data_sources task_source
JOIN data_sources source ON source.id = task_source.data_source_id
WHERE task_source.task_id = ? AND source.status = 'active'
ORDER BY source.id`, lease.TaskID).Scan(&sourceRecords).Error; err != nil {
		return diagnosisworker.Task{}, TranslateError(err)
	}
	dataSources := make([]diagnosisworker.DataSource, 0, len(sourceRecords))
	seenCaseSource := false
	for _, source := range sourceRecords {
		item := diagnosisworker.DataSource{
			ID: source.ID, Role: agent.DataSourceRole(source.Role),
			SafetyMode: agent.DataSourceSafetyMode(source.SafetyMode),
		}
		if err := (agent.ScopedDataSource{
			ID: item.ID, Role: item.Role, SafetyMode: item.SafetyMode,
		}).Validate(); err != nil {
			return diagnosisworker.Task{}, fmt.Errorf("%w: data source scope: %v", diagnosis.ErrInvalidTask, err)
		}
		if item.ID == caseSnapshot.DataSourceID && item.Role == agent.DataSourceRoleCaseSource {
			seenCaseSource = true
		}
		dataSources = append(dataSources, item)
	}
	if !seenCaseSource {
		return diagnosisworker.Task{}, fmt.Errorf("%w: case source is not active or not bound", diagnosis.ErrInvalidTask)
	}
	var attachmentRecords []workerTaskAttachmentRecord
	if err := ResolveDB(ctx, r.db).Raw(`
SELECT reference.attachment_id, reference.purpose, item.original_filename,
       item.content_type, item.size_bytes, item.content_sha256
FROM diagnosis_task_attachments reference
JOIN attachments item ON item.id = reference.attachment_id
WHERE reference.task_id = ? AND item.owner_user_id = ? AND item.processing_status = 'uploaded'
ORDER BY reference.created_at, reference.attachment_id`, lease.TaskID, record.CreatedBy).Scan(&attachmentRecords).Error; err != nil {
		return diagnosisworker.Task{}, TranslateError(err)
	}
	attachments := make([]diagnosisworker.TaskAttachment, 0, len(attachmentRecords))
	for _, current := range attachmentRecords {
		attachments = append(attachments, diagnosisworker.TaskAttachment{
			ID: current.AttachmentID, Purpose: current.Purpose, OriginalName: current.OriginalName,
			MediaType: current.MediaType, SizeBytes: current.SizeBytes, ContentSHA256: current.ContentSHA256,
		})
	}
	capabilities, err := diagnosis.TaskCapabilitiesFromRequestScope(requestScope)
	if err != nil {
		return diagnosisworker.Task{}, fmt.Errorf("%w: attachment capability: %v", diagnosis.ErrInvalidTask, err)
	}
	hasAttachmentCapability := false
	for _, capability := range capabilities {
		if capability == diagnosis.TaskCapabilityAttachment {
			hasAttachmentCapability = true
			break
		}
	}
	if hasAttachmentCapability != (len(attachments) > 0) {
		return diagnosisworker.Task{}, fmt.Errorf("%w: attachment scope and frozen task attachments disagree", diagnosis.ErrInvalidTask)
	}
	return diagnosisworker.Task{
		ID: record.ID, CreatedBy: record.CreatedBy, Role: record.Role,
		RequestText: record.RequestText, RequestScope: requestScope,
		CaseSnapshot: caseSnapshot, DataSources: dataSources, Attachments: attachments,
	}, nil
}

func (r *DiagnosisWorkerRepository) Complete(
	ctx context.Context,
	lease diagnosis.TaskLease,
	result diagnosisworker.ExecutionResult,
	completedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("diagnosis worker repository is unavailable")
	}
	if err := validateExecutionResult(result); err != nil {
		return false, err
	}
	completedAt = completedAt.UTC()
	committed := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedTask(tx, lease, completedAt)
		if err != nil || !owned {
			return err
		}
		reportID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := insertExecutionSteps(tx, lease, result.Orchestration.Investigation, completedAt); err != nil {
			return err
		}
		if err := insertToolExecutions(tx, lease, result.Orchestration.ToolExecutions, completedAt); err != nil {
			return err
		}
		evidenceIDs, err := insertEvidenceItems(tx, lease.TaskID, result.Orchestration.EvidenceItems, completedAt)
		if err != nil {
			return err
		}
		businessSummary, technicalSummary, err := marshalReportSummaries(result.Orchestration)
		if err != nil {
			return err
		}
		report := result.Orchestration.Report
		if err := tx.Exec(`
INSERT INTO diagnosis_reports
    (id, task_id, conclusion_status, business_summary, technical_summary,
     report_schema_version, risk_level, model_provider, model_id,
     prompt_version, generated_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
			reportID, lease.TaskID, report.ConclusionStatus, businessSummary, technicalSummary,
			report.RiskLevel, result.ModelProvider, result.ModelID, result.PromptVersion,
			completedAt, completedAt, completedAt,
		).Error; err != nil {
			return TranslateError(err)
		}
		for index, reference := range report.Evidence {
			evidenceID, ok := evidenceIDs[strings.TrimSpace(reference.SourceRef)]
			if !ok {
				return fmt.Errorf("report evidence %q was not persisted", reference.SourceRef)
			}
			if err := tx.Exec(`
INSERT INTO report_evidence
    (report_id, evidence_id, claim_key, claim_text, support_type, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, reportID, evidenceID, fmt.Sprintf("claim-%03d", index+1),
				reference.Claim, reference.SupportType, completedAt,
			).Error; err != nil {
				return TranslateError(err)
			}
		}
		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = 'succeeded', lease_until = NULL, completed_at = ?,
    last_error_code = NULL, last_error_message = NULL, updated_at = ?
WHERE id = ? AND status = 'running' AND claim_owner = ? AND attempt_count = ?
  AND lease_until > ?`, completedAt, completedAt, lease.TaskID,
			lease.ClaimOwner, lease.AttemptCount, completedAt)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosisworker.ErrLeaseLost
		}
		if err := appendTaskEvent(tx, lease.TaskID, diagnosis.TaskEventSucceeded, map[string]any{
			"taskId": lease.TaskID.String(), "status": string(diagnosis.TaskSucceeded),
			"reportId": reportID.String(), "partial": result.Orchestration.Partial,
			"attemptCount": lease.AttemptCount,
		}, completedAt); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		if errors.Is(err, diagnosisworker.ErrLeaseLost) {
			return false, nil
		}
		return false, TranslateError(err)
	}
	return committed, nil
}

func (r *DiagnosisWorkerRepository) ReleaseForRetry(
	ctx context.Context,
	lease diagnosis.TaskLease,
	code, message string,
	releasedAt time.Time,
) (bool, error) {
	return r.finishAttempt(ctx, lease, diagnosis.TaskPending, code, message, releasedAt)
}

func (r *DiagnosisWorkerRepository) Fail(
	ctx context.Context,
	lease diagnosis.TaskLease,
	code, message string,
	failedAt time.Time,
) (bool, error) {
	return r.finishAttempt(ctx, lease, diagnosis.TaskFailed, code, message, failedAt)
}

func (r *DiagnosisWorkerRepository) finishAttempt(
	ctx context.Context,
	lease diagnosis.TaskLease,
	status diagnosis.TaskStatus,
	code, message string,
	finishedAt time.Time,
) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("diagnosis worker repository is unavailable")
	}
	if status != diagnosis.TaskPending && status != diagnosis.TaskFailed {
		return false, diagnosis.ErrTaskStateConflict
	}
	code = strings.TrimSpace(code)
	message = truncateWorkerValue(message, 1000)
	if code == "" || len(code) > 64 || message == "" {
		return false, diagnosis.ErrInvalidTask
	}
	finishedAt = finishedAt.UTC()
	changed := false
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		owned, err := lockOwnedTask(tx, lease, finishedAt)
		if err != nil || !owned {
			return err
		}
		completedAt := any(nil)
		eventType := diagnosis.TaskEventRetryScheduled
		if status == diagnosis.TaskFailed {
			completedAt = finishedAt
			eventType = diagnosis.TaskEventFailed
		}
		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = ?, claim_owner = NULL, claimed_at = NULL, lease_until = NULL,
    completed_at = ?, last_error_code = ?, last_error_message = ?, updated_at = ?
WHERE id = ? AND status = 'running' AND claim_owner = ? AND attempt_count = ?
  AND lease_until > ?`, status, completedAt, code, message, finishedAt,
			lease.TaskID, lease.ClaimOwner, lease.AttemptCount, finishedAt)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosisworker.ErrLeaseLost
		}
		if err := appendTaskEvent(tx, lease.TaskID, eventType, map[string]any{
			"taskId": lease.TaskID.String(), "status": string(status),
			"attemptCount": lease.AttemptCount, "errorCode": code,
		}, finishedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		if errors.Is(err, diagnosisworker.ErrLeaseLost) {
			return false, nil
		}
		return false, TranslateError(err)
	}
	return changed, nil
}

func (r *DiagnosisWorkerRepository) FinalizeCancellation(
	ctx context.Context,
	taskID uuid.UUID,
	completedAt time.Time,
) (diagnosis.TaskStatus, error) {
	if r == nil || r.db == nil {
		return "", errors.New("diagnosis worker repository is unavailable")
	}
	completedAt = completedAt.UTC()
	status := diagnosis.TaskStatus("")
	err := ResolveDB(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		state, err := selectTaskClaimStateForUpdate(tx, taskID)
		if err != nil {
			return err
		}
		status = state.Status
		if status != diagnosis.TaskCancelRequested {
			return nil
		}
		updated := tx.Exec(`
UPDATE diagnosis_tasks
SET status = 'cancelled', claim_owner = NULL, claimed_at = NULL,
    lease_until = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = 'cancel_requested'`, completedAt, completedAt, taskID)
		if updated.Error != nil {
			return TranslateError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return diagnosis.ErrTaskStateConflict
		}
		if err := appendTaskEvent(tx, taskID, diagnosis.TaskEventCancelled, map[string]any{
			"taskId": taskID.String(), "status": string(diagnosis.TaskCancelled),
		}, completedAt); err != nil {
			return err
		}
		status = diagnosis.TaskCancelled
		return nil
	})
	if err != nil {
		return "", TranslateError(err)
	}
	return status, nil
}

func lockOwnedTask(db *gorm.DB, lease diagnosis.TaskLease, now time.Time) (bool, error) {
	var record struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	result := db.Raw(`
SELECT id
FROM diagnosis_tasks
WHERE id = ? AND status = 'running' AND claim_owner = ? AND attempt_count = ?
  AND lease_until > ?
FOR UPDATE`, lease.TaskID, lease.ClaimOwner, lease.AttemptCount, now.UTC()).Scan(&record)
	if result.Error != nil {
		return false, TranslateError(result.Error)
	}
	return result.RowsAffected == 1 && record.ID == lease.TaskID, nil
}

func validateExecutionResult(result diagnosisworker.ExecutionResult) error {
	report := result.Orchestration.Report
	if !report.ConclusionStatus.Valid() || !report.RiskLevel.Valid() || !report.Confidence.Valid() ||
		strings.TrimSpace(report.Conclusion) == "" || strings.TrimSpace(report.BusinessSummary) == "" ||
		strings.TrimSpace(report.TechnicalSummary) == "" || result.Orchestration.Investigation == nil ||
		strings.TrimSpace(result.ModelProvider) == "" || len(result.ModelProvider) > 64 ||
		strings.TrimSpace(result.ModelID) == "" || len(result.ModelID) > 128 ||
		strings.TrimSpace(result.PromptVersion) == "" || len(result.PromptVersion) > 128 {
		return errors.New("diagnosis execution result is invalid")
	}
	seen := make(map[string]struct{}, len(result.Orchestration.EvidenceItems))
	for _, item := range result.Orchestration.EvidenceItems {
		if strings.TrimSpace(item.SourceRef) == "" || strings.TrimSpace(item.Snapshot) == "" ||
			strings.TrimSpace(item.ContentHash) == "" || item.CollectedAt.IsZero() {
			return errors.New("diagnosis evidence item is invalid")
		}
		if _, exists := seen[item.SourceRef]; exists {
			return errors.New("diagnosis evidence reference is duplicated")
		}
		seen[item.SourceRef] = struct{}{}
	}
	return nil
}

func insertExecutionSteps(
	db *gorm.DB,
	lease diagnosis.TaskLease,
	steps []agent.InvestigationStep,
	createdAt time.Time,
) error {
	for _, step := range steps {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		output, err := json.Marshal(map[string]any{"summary": step.Summary, "toolName": step.ToolName})
		if err != nil {
			return err
		}
		var duration any
		if step.DurationMS > 0 {
			duration = step.DurationMS
		}
		if err := db.Exec(`
INSERT INTO diagnosis_steps
    (id, task_id, attempt_count, step_no, step_type, display_name,
     status, output_summary, duration_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, lease.TaskID, lease.AttemptCount,
			step.Sequence, step.Kind, step.Title, step.Status, output, duration, createdAt,
		).Error; err != nil {
			return TranslateError(err)
		}
	}
	return nil
}

func insertToolExecutions(
	db *gorm.DB,
	lease diagnosis.TaskLease,
	executions []agent.ToolExecution,
	createdAt time.Time,
) error {
	for index, execution := range executions {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		status := "failed"
		if execution.Succeeded {
			status = "succeeded"
		}
		var errorKind any
		var errorMessage any
		if execution.Error != "" {
			errorKind = "tool_execution_error"
			errorMessage = truncateWorkerValue(execution.Error, 1000)
		}
		if err := db.Exec(`
INSERT INTO tool_executions
    (id, task_id, attempt_count, execution_no, tool_name, status,
     degraded, error_kind, error_message, evidence_ref, duration_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, id, lease.TaskID,
			lease.AttemptCount, index+1, execution.Name, status, execution.Degraded,
			errorKind, errorMessage, execution.EvidenceID, max(execution.DurationMS, 0), createdAt,
		).Error; err != nil {
			return TranslateError(err)
		}
	}
	return nil
}

func insertEvidenceItems(
	db *gorm.DB,
	taskID uuid.UUID,
	items []agent.EvidenceItem,
	createdAt time.Time,
) (map[string]uuid.UUID, error) {
	ids := make(map[string]uuid.UUID, len(items))
	for _, item := range items {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		locator, err := json.Marshal(map[string]any{
			"sourceRef": item.SourceRef, "sourceTool": item.SourceTool, "location": item.Location,
		})
		if err != nil {
			return nil, err
		}
		redactionStatus := "not_required"
		if item.Redacted {
			redactionStatus = "redacted"
		}
		if err := db.Exec(`
INSERT INTO evidence_items
    (id, task_id, source_type, source_locator, source_locator_schema_version,
     content_text, content_hash, collected_at, redaction_status, truncated,
     validity_status, created_at)
VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, 'valid', ?)`, id, taskID, item.SourceType,
			locator, item.Snapshot, item.ContentHash, item.CollectedAt.UTC(), redactionStatus,
			item.Truncated, createdAt,
		).Error; err != nil {
			return nil, TranslateError(err)
		}
		ids[item.SourceRef] = id
	}
	return ids, nil
}

func marshalReportSummaries(result agent.OrchestrationResult) ([]byte, []byte, error) {
	business, err := json.Marshal(map[string]any{
		"conclusion": result.Report.Conclusion,
		"summary":    result.Report.BusinessSummary,
		"confidence": result.Report.Confidence,
	})
	if err != nil {
		return nil, nil, err
	}
	technical, err := json.Marshal(map[string]any{
		"summary":                       result.Report.TechnicalSummary,
		"limitations":                   result.Report.Limitations,
		"partial":                       result.Partial,
		"missingEvidence":               result.MissingEvidence,
		"usage":                         result.Usage,
		"agentRuns":                     result.AgentRuns,
		"selectedSkill":                 result.SelectedSkill,
		"executedSkills":                result.ExecutedSkills,
		"stopReason":                    result.StopReason,
		"agenticRetrievalAttempted":     result.AgenticRetrievalAttempted,
		"agenticRetrievalAddedEvidence": result.AgenticRetrievalAddedEvidence,
		"agenticRetrievalStopReason":    result.AgenticRetrievalStopReason,
	})
	if err != nil {
		return nil, nil, err
	}
	return business, technical, nil
}

func truncateWorkerValue(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLength {
		return value
	}
	return strings.ToValidUTF8(value[:maxLength], "?")
}

type workerTaskRecord struct {
	ID             uuid.UUID       `gorm:"column:id"`
	CreatedBy      uuid.UUID       `gorm:"column:created_by"`
	RequestText    string          `gorm:"column:request_text"`
	RequestScope   []byte          `gorm:"column:request_scope"`
	ExternalCaseID uuid.UUID       `gorm:"column:external_case_id"`
	CaseSnapshot   json.RawMessage `gorm:"column:case_snapshot"`
	Role           auth.Role       `gorm:"column:role"`
	UserStatus     string          `gorm:"column:user_status"`
}

type workerDataSourceRecord struct {
	ID         uuid.UUID `gorm:"column:id"`
	Role       string    `gorm:"column:source_role"`
	SafetyMode string    `gorm:"column:safety_mode"`
}

type workerTaskAttachmentRecord struct {
	AttachmentID  uuid.UUID `gorm:"column:attachment_id"`
	Purpose       string    `gorm:"column:purpose"`
	OriginalName  string    `gorm:"column:original_filename"`
	MediaType     string    `gorm:"column:content_type"`
	SizeBytes     int64     `gorm:"column:size_bytes"`
	ContentSHA256 string    `gorm:"column:content_sha256"`
}
