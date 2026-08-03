package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestDiagnosisReportRoutesReturnsFormalReportWithoutRawEvidence(t *testing.T) {
	taskID := uuid.New()
	ownerID := uuid.New()
	reportID := uuid.New()
	evidenceID := uuid.New()
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	useCase := &diagnosisReportUseCaseStub{report: diagnosis.DiagnosisReport{
		ID: reportID, TaskID: taskID, ConclusionStatus: "probable", RiskLevel: "medium",
		Conclusion: "状态同步延迟", BusinessSummary: "业务状态尚未闭环",
		TechnicalSummary: "快照显示处理链路延迟", Confidence: "medium",
		Limitations: []string{"缺少服务日志"}, MissingEvidence: []string{},
		Usage: diagnosis.ReportModelUsage{ModelCalls: 2, TotalTokens: 1200}, AgentRuns: 1,
		SelectedSkill: "ticket-diagnosis", ExecutedSkills: []string{"ticket-diagnosis"},
		ReportSchemaVersion: 1, ModelProvider: "stepfun", ModelID: "step-3.7-flash",
		PromptVersion: "diagnosis-v1", GeneratedAt: now, CreatedAt: now, UpdatedAt: now,
		Evidence: []diagnosis.ReportEvidenceClaim{{
			EvidenceID: evidenceID, ClaimKey: "claim-001", Claim: "工单仍处于处理中",
			SupportType: "supports", SourceType: "case_snapshot",
			SourceRef: "evidence:case-1", SourceTool: "read_external_case",
			ContentHash: "sha256:evidence", CollectedAt: now,
			RedactionStatus: "redacted", ValidityStatus: "valid",
		}},
	}}
	routes, err := NewDiagnosisReportRoutes(useCase, identityMiddleware(ownerID, false))
	if err != nil {
		t.Fatalf("NewDiagnosisReportRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/"+taskID.String()+"/report", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	body := string(data)
	for _, expected := range []string{reportID.String(), evidenceID.String(), `"sourceRef":"evidence:case-1"`, `"modelProvider":"stepfun"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response missing %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{`"contentText":`, `"rawSnapshot":`, `"promptText":`, `"reasoningContent":`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}
	if useCase.actor.UserID != ownerID || useCase.taskID != taskID {
		t.Fatalf("use case arguments actor=%+v task=%s", useCase.actor, useCase.taskID)
	}
}

func TestDiagnosisReportRoutesMapsUnavailableAndForbidden(t *testing.T) {
	taskID := uuid.New()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "unavailable", err: diagnosis.ErrTaskReportUnavailable, status: http.StatusConflict, code: `"code":40921`},
		{name: "forbidden", err: diagnosis.ErrTaskForbidden, status: http.StatusForbidden, code: `"code":40301`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routes, err := NewDiagnosisReportRoutes(
				&diagnosisReportUseCaseStub{err: test.err}, identityMiddleware(uuid.New(), false),
			)
			if err != nil {
				t.Fatalf("NewDiagnosisReportRoutes(): %v", err)
			}
			router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
				"/api/v1/diagnosis-tasks/"+taskID.String()+"/report", nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDiagnosisReportRoutesRejectsInvalidTaskID(t *testing.T) {
	routes, err := NewDiagnosisReportRoutes(&diagnosisReportUseCaseStub{}, identityMiddleware(uuid.New(), false))
	if err != nil {
		t.Fatalf("NewDiagnosisReportRoutes(): %v", err)
	}
	router := NewRouter(zap.NewNop(), func(context.Context) error { return nil }, routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/diagnosis-tasks/not-a-uuid/report", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"field":"taskId"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type diagnosisReportUseCaseStub struct {
	report diagnosis.DiagnosisReport
	err    error
	actor  diagnosis.TaskActor
	taskID uuid.UUID
}

func (s *diagnosisReportUseCaseStub) Get(
	_ context.Context,
	actor diagnosis.TaskActor,
	taskID uuid.UUID,
) (diagnosis.DiagnosisReport, error) {
	s.actor, s.taskID = actor, taskID
	return s.report, s.err
}
