//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

	item := &externalcase.ExternalCase{
		ID: externalCaseID, DataSourceID: dataSourceID, ExternalCaseKey: "TASK-1001", CaseType: "incident",
		Title: "Task fixture", Description: "task creation fixture", SourceFingerprint: "sha256:task-source",
		ReportedAt: time.Now().UTC(), SourceUpdatedAt: time.Now().UTC(),
		Attachments: []externalcase.ExternalAttachment{{ObjectKey: "private/object-key", FileName: "fixture.txt"}},
	}
	repository := NewDiagnosisTaskRepository(tx)
	service, err := diagnosis.NewDiagnosisTaskService(repository, integrationCaseReader{item: item})
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	input := diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: item.SourceFingerprint,
		RequestText: "检查任务快照", RequestScope: map[string]any{"source": "integration"},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	}
	created, err := service.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, input)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if created.Replayed || created.Task.ID == uuid.Nil || created.Task.Status != diagnosis.TaskPending {
		t.Fatalf("created task = %+v", created)
	}

	var snapshotCount, taskCount, eventCount, outboxCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM case_snapshots WHERE id = ?", created.Task.CaseSnapshotID).Scan(&snapshotCount).Error; err != nil {
		t.Fatalf("count snapshot: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM diagnosis_tasks WHERE id = ?", created.Task.ID).Scan(&taskCount).Error; err != nil {
		t.Fatalf("count task: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM task_events WHERE task_id = ?", created.Task.ID).Scan(&eventCount).Error; err != nil {
		t.Fatalf("count task event: %v", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ?", created.Task.ID).Scan(&outboxCount).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if snapshotCount != 1 || taskCount != 1 || eventCount != 1 || outboxCount != 1 {
		t.Fatalf("durable counts snapshot/task/event/outbox = %d/%d/%d/%d, want 1/1/1/1", snapshotCount, taskCount, eventCount, outboxCount)
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
}

type integrationCaseReader struct {
	item *externalcase.ExternalCase
}

func (r integrationCaseReader) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return r.item, nil
}
