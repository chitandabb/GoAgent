//go:build integration

package postgres

import (
	"context"
	"os"
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

func TestDiagnosisWorkerRepositoryCommitsFencedResultAgainstPostgres(t *testing.T) {
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

	ownerID := uuid.New()
	dataSourceID := uuid.New()
	externalCaseID := uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Worker Owner', 'integration-hash', 'analyst', 'active', false)`,
		ownerID, "worker_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Worker Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		dataSourceID, "worker-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', now())`, externalCaseID, dataSourceID, "WORKER-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	caseItem := &externalcase.ExternalCase{
		ID: externalCaseID, DataSourceID: dataSourceID,
		ExternalCaseKey: "WORKER-1001", CaseType: "incident", Title: "Worker fixture",
		Description: "verify formal result persistence", Status: externalcase.StatusOpen,
		Priority: externalcase.PriorityHigh, ReportedAt: now, SourceUpdatedAt: now,
		SourceFingerprint: "sha256:worker-source", Attributes: map[string]any{"module": "MES"},
	}
	taskRepository := NewDiagnosisTaskRepository(tx)
	taskService, err := diagnosis.NewDiagnosisTaskService(taskRepository, integrationCaseReader{item: caseItem})
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	created, err := taskService.Create(ctx, diagnosis.TaskActor{UserID: ownerID}, diagnosis.CreateTaskInput{
		ExternalCaseID: externalCaseID, ExpectedSourceFingerprint: caseItem.SourceFingerprint,
		RequestText: "检查工单状态", RequestScope: map[string]any{"requestedSkill": "ticket-diagnosis"},
		IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	leaseService, err := diagnosis.NewTaskExecutionService(taskRepository, time.Minute)
	if err != nil {
		t.Fatalf("NewTaskExecutionService(): %v", err)
	}
	claim, err := leaseService.Claim(ctx, created.Task.ID, "worker-integration")
	if err != nil || claim.Lease == nil {
		t.Fatalf("Claim(): result=%+v err=%v", claim, err)
	}

	workerRepository := NewDiagnosisWorkerRepository(tx)
	task, err := workerRepository.LoadTask(ctx, *claim.Lease, time.Now().UTC())
	if err != nil {
		t.Fatalf("LoadTask(): %v", err)
	}
	if task.CaseSnapshot.ID != externalCaseID || len(task.DataSources) != 1 || task.Role != "analyst" {
		t.Fatalf("task = %+v", task)
	}

	evidenceRef := "evidence:" + uuid.NewString()
	executionResult := diagnosisworker.ExecutionResult{
		Orchestration: agent.OrchestrationResult{
			Report: agent.StructuredReport{
				ConclusionStatus: agent.ConclusionProbable, RiskLevel: agent.RiskMedium,
				Conclusion: "状态更新存在延迟", BusinessSummary: "工单仍在处理中",
				TechnicalSummary: "快照显示状态尚未闭环", Confidence: agent.ConfidenceMedium,
				Evidence: []agent.ReportEvidence{{
					Claim: "工单状态为处理中", SourceTool: agent.ToolReadExternalCase,
					SourceRef: evidenceRef, SupportType: agent.EvidenceSupports,
				}}, Limitations: []string{},
			},
			AgentRuns: 1,
			ToolExecutions: []agent.ToolExecution{{
				Name: agent.ToolReadExternalCase, Succeeded: true, EvidenceID: evidenceRef, DurationMS: 5,
			}},
			EvidenceItems: []agent.EvidenceItem{{
				ID: evidenceRef, SourceType: agent.EvidenceSourceCaseSnapshot,
				SourceTool: agent.ToolReadExternalCase, SourceRef: evidenceRef,
				CollectedAt: now, Summary: "工单快照", Snapshot: `{"status":"processing"}`,
				ContentHash: "sha256:worker-evidence", Redacted: true,
			}},
			SelectedSkill:  agent.SkillTicketDiagnosis,
			ExecutedSkills: []agent.SkillID{agent.SkillTicketDiagnosis},
			Investigation: []agent.InvestigationStep{{
				Sequence: 1, Kind: agent.InvestigationAgentRun, Title: "Agent 调查",
				Summary: "调查完成", Status: "completed", DurationMS: 5,
			}},
		},
		ModelProvider: "stepfun", ModelID: "integration-model",
		PromptVersion: "evidence-gate-v1",
	}
	completed, err := workerRepository.Complete(ctx, *claim.Lease, executionResult, time.Now().UTC())
	if err != nil || !completed {
		t.Fatalf("Complete(): completed=%v err=%v", completed, err)
	}

	var state string
	var reports, evidence, links, steps, tools, successEvents int64
	if err := tx.Raw("SELECT status FROM diagnosis_tasks WHERE id = ?", created.Task.ID).Scan(&state).Error; err != nil {
		t.Fatalf("read task state: %v", err)
	}
	for query, target := range map[string]*int64{
		"SELECT COUNT(*) FROM diagnosis_reports WHERE task_id = ?":                                                                       &reports,
		"SELECT COUNT(*) FROM evidence_items WHERE task_id = ?":                                                                          &evidence,
		"SELECT COUNT(*) FROM report_evidence link JOIN diagnosis_reports report ON report.id = link.report_id WHERE report.task_id = ?": &links,
		"SELECT COUNT(*) FROM diagnosis_steps WHERE task_id = ?":                                                                         &steps,
		"SELECT COUNT(*) FROM tool_executions WHERE task_id = ?":                                                                         &tools,
		"SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_type = 'task_succeeded'":                                           &successEvents,
	} {
		if err := tx.Raw(query, created.Task.ID).Scan(target).Error; err != nil {
			t.Fatalf("count persisted result: %v", err)
		}
	}
	if state != "succeeded" || reports != 1 || evidence != 1 || links != 1 || steps != 1 || tools != 1 || successEvents != 1 {
		t.Fatalf("state/counts = %s %d/%d/%d/%d/%d/%d", state, reports, evidence, links, steps, tools, successEvents)
	}
	reportLookup, err := NewDiagnosisReportRepository(tx).FindTaskReport(ctx, created.Task.ID)
	if err != nil {
		t.Fatalf("FindTaskReport(): %v", err)
	}
	if reportLookup.TaskCreator != ownerID || reportLookup.TaskStatus != diagnosis.TaskSucceeded || reportLookup.Report == nil {
		t.Fatalf("report lookup = %+v", reportLookup)
	}
	formalReport := reportLookup.Report
	if formalReport.ModelProvider != "stepfun" || formalReport.ModelID != "integration-model" ||
		formalReport.Conclusion != "状态更新存在延迟" || len(formalReport.Evidence) != 1 ||
		formalReport.Evidence[0].SourceRef != evidenceRef || formalReport.Evidence[0].ClaimKey != "claim-001" {
		t.Fatalf("formal report = %+v", formalReport)
	}

	completed, err = workerRepository.Complete(ctx, *claim.Lease, executionResult, time.Now().UTC())
	if err != nil || completed {
		t.Fatalf("stale Complete() = %v, %v; want false, nil", completed, err)
	}
}
