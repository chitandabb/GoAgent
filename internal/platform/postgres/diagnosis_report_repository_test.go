package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDiagnosisReportRecordDecodesVersionedSummaries(t *testing.T) {
	reportID := uuid.New()
	taskID := uuid.New()
	now := time.Now().UTC()
	record := diagnosisReportRecord{
		TaskID: taskID, ReportID: &reportID, ConclusionStatus: "probable",
		RiskLevel: "medium", ReportSchemaVersion: 1,
		BusinessSummary:  []byte(`{"conclusion":"状态同步延迟","summary":"业务状态尚未闭环","confidence":"medium"}`),
		TechnicalSummary: []byte(`{"summary":"快照显示处理延迟","limitations":[],"partial":false,"missingEvidence":[],"usage":{"modelCalls":2,"promptTokens":100,"completionTokens":20,"totalTokens":120,"cachedTokens":0,"reasoningTokens":0},"agentRuns":1,"selectedSkill":"ticket-diagnosis","executedSkills":["ticket-diagnosis"],"stopReason":""}`),
		ModelProvider:    "stepfun", ModelID: "step-3.7-flash", PromptVersion: "diagnosis-v1",
		GeneratedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	report, err := record.toDomain()
	if err != nil {
		t.Fatalf("toDomain(): %v", err)
	}
	if report.ID != reportID || report.TaskID != taskID || report.Usage.TotalTokens != 120 || report.AgentRuns != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnosisReportRecordRejectsUnknownStoredFields(t *testing.T) {
	reportID := uuid.New()
	record := diagnosisReportRecord{
		TaskID: uuid.New(), ReportID: &reportID, ReportSchemaVersion: 1,
		BusinessSummary:  []byte(`{"conclusion":"x","summary":"x","confidence":"low","unknown":true}`),
		TechnicalSummary: []byte(`{"summary":"x","limitations":[],"partial":false,"missingEvidence":[],"usage":{},"agentRuns":0,"selectedSkill":"","executedSkills":[],"stopReason":""}`),
		ModelProvider:    "stepfun", ModelID: "model", PromptVersion: "v1",
	}
	if _, err := record.toDomain(); err == nil {
		t.Fatal("toDomain() accepted an unknown stored field")
	}
}

func TestDiagnosisReportEvidenceRecordRejectsUnknownLocatorVersion(t *testing.T) {
	record := diagnosisReportEvidenceRecord{
		EvidenceID: uuid.New(), ClaimKey: "claim-001", ClaimText: "claim",
		SourceLocatorSchemaVersion: 2,
	}
	if _, err := record.toDomain(); err == nil {
		t.Fatal("toDomain() accepted an unknown locator schema version")
	}
}
