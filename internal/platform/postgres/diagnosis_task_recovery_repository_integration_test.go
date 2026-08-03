//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/diagnosisworker"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDiagnosisWorkerRecoversAfterRetryExhaustionAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	fixture := newTaskRecoveryIntegrationFixture(t, tx)
	taskID := fixture.createTask(t)
	incoming := taskRecoveryIncomingMessage(t, tx, taskID, fixture.now)
	taskRepository := NewDiagnosisTaskRepository(tx)
	leaseService, err := diagnosis.NewTaskExecutionService(taskRepository, time.Minute)
	if err != nil {
		t.Fatalf("NewTaskExecutionService(): %v", err)
	}
	executor := &recoveringAgentExecutor{failuresRemaining: 4, now: fixture.now}
	worker, err := diagnosisworker.New(
		leaseService,
		NewDiagnosisWorkerRepository(tx),
		executor,
		diagnosisworker.Config{
			WorkerID: "worker-recovery-drill", RenewInterval: time.Hour, MaxAttempts: 4,
		},
	)
	if err != nil {
		t.Fatalf("diagnosisworker.New(): %v", err)
	}

	wantDelays := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	for attempt, wantDelay := range wantDelays {
		outcome := worker.Process(ctx, incoming)
		if outcome.Action != diagnosisworker.ActionRetry || outcome.RetryDelay != wantDelay {
			t.Fatalf("attempt %d outcome = %+v, want retry %s", attempt+1, outcome, wantDelay)
		}
	}
	failedOutcome := worker.Process(ctx, incoming)
	if failedOutcome.Action != diagnosisworker.ActionDeadLetter {
		t.Fatalf("retry exhaustion outcome = %+v, want dead_letter", failedOutcome)
	}

	var failed taskRecoveryTaskSnapshot
	if err := tx.Raw(`
SELECT status, attempt_count, claim_owner, claimed_at, lease_until, completed_at,
       last_error_code, last_error_message
FROM diagnosis_tasks WHERE id = ?`, taskID).Scan(&failed).Error; err != nil {
		t.Fatalf("read exhausted task: %v", err)
	}
	if failed.Status != diagnosis.TaskFailed || failed.AttemptCount != 4 || failed.LastErrorCode == nil ||
		*failed.LastErrorCode != diagnosis.TaskFailureAgentExecution {
		t.Fatalf("exhausted task = %+v", failed)
	}

	recoveryService, err := diagnosis.NewTaskRecoveryService(NewDiagnosisTaskRecoveryRepository(tx))
	if err != nil {
		t.Fatalf("NewTaskRecoveryService(): %v", err)
	}
	recovered, err := recoveryService.Recover(
		ctx,
		diagnosis.TaskActor{UserID: fixture.adminID, IsAdmin: true},
		taskID,
		uuid.NewString(),
		"模型服务恢复，继续原诊断任务",
	)
	if err != nil || recovered.Replayed {
		t.Fatalf("Recover(): result=%+v err=%v", recovered, err)
	}

	successOutcome := worker.Process(ctx, incoming)
	if successOutcome.Action != diagnosisworker.ActionAck {
		t.Fatalf("recovered execution outcome = %+v, want ack", successOutcome)
	}
	duplicateOutcome := worker.Process(ctx, incoming)
	if duplicateOutcome.Action != diagnosisworker.ActionAck {
		t.Fatalf("duplicate delivery outcome = %+v, want ack", duplicateOutcome)
	}

	var status diagnosis.TaskStatus
	var attemptCount, reports, failedEvents, requeueEvents, succeededEvents int64
	if err := tx.Raw("SELECT status, attempt_count FROM diagnosis_tasks WHERE id = ?", taskID).
		Row().Scan(&status, &attemptCount); err != nil {
		t.Fatalf("read recovered terminal task: %v", err)
	}
	for query, target := range map[string]*int64{
		"SELECT COUNT(*) FROM diagnosis_reports WHERE task_id = ?":                             &reports,
		"SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_failed'":    &failedEvents,
		"SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_requeued'":  &requeueEvents,
		"SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_succeeded'": &succeededEvents,
	} {
		if err := tx.Raw(query, taskID).Scan(target).Error; err != nil {
			t.Fatalf("count recovery drill facts: %v", err)
		}
	}
	if status != diagnosis.TaskSucceeded || attemptCount != 5 || reports != 1 || failedEvents != 1 ||
		requeueEvents != 1 || succeededEvents != 1 || executor.callCount() != 5 {
		t.Fatalf(
			"recovery drill facts = status:%s attempts:%d reports:%d failed:%d requeued:%d succeeded:%d executor:%d",
			status, attemptCount, reports, failedEvents, requeueEvents, succeededEvents, executor.callCount(),
		)
	}
}

func TestDiagnosisTaskRecoveryRepositoryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	fixture := newTaskRecoveryIntegrationFixture(t, tx)
	taskID, oldLease := fixture.createFailedTask(t, diagnosis.TaskFailureAgentExecution)
	var outboxBefore taskRecoveryOutboxSnapshot
	if err := tx.Raw(`
SELECT id, attempt_count, requeue_count, published_at, locked_at, locked_by,
       locked_until, last_error
FROM outbox_events WHERE aggregate_id = ? AND event_type = 'diagnosis.execute'`, taskID).
		Scan(&outboxBefore).Error; err != nil {
		t.Fatalf("read original outbox: %v", err)
	}
	if outboxBefore.ID == uuid.Nil {
		t.Fatal("original execution outbox is missing")
	}
	if err := tx.Exec(`
UPDATE outbox_events
SET published_at = ?, attempt_count = 3, locked_at = ?, locked_by = 'relay-old',
    locked_until = ?, requeue_count = 2, last_error = 'publish failed'
WHERE id = ?`, fixture.now, fixture.now, fixture.now.Add(time.Minute), outboxBefore.ID).Error; err != nil {
		t.Fatalf("prepare outbox state: %v", err)
	}

	recoveryID := uuid.New()
	key := uuid.NewString()
	input := diagnosis.TaskRecoveryRecord{
		ID: recoveryID, TaskID: taskID, RecoveredBy: fixture.adminID,
		IdempotencyKey: key, Reason: "模型服务已经恢复", RecoveredAt: fixture.now.Add(time.Minute),
	}
	repository := NewDiagnosisTaskRecoveryRepository(tx)
	result, err := repository.RecoverFailedTask(ctx, input)
	if err != nil {
		t.Fatalf("RecoverFailedTask(): %v", err)
	}
	if result.Replayed || result.Recovery.ID != recoveryID || result.Recovery.TaskEventSeq != 4 ||
		result.Recovery.PreviousAttemptCount != 1 || result.Recovery.OutboxEventID != outboxBefore.ID {
		t.Fatalf("recovery result = %+v", result)
	}

	var task taskRecoveryTaskSnapshot
	if err := tx.Raw(`
SELECT status, attempt_count, claim_owner, claimed_at, lease_until, completed_at,
       last_error_code, last_error_message
FROM diagnosis_tasks WHERE id = ?`, taskID).Scan(&task).Error; err != nil {
		t.Fatalf("read recovered task: %v", err)
	}
	if task.Status != diagnosis.TaskPending || task.AttemptCount != 1 || task.ClaimOwner != nil ||
		task.ClaimedAt != nil || task.LeaseUntil != nil || task.CompletedAt != nil ||
		task.LastErrorCode != nil || task.LastErrorMessage != nil {
		t.Fatalf("recovered task = %+v", task)
	}

	var outboxAfter taskRecoveryOutboxSnapshot
	if err := tx.Raw(`
SELECT id, attempt_count, requeue_count, published_at, locked_at, locked_by,
       locked_until, last_error
FROM outbox_events WHERE id = ?`, outboxBefore.ID).Scan(&outboxAfter).Error; err != nil {
		t.Fatalf("read reopened outbox: %v", err)
	}
	if outboxAfter.ID != outboxBefore.ID || outboxAfter.AttemptCount != 0 || outboxAfter.RequeueCount != 3 ||
		outboxAfter.PublishedAt != nil || outboxAfter.LockedAt != nil || outboxAfter.LockedBy != nil ||
		outboxAfter.LockedUntil != nil || outboxAfter.LastError != nil {
		t.Fatalf("reopened outbox = %+v", outboxAfter)
	}

	var recoveryCount, requeueEventCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_task_recoveries WHERE task_id = ?", taskID).
		Scan(&recoveryCount).Error; err != nil {
		t.Fatalf("count recovery audit: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_requeued'", taskID).
		Scan(&requeueEventCount).Error; err != nil {
		t.Fatalf("count requeue events: %v", err)
	}
	if recoveryCount != 1 || requeueEventCount != 1 {
		t.Fatalf("recovery/event counts = %d/%d, want 1/1", recoveryCount, requeueEventCount)
	}

	replayed, err := repository.RecoverFailedTask(ctx, diagnosis.TaskRecoveryRecord{
		ID: uuid.New(), TaskID: taskID, RecoveredBy: fixture.adminID,
		IdempotencyKey: key, Reason: input.Reason, RecoveredAt: input.RecoveredAt.Add(time.Minute),
	})
	if err != nil || !replayed.Replayed || replayed.Recovery.ID != recoveryID {
		t.Fatalf("replayed recovery = %+v, %v", replayed, err)
	}
	if _, err := repository.RecoverFailedTask(ctx, diagnosis.TaskRecoveryRecord{
		ID: uuid.New(), TaskID: taskID, RecoveredBy: fixture.adminID,
		IdempotencyKey: key, Reason: "不同原因", RecoveredAt: input.RecoveredAt.Add(time.Minute),
	}); !errors.Is(err, diagnosis.ErrIdempotencyConflict) {
		t.Fatalf("conflicting recovery error = %v, want ErrIdempotencyConflict", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_task_recoveries WHERE task_id = ?", taskID).
		Scan(&recoveryCount).Error; err != nil {
		t.Fatalf("recount recovery audit: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_requeued'", taskID).
		Scan(&requeueEventCount).Error; err != nil {
		t.Fatalf("recount requeue events: %v", err)
	}
	if recoveryCount != 1 || requeueEventCount != 1 {
		t.Fatalf("replay duplicated recovery facts: %d/%d", recoveryCount, requeueEventCount)
	}

	claimedAt := input.RecoveredAt.Add(2 * time.Minute)
	claim, err := NewDiagnosisTaskRepository(tx).ClaimTask(ctx, diagnosis.TaskClaimRecord{
		TaskID: taskID, ClaimOwner: "worker-recovered", ClaimedAt: claimedAt,
		LeaseUntil: claimedAt.Add(time.Minute),
	})
	if err != nil || claim.Lease == nil || claim.Lease.AttemptCount != 2 {
		t.Fatalf("claim recovered task = %+v, %v", claim, err)
	}
	staleChanged, err := NewDiagnosisWorkerRepository(tx).Fail(
		ctx, oldLease, diagnosis.TaskFailureAgentExecution, "stale worker", claimedAt.Add(time.Second),
	)
	if err != nil || staleChanged {
		t.Fatalf("stale lease Fail() = %v, %v; want false, nil", staleChanged, err)
	}
}

func TestDiagnosisTaskRecoveryRepositoryRejectsUnsafeStates(t *testing.T) {
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	fixture := newTaskRecoveryIntegrationFixture(t, tx)
	repository := NewDiagnosisTaskRecoveryRepository(tx)
	assertRejected := func(name string, taskID uuid.UUID) {
		t.Helper()
		_, err := repository.RecoverFailedTask(ctx, diagnosis.TaskRecoveryRecord{
			ID: uuid.New(), TaskID: taskID, RecoveredBy: fixture.adminID,
			IdempotencyKey: uuid.NewString(), Reason: name, RecoveredAt: fixture.now.Add(time.Minute),
		})
		if !errors.Is(err, diagnosis.ErrTaskStateConflict) {
			t.Fatalf("%s recovery error = %v, want ErrTaskStateConflict", name, err)
		}
	}

	permanentTask, _ := fixture.createFailedTask(t, "invalid_task_execution_input")
	assertRejected("permanent failure", permanentTask)

	runningTask := fixture.createTask(t)
	if _, err := NewDiagnosisTaskRepository(tx).ClaimTask(ctx, diagnosis.TaskClaimRecord{
		TaskID: runningTask, ClaimOwner: "worker-running", ClaimedAt: fixture.now,
		LeaseUntil: fixture.now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("claim running task: %v", err)
	}
	assertRejected("running task", runningTask)

	reportedTask, _ := fixture.createFailedTask(t, diagnosis.TaskFailureAgentExecution)
	if err := tx.Exec(`
INSERT INTO diagnosis_reports
    (id, task_id, conclusion_status, business_summary, technical_summary,
     report_schema_version, risk_level, model_provider, model_id, prompt_version,
     generated_at, created_at, updated_at)
VALUES (?, ?, 'inconclusive', '{}'::jsonb, '{}'::jsonb, 1, 'medium',
        'integration-provider', 'integration-model', 'integration-prompt', ?, ?, ?)`,
		uuid.New(), reportedTask, fixture.now, fixture.now, fixture.now).Error; err != nil {
		t.Fatalf("insert report: %v", err)
	}
	assertRejected("task with report", reportedTask)

	inactiveSourceTask, _ := fixture.createFailedTask(t, diagnosis.TaskFailureAgentExecution)
	if err := tx.Exec("UPDATE data_sources SET status = 'disabled' WHERE id = ?", fixture.dataSourceID).Error; err != nil {
		t.Fatalf("deactivate source: %v", err)
	}
	assertRejected("inactive source", inactiveSourceTask)
	if err := tx.Exec("UPDATE data_sources SET status = 'active' WHERE id = ?", fixture.dataSourceID).Error; err != nil {
		t.Fatalf("reactivate source: %v", err)
	}

	inactiveCreatorTask, _ := fixture.createFailedTask(t, diagnosis.TaskFailureAgentExecution)
	if err := tx.Exec("UPDATE users SET status = 'disabled' WHERE id = ?", fixture.creatorID).Error; err != nil {
		t.Fatalf("deactivate creator: %v", err)
	}
	assertRejected("inactive creator", inactiveCreatorTask)
}

type taskRecoveryIntegrationFixture struct {
	t            *testing.T
	db           *gorm.DB
	now          time.Time
	creatorID    uuid.UUID
	adminID      uuid.UUID
	dataSourceID uuid.UUID
	externalCase externalcase.ExternalCase
}

func newTaskRecoveryIntegrationFixture(t *testing.T, db *gorm.DB) *taskRecoveryIntegrationFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := &taskRecoveryIntegrationFixture{
		t: t, db: db, now: now, creatorID: uuid.New(), adminID: uuid.New(), dataSourceID: uuid.New(),
		externalCase: externalcase.ExternalCase{
			ID: uuid.New(), ExternalCaseKey: "RECOVERY-" + uuid.NewString()[:8], CaseType: "incident",
			Title: "Recovery fixture", Description: "verify failed task recovery",
			Status: externalcase.StatusOpen, Priority: externalcase.PriorityHigh,
			ReportedAt: now, SourceUpdatedAt: now, SourceFingerprint: "sha256:recovery-source",
		},
	}
	fixture.externalCase.DataSourceID = fixture.dataSourceID
	for _, user := range []struct {
		id   uuid.UUID
		name string
		role string
	}{
		{id: fixture.creatorID, name: "recovery_owner_" + uuid.NewString()[:8], role: "analyst"},
		{id: fixture.adminID, name: "recovery_admin_" + uuid.NewString()[:8], role: "admin"},
	} {
		if err := db.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Recovery User', 'integration-hash', ?, 'active', false)`, user.id, user.name, user.role).Error; err != nil {
			t.Fatalf("insert %s user: %v", user.role, err)
		}
	}
	if err := db.Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Recovery Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		fixture.dataSourceID, "recovery-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := db.Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', ?)`, fixture.externalCase.ID, fixture.dataSourceID,
		fixture.externalCase.ExternalCaseKey, now).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}
	return fixture
}

func (f *taskRecoveryIntegrationFixture) createTask(t *testing.T) uuid.UUID {
	t.Helper()
	repository := NewDiagnosisTaskRepository(f.db)
	service, err := diagnosis.NewDiagnosisTaskService(repository, integrationCaseReader{item: &f.externalCase})
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	created, err := service.Create(context.Background(), diagnosis.TaskActor{UserID: f.creatorID}, diagnosis.CreateTaskInput{
		ExternalCaseID: f.externalCase.ID, ExpectedSourceFingerprint: f.externalCase.SourceFingerprint,
		RequestText: "验证任务恢复", RequestScope: map[string]any{"source": "integration"},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	return created.Task.ID
}

func (f *taskRecoveryIntegrationFixture) createFailedTask(t *testing.T, failureCode string) (uuid.UUID, diagnosis.TaskLease) {
	t.Helper()
	taskID := f.createTask(t)
	claim, err := NewDiagnosisTaskRepository(f.db).ClaimTask(context.Background(), diagnosis.TaskClaimRecord{
		TaskID: taskID, ClaimOwner: "worker-failed-" + uuid.NewString()[:8], ClaimedAt: f.now,
		LeaseUntil: f.now.Add(time.Minute),
	})
	if err != nil || claim.Lease == nil {
		t.Fatalf("ClaimTask(): %+v, %v", claim, err)
	}
	failed, err := NewDiagnosisWorkerRepository(f.db).Fail(
		context.Background(), *claim.Lease, failureCode, "integration failure", f.now.Add(time.Second),
	)
	if err != nil || !failed {
		t.Fatalf("Fail(): %v, %v", failed, err)
	}
	return taskID, *claim.Lease
}

type taskRecoveryTaskSnapshot struct {
	Status           diagnosis.TaskStatus `gorm:"column:status"`
	AttemptCount     int                  `gorm:"column:attempt_count"`
	ClaimOwner       *string              `gorm:"column:claim_owner"`
	ClaimedAt        *time.Time           `gorm:"column:claimed_at"`
	LeaseUntil       *time.Time           `gorm:"column:lease_until"`
	CompletedAt      *time.Time           `gorm:"column:completed_at"`
	LastErrorCode    *string              `gorm:"column:last_error_code"`
	LastErrorMessage *string              `gorm:"column:last_error_message"`
}

type taskRecoveryOutboxSnapshot struct {
	ID           uuid.UUID  `gorm:"column:id"`
	AttemptCount int        `gorm:"column:attempt_count"`
	RequeueCount int        `gorm:"column:requeue_count"`
	PublishedAt  *time.Time `gorm:"column:published_at"`
	LockedAt     *time.Time `gorm:"column:locked_at"`
	LockedBy     *string    `gorm:"column:locked_by"`
	LockedUntil  *time.Time `gorm:"column:locked_until"`
	LastError    *string    `gorm:"column:last_error"`
}

type recoveringAgentExecutor struct {
	mu                sync.Mutex
	failuresRemaining int
	calls             int
	now               time.Time
}

func (e *recoveringAgentExecutor) Execute(
	_ context.Context,
	_ diagnosisworker.Task,
) (diagnosisworker.ExecutionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.failuresRemaining > 0 {
		e.failuresRemaining--
		return diagnosisworker.ExecutionResult{}, errors.New("model temporarily unavailable")
	}
	evidenceRef := "evidence:" + uuid.NewString()
	return diagnosisworker.ExecutionResult{
		Orchestration: agent.OrchestrationResult{
			Report: agent.StructuredReport{
				ConclusionStatus: agent.ConclusionProbable,
				RiskLevel:        agent.RiskMedium,
				Conclusion:       "模型服务恢复后完成诊断",
				BusinessSummary:  "任务已经恢复并完成",
				TechnicalSummary: "原任务保留上下文并产生唯一报告",
				Confidence:       agent.ConfidenceMedium,
				Evidence: []agent.ReportEvidence{{
					Claim: "恢复后诊断成功", SourceTool: agent.ToolReadExternalCase,
					SourceRef: evidenceRef, SupportType: agent.EvidenceSupports,
				}},
				Limitations: []string{},
			},
			AgentRuns: 1,
			ToolExecutions: []agent.ToolExecution{{
				Name: agent.ToolReadExternalCase, Succeeded: true,
				EvidenceID: evidenceRef, DurationMS: 5,
			}},
			EvidenceItems: []agent.EvidenceItem{{
				ID: evidenceRef, SourceType: agent.EvidenceSourceCaseSnapshot,
				SourceTool: agent.ToolReadExternalCase, SourceRef: evidenceRef,
				CollectedAt: e.now, Summary: "恢复演练证据", Snapshot: `{"status":"recovered"}`,
				ContentHash: "sha256:recovery-drill-evidence", Redacted: true,
			}},
			SelectedSkill:  agent.SkillTicketDiagnosis,
			ExecutedSkills: []agent.SkillID{agent.SkillTicketDiagnosis},
			Investigation: []agent.InvestigationStep{{
				Sequence: 1, Kind: agent.InvestigationAgentRun,
				Title: "恢复后 Agent 调查", Summary: "调查完成", Status: "completed", DurationMS: 5,
			}},
		},
		ModelProvider: "integration-provider",
		ModelID:       "recovery-drill-model",
		PromptVersion: "recovery-drill-prompt-v1",
	}, nil
}

func (e *recoveringAgentExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func taskRecoveryIncomingMessage(
	t *testing.T,
	db *gorm.DB,
	taskID uuid.UUID,
	occurredAt time.Time,
) diagnosisworker.IncomingMessage {
	t.Helper()
	var outbox struct {
		ID            uuid.UUID `gorm:"column:id"`
		CorrelationID uuid.UUID `gorm:"column:correlation_id"`
	}
	if err := db.Raw(`
SELECT id, correlation_id
FROM outbox_events
WHERE aggregate_id = ? AND event_type = 'diagnosis.execute'`, taskID).Scan(&outbox).Error; err != nil {
		t.Fatalf("read diagnosis outbox: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"messageId": outbox.ID.String(), "messageType": diagnosisworker.DiagnosisMessageType,
		"schemaVersion": diagnosisworker.DiagnosisSchemaVersion,
		"occurredAt":    occurredAt.UTC().Format(time.RFC3339Nano),
		"correlationId": outbox.CorrelationID.String(), "causationId": nil,
		"payload": map[string]any{"taskId": taskID.String()},
	})
	if err != nil {
		t.Fatalf("marshal diagnosis message: %v", err)
	}
	return diagnosisworker.IncomingMessage{
		ContentType: "application/json", MessageID: outbox.ID.String(),
		CorrelationID: outbox.CorrelationID.String(), Type: diagnosisworker.DiagnosisMessageType,
		Body: body,
	}
}
