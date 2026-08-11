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
		TechnicalSummary: []byte(`{"summary":"快照显示处理延迟","limitations":[],"partial":false,"missingEvidence":[],"usage":{"modelCalls":2,"promptTokens":100,"completionTokens":20,"totalTokens":120,"cachedTokens":0,"reasoningTokens":0},"agentRuns":2,"selectedSkill":"ticket-diagnosis","executedSkills":["ticket-diagnosis"],"stopReason":"","agenticRetrievalAttempted":true,"agenticRetrievalAddedEvidence":true,"agenticRetrievalStopReason":"new_evidence_added","contextObservation":{"preflightCalls":2,"preflightFailureCount":0,"highWaterTokens":1200,"availableInputTokens":124928,"highWaterRatio":0.0096,"toolResultTruncatedCount":1,"hardWindowBlockedCount":0,"lastEstimatedUpperBoundTokens":1200,"reportOutputReserveTokens":4096,"toolGrowthReserveTokens":8192,"estimationMethod":"local_calibrated"}}`),
		ModelProvider:    "stepfun", ModelID: "step-3.7-flash", PromptVersion: "diagnosis-v1",
		GeneratedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	report, err := record.toDomain()
	if err != nil {
		t.Fatalf("toDomain(): %v", err)
	}
	if report.ID != reportID || report.TaskID != taskID || report.Usage.TotalTokens != 120 || report.AgentRuns != 2 ||
		!report.AgenticRetrievalAttempted || !report.AgenticRetrievalAddedEvidence ||
		report.AgenticRetrievalStopReason != "new_evidence_added" ||
		report.ContextObservation.HighWaterTokens != 1200 ||
		report.ContextObservation.ToolResultTruncatedCount != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnosisReportRecordRejectsInvalidContextObservation(t *testing.T) {
	reportID := uuid.New()
	record := diagnosisReportRecord{
		TaskID: uuid.New(), ReportID: &reportID, ReportSchemaVersion: 1,
		BusinessSummary:  []byte(`{"conclusion":"x","summary":"x","confidence":"low"}`),
		TechnicalSummary: []byte(`{"summary":"x","limitations":[],"partial":false,"missingEvidence":[],"usage":{},"agentRuns":0,"selectedSkill":"","executedSkills":[],"stopReason":"","contextObservation":{"preflightCalls":1,"preflightFailureCount":0,"highWaterTokens":10,"availableInputTokens":100,"highWaterRatio":0.1,"toolResultTruncatedCount":0,"hardWindowBlockedCount":2,"lastEstimatedUpperBoundTokens":10,"reportOutputReserveTokens":10,"toolGrowthReserveTokens":10,"estimationMethod":"local_calibrated"}}`),
		ModelProvider:    "stepfun", ModelID: "model", PromptVersion: "v1",
	}
	if _, err := record.toDomain(); err == nil {
		t.Fatal("toDomain() accepted an invalid context observation")
	}
}

func TestDiagnosisReportRecordAcceptsPreflightFailureObservation(t *testing.T) {
	reportID := uuid.New()
	record := diagnosisReportRecord{
		TaskID: uuid.New(), ReportID: &reportID, ReportSchemaVersion: 1,
		BusinessSummary: []byte(`{"conclusion":"x","summary":"x","confidence":"low"}`),
		TechnicalSummary: []byte(`{"summary":"x","limitations":[],"partial":true,"missingEvidence":["preflight failed"],"usage":{},"agentRuns":1,"selectedSkill":"ticket-diagnosis","executedSkills":["ticket-diagnosis"],"stopReason":"context_preflight_failed","contextObservation":{"preflightCalls":1,"preflightFailureCount":1,"highWaterTokens":0,"availableInputTokens":0,"highWaterRatio":0,"toolResultTruncatedCount":0,"hardWindowBlockedCount":0,"lastEstimatedUpperBoundTokens":0,"reportOutputReserveTokens":4096,"toolGrowthReserveTokens":8192}}`),
		ModelProvider: "stepfun", ModelID: "model", PromptVersion: "v1",
	}
	report, err := record.toDomain()
	if err != nil {
		t.Fatalf("toDomain(): %v", err)
	}
	if report.ContextObservation.PreflightFailureCount != 1 ||
		report.StopReason != "context_preflight_failed" {
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
