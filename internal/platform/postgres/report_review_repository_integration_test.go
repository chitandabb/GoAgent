//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDiagnosisReportReviewRepositoryAgainstPostgres(t *testing.T) {
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
	adminID := uuid.New()
	dataSourceID := uuid.New()
	externalCaseID := uuid.New()
	snapshotID := uuid.New()
	taskID := uuid.New()
	reportID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Review Owner', 'integration-hash', 'analyst', 'active', false),
       (?, ?, 'Review Admin', 'integration-hash', 'admin', 'active', false)`,
		ownerID, "review_owner_"+uuid.NewString()[:8], adminID, "review_admin_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Review Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		dataSourceID, "review-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', now())`, externalCaseID, dataSourceID, "REVIEW-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO case_snapshots
    (id, external_case_id, snapshot_no, payload, content_hash, source_read_at)
VALUES (?, ?, 1, '{"title":"review fixture"}', 'sha256:review', now())`,
		snapshotID, externalCaseID).Error; err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO diagnosis_tasks
    (id, created_by, external_case_id, case_snapshot_id, idempotency_key, request_fingerprint, request_text)
VALUES (?, ?, ?, ?, ?, ?, 'review fixture task')`,
		taskID, ownerID, externalCaseID, snapshotID, "review-key-"+uuid.NewString()[:8], "sha256:request").Error; err != nil {
		t.Fatalf("insert diagnosis task: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO diagnosis_reports
    (id, task_id, conclusion_status, risk_level, model_provider, model_id, prompt_version, generated_at)
VALUES (?, ?, 'probable', 'medium', 'stepfun', 'test-model', 'test-prompt', now())`,
		reportID, taskID).Error; err != nil {
		t.Fatalf("insert diagnosis report: %v", err)
	}

	repository := NewDiagnosisReportReviewRepository(tx)
	service, err := diagnosis.NewReportReviewService(repository)
	if err != nil {
		t.Fatalf("NewReportReviewService: %v", err)
	}
	created, err := service.Submit(ctx, diagnosis.ReviewActor{UserID: ownerID}, reportID, diagnosis.SubmitReviewInput{
		Verdict: diagnosis.ReviewPartiallyAdopted, Comment: "数据库方向正确，但需要继续验证代码逻辑",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if created.ReportID != reportID || created.ReviewedBy != ownerID {
		t.Fatalf("created review = %+v", created)
	}

	reviews, err := service.List(ctx, diagnosis.ReviewActor{UserID: adminID, IsAdmin: true}, reportID)
	if err != nil {
		t.Fatalf("admin List: %v", err)
	}
	if len(reviews) != 1 || reviews[0].Verdict != diagnosis.ReviewPartiallyAdopted {
		t.Fatalf("reviews = %+v", reviews)
	}
}
