//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	domainrepository "github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// mustIntegrationPolicyBuilder 是集成测试共用的最小 Policy Builder。
func mustIntegrationPolicyBuilder(t *testing.T, allowedSourceIDs ...uuid.UUID) diagnosis.InvestigationPolicyBuilder {
	t.Helper()
	builder, err := diagnosis.NewInvestigationPolicyBuilder(diagnosis.InvestigationPolicyConfig{
		BasePermissions: []agentruntime.Permission{
			agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		},
		AllowedDataSourceIDs: allowedSourceIDs,
	})
	if err != nil {
		t.Fatalf("NewInvestigationPolicyBuilder: %v", err)
	}
	return builder
}

func TestDiagnosisTaskRepositoryAgainstPostgres(t *testing.T) {
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
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	ownerID := uuid.New()
	dataSourceID := uuid.New()
	externalCaseID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Task Owner', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "task_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Task Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		dataSourceID, "task-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', now())`,
		externalCaseID, dataSourceID, "TASK-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}
	conversationRepository := NewConversationRepository(tx)
	conversationItem, err := conversationRepository.Create(ctx, ownerID, conversation.CreateInput{Title: "诊断附件"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	attachmentRepository := NewAttachmentRepository(tx)
	attachmentItem := integrationAttachment(ownerID, conversationItem.ID, uuid.New())
	if err := attachmentRepository.Create(ctx, attachmentItem); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	message, err := conversationRepository.AppendMessage(ctx, ownerID, conversation.AppendMessageInput{
		ConversationID: conversationItem.ID, Role: conversation.MessageRoleUser, Content: "请诊断并检查附件",
		Attachments: []conversation.MessageAttachmentInput{{AttachmentID: attachmentItem.ID, Purpose: "log_file"}},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("append attachment message: %v", err)
	}

	item := &externalcase.ExternalCase{
		ID: externalCaseID, DataSourceID: dataSourceID, ExternalCaseKey: "TASK-1001", CaseType: "incident",
		Title: "Task fixture", Description: "task creation fixture", SourceFingerprint: "sha256:task-source",
		ReportedAt: time.Now().UTC(), SourceUpdatedAt: time.Now().UTC(),
		Attachments: []externalcase.ExternalAttachment{{ObjectKey: "private/object-key", FileName: "fixture.txt"}},
	}
	repository := NewDiagnosisTaskRepository(tx)
	service, err := diagnosis.NewDiagnosisTaskService(repository, integrationCaseReader{item: item}, mustIntegrationPolicyBuilder(t))
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	input := diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: "检查任务快照", RequestScope: map[string]any{"source": "integration"},
		Attachments: []diagnosis.TaskAttachment{{AttachmentID: attachmentItem.ID, Purpose: "log_file"}},
		AttachmentSource: &diagnosis.TaskAttachmentSource{
			ConversationID: conversationItem.ID, MessageID: message.ID,
		},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	}
	created, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, input)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if created.Replayed || created.Task.ID == uuid.Nil || created.Task.Status != diagnosis.TaskPending {
		t.Fatalf("created task = %+v", created)
	}

	var snapshotCount, taskCount, attachmentCount, eventCount, outboxCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM case_snapshots WHERE id = ?", created.Task.CaseSnapshotID).Scan(&snapshotCount).Error; err != nil {
		t.Fatalf("count snapshot: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_tasks WHERE id = ?", created.Task.ID).Scan(&taskCount).Error; err != nil {
		t.Fatalf("count task: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_task_attachments WHERE task_id = ?", created.Task.ID).Scan(&attachmentCount).Error; err != nil {
		t.Fatalf("count task attachment: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM task_events WHERE task_id = ?", created.Task.ID).Scan(&eventCount).Error; err != nil {
		t.Fatalf("count task event: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ?", created.Task.ID).Scan(&outboxCount).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if snapshotCount != 1 || taskCount != 1 || attachmentCount != 1 || eventCount != 1 || outboxCount != 1 {
		t.Fatalf("durable counts snapshot/task/attachment/event/outbox = %d/%d/%d/%d/%d, want 1/1/1/1/1", snapshotCount, taskCount, attachmentCount, eventCount, outboxCount)
	}
	var snapshotPayload string
	if err := tx.Raw("SELECT payload::text FROM case_snapshots WHERE id = ?", created.Task.CaseSnapshotID).Scan(&snapshotPayload).Error; err != nil {
		t.Fatalf("read snapshot payload: %v", err)
	}
	if strings.Contains(snapshotPayload, "private/object-key") || strings.Contains(snapshotPayload, "objectKey") {
		t.Fatalf("snapshot leaked object key: %s", snapshotPayload)
	}

	replayed, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, input)
	if err != nil {
		t.Fatalf("replayed Create(): %v", err)
	}
	if !replayed.Replayed || replayed.Task.ID != created.Task.ID {
		t.Fatalf("replayed result = %+v", replayed)
	}
	if len(replayed.Task.Attachments) != 1 || replayed.Task.Attachments[0].AttachmentID != attachmentItem.ID ||
		replayed.Task.Attachments[0].SourceMessageID != message.ID {
		t.Fatalf("replayed task attachments=%+v", replayed.Task.Attachments)
	}
	if _, err := attachmentRepository.GetTaskReadable(ctx, ownerID, created.Task.ID, attachmentItem.ID); err != nil {
		t.Fatalf("GetTaskReadable(): %v", err)
	}
	if _, err := attachmentRepository.GetTaskReadable(ctx, uuid.New(), created.Task.ID, attachmentItem.ID); !errors.Is(err, domainrepository.ErrNotFound) {
		t.Fatalf("cross-user GetTaskReadable() error=%v", err)
	}
	unlinkedAttachment := integrationAttachment(ownerID, conversationItem.ID, uuid.New())
	if err := attachmentRepository.Create(ctx, unlinkedAttachment); err != nil {
		t.Fatalf("create unlinked attachment: %v", err)
	}
	unauthorizedInput := input
	unauthorizedInput.IdempotencyKey = uuid.NewString()
	unauthorizedInput.Attachments = []diagnosis.TaskAttachment{{AttachmentID: unlinkedAttachment.ID, Purpose: "context"}}
	if _, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, unauthorizedInput); !errors.Is(err, diagnosis.ErrTaskAttachmentForbidden) {
		t.Fatalf("unauthorized attachment Create() error=%v", err)
	}
	var unauthorizedTaskCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_tasks WHERE created_by = ? AND idempotency_key = ?", ownerID, unauthorizedInput.IdempotencyKey).Scan(&unauthorizedTaskCount).Error; err != nil {
		t.Fatalf("count unauthorized tasks: %v", err)
	}
	if unauthorizedTaskCount != 0 {
		t.Fatalf("unauthorized task count=%d, want 0", unauthorizedTaskCount)
	}

	input.RequestText = "不同请求内容"
	if _, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, input); !errors.Is(err, diagnosis.ErrIdempotencyConflict) {
		t.Fatalf("conflicting Create() error = %v, want ErrIdempotencyConflict", err)
	}

	loaded, err := service.Get(ctx, diagnosis.TaskActor{UserID: ownerID}, created.Task.ID)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if loaded.ID != created.Task.ID || loaded.CaseSnapshotID != created.Task.CaseSnapshotID || loaded.Status != diagnosis.TaskPending {
		t.Fatalf("loaded task = %+v", loaded)
	}

	firstPage, err := service.ListEvents(ctx, diagnosis.TaskActor{UserID: ownerID}, created.Task.ID, 0, 1)
	if err != nil {
		t.Fatalf("ListEvents(): %v", err)
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].Seq != 1 || firstPage.Items[0].EventType != "task_created" || firstPage.HasMore {
		t.Fatalf("initial event page = %+v", firstPage)
	}
	cancelled, err := service.Cancel(ctx, diagnosis.TaskActor{UserID: ownerID}, created.Task.ID)
	if err != nil {
		t.Fatalf("Cancel(): %v", err)
	}
	if !cancelled.Changed || cancelled.Task.Status != diagnosis.TaskCancelRequested {
		t.Fatalf("cancel result = %+v", cancelled)
	}
	repeatedCancel, err := service.Cancel(ctx, diagnosis.TaskActor{UserID: ownerID}, created.Task.ID)
	if err != nil {
		t.Fatalf("repeated Cancel(): %v", err)
	}
	if repeatedCancel.Changed || repeatedCancel.Task.Status != diagnosis.TaskCancelRequested {
		t.Fatalf("repeated cancel result = %+v", repeatedCancel)
	}
	cancelEvents, err := service.ListEvents(ctx, diagnosis.TaskActor{UserID: ownerID}, created.Task.ID, 1, 10)
	if err != nil {
		t.Fatalf("ListEvents(after cancel): %v", err)
	}
	if len(cancelEvents.Items) != 1 || cancelEvents.Items[0].Seq != 2 || cancelEvents.Items[0].EventType != "task_cancel_requested" {
		t.Fatalf("cancel event page = %+v", cancelEvents)
	}

	claimInput := diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: "检查 Worker 领取", RequestScope: map[string]any{"source": "integration"},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	}
	claimable, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, claimInput)
	if err != nil {
		t.Fatalf("Create(claimable): %v", err)
	}
	claimedAt := time.Now().UTC()
	firstClaim, err := repository.ClaimTask(ctx, diagnosis.TaskClaimRecord{
		TaskID: claimable.Task.ID, ClaimOwner: "worker-1", ClaimedAt: claimedAt,
		LeaseUntil: claimedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("ClaimTask(first): %v", err)
	}
	if firstClaim.Disposition != diagnosis.TaskClaimAcquired || firstClaim.Lease == nil || firstClaim.Lease.AttemptCount != 1 {
		t.Fatalf("first claim = %+v", firstClaim)
	}
	contended, err := repository.ClaimTask(ctx, diagnosis.TaskClaimRecord{
		TaskID: claimable.Task.ID, ClaimOwner: "worker-2", ClaimedAt: claimedAt.Add(time.Second),
		LeaseUntil: claimedAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ClaimTask(contended): %v", err)
	}
	if contended.Disposition != diagnosis.TaskClaimLeaseHeld || contended.Lease != nil {
		t.Fatalf("contended claim = %+v", contended)
	}
	if err := tx.Exec(`UPDATE diagnosis_tasks SET lease_until = ? WHERE id = ?`, claimedAt, claimable.Task.ID).Error; err != nil {
		t.Fatalf("expire task lease: %v", err)
	}
	secondClaim, err := repository.ClaimTask(ctx, diagnosis.TaskClaimRecord{
		TaskID: claimable.Task.ID, ClaimOwner: "worker-2", ClaimedAt: claimedAt.Add(2 * time.Second),
		LeaseUntil: claimedAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ClaimTask(reclaim): %v", err)
	}
	if secondClaim.Disposition != diagnosis.TaskClaimAcquired || secondClaim.Lease == nil || secondClaim.Lease.AttemptCount != 2 {
		t.Fatalf("second claim = %+v", secondClaim)
	}
	oldRenewed, err := repository.RenewTaskLease(ctx, diagnosis.TaskLeaseRenewal{
		TaskLease: *firstClaim.Lease, RenewedAt: claimedAt.Add(3 * time.Second),
		NewLeaseUntil: claimedAt.Add(3 * time.Minute),
	})
	if err != nil || oldRenewed {
		t.Fatalf("old RenewTaskLease() renewed=%v err=%v", oldRenewed, err)
	}
	newRenewed, err := repository.RenewTaskLease(ctx, diagnosis.TaskLeaseRenewal{
		TaskLease: *secondClaim.Lease, RenewedAt: claimedAt.Add(3 * time.Second),
		NewLeaseUntil: claimedAt.Add(3 * time.Minute),
	})
	if err != nil || !newRenewed {
		t.Fatalf("new RenewTaskLease() renewed=%v err=%v", newRenewed, err)
	}
	claimEvents, err := repository.ListTaskEvents(ctx, claimable.Task.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListTaskEvents(claims): %v", err)
	}
	if len(claimEvents.Items) != 3 || claimEvents.Items[1].EventType != "task_started" || claimEvents.Items[2].EventType != "task_reclaimed" {
		t.Fatalf("claim event page = %+v", claimEvents)
	}
}

func TestDiagnosisTaskRepositoryConcurrentClaimAgainstPostgres(t *testing.T) {
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
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ownerID := uuid.New()
	dataSourceID := uuid.New()
	externalCaseID := uuid.New()
	taskID := uuid.Nil
	snapshotID := uuid.Nil
	t.Cleanup(func() {
		cleanup := db.WithContext(context.Background())
		if taskID != uuid.Nil {
			_ = cleanup.Exec(`DELETE FROM outbox_events WHERE aggregate_id = ?`, taskID).Error
			_ = cleanup.Exec(`DELETE FROM task_events WHERE task_id = ?`, taskID).Error
			_ = cleanup.Exec(`DELETE FROM diagnosis_task_data_sources WHERE task_id = ?`, taskID).Error
			_ = cleanup.Exec(`DELETE FROM diagnosis_tasks WHERE id = ?`, taskID).Error
		}
		if snapshotID != uuid.Nil {
			_ = cleanup.Exec(`DELETE FROM case_snapshots WHERE id = ?`, snapshotID).Error
		}
		_ = cleanup.Exec(`DELETE FROM external_cases WHERE id = ?`, externalCaseID).Error
		_ = cleanup.Exec(`DELETE FROM data_sources WHERE id = ?`, dataSourceID).Error
		_ = cleanup.Exec(`DELETE FROM users WHERE id = ?`, ownerID).Error
	})

	if err := db.WithContext(ctx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Concurrent Owner', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "claim_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Concurrent Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		dataSourceID, "claim-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', now())`,
		externalCaseID, dataSourceID, "CLAIM-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}

	item := &externalcase.ExternalCase{
		ID: externalCaseID, DataSourceID: dataSourceID, ExternalCaseKey: "CLAIM-1001", CaseType: "incident",
		Title: "Concurrent claim fixture", Description: "two workers", SourceFingerprint: "sha256:claim-source",
		ReportedAt: time.Now().UTC(), SourceUpdatedAt: time.Now().UTC(),
	}
	repository := NewDiagnosisTaskRepository(db)
	service, err := diagnosis.NewDiagnosisTaskService(repository, integrationCaseReader{item: item}, mustIntegrationPolicyBuilder(t))
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	created, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: "并发领取", IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	taskID, snapshotID = created.Task.ID, created.Task.CaseSnapshotID

	claimedAt := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan diagnosis.TaskClaimResult, 2)
	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for _, workerID := range []string{"worker-a", "worker-b"} {
		wait.Add(1)
		go func(worker string) {
			defer wait.Done()
			<-start
			result, claimErr := repository.ClaimTask(ctx, diagnosis.TaskClaimRecord{
				TaskID: taskID, ClaimOwner: worker, ClaimedAt: claimedAt,
				LeaseUntil: claimedAt.Add(time.Minute),
			})
			results <- result
			errorsCh <- claimErr
		}(workerID)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for claimErr := range errorsCh {
		if claimErr != nil {
			t.Fatalf("concurrent ClaimTask(): %v", claimErr)
		}
	}
	acquired, held := 0, 0
	for result := range results {
		switch result.Disposition {
		case diagnosis.TaskClaimAcquired:
			acquired++
		case diagnosis.TaskClaimLeaseHeld:
			held++
		default:
			t.Fatalf("unexpected claim result: %+v", result)
		}
	}
	if acquired != 1 || held != 1 {
		t.Fatalf("concurrent dispositions acquired=%d held=%d", acquired, held)
	}
	var attemptCount int
	var startedEvents int64
	if err := db.WithContext(ctx).Raw(`SELECT attempt_count FROM diagnosis_tasks WHERE id = ?`, taskID).Scan(&attemptCount).Error; err != nil {
		t.Fatalf("read attempt count: %v", err)
	}
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_started'`, taskID).Scan(&startedEvents).Error; err != nil {
		t.Fatalf("count task_started events: %v", err)
	}
	if attemptCount != 1 || startedEvents != 1 {
		t.Fatalf("attemptCount=%d task_started=%d, want 1/1", attemptCount, startedEvents)
	}
}

type integrationCaseReader struct {
	item *externalcase.ExternalCase
}

func (r integrationCaseReader) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return r.item, nil
}
