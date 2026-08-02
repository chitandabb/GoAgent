package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type scriptedAgentRun struct {
	result RunResult
	err    error
}

type scriptedAgentInvoker struct {
	mu       sync.Mutex
	runs     []scriptedAgentRun
	requests []RunRequest
}

func (i *scriptedAgentInvoker) Invoke(_ context.Context, request RunRequest) (RunResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.requests = append(i.requests, request)
	if len(i.runs) == 0 {
		return RunResult{}, errors.New("unexpected Agent run")
	}
	run := i.runs[0]
	i.runs = i.runs[1:]
	return run.result, run.err
}

func (i *scriptedAgentInvoker) snapshotRequests() []RunRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]RunRequest(nil), i.requests...)
}

func TestEvidenceOrchestratorAcceptsValidReport(t *testing.T) {
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{{result: evidenceRunResult(t, validEvidenceReport())}}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{
		UserQuery: "诊断工单", ExternalCaseID: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.Partial || result.AgentRuns != 1 || result.Report.ConclusionStatus != ConclusionProbable {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.EvidenceItems) != 1 || result.Report.Evidence[0].SourceRef != result.EvidenceItems[0].SourceRef {
		t.Fatalf("report evidence was not bound to runtime EvidenceItem: %+v", result)
	}
	if len(result.MissingEvidence) != 0 || len(result.Investigation) < 4 {
		t.Fatalf("unexpected evidence trace: %+v", result)
	}
	for _, step := range result.Investigation {
		if strings.Contains(strings.ToLower(step.Summary), "reasoningcontent") {
			t.Fatalf("investigation leaked raw reasoning: %+v", step)
		}
	}
}

func TestEvidenceOrchestratorRetriesOnlyForEvidenceGaps(t *testing.T) {
	invalid := validEvidenceReport()
	invalid.Evidence = nil
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{
		{result: evidenceRunResult(t, invalid)},
		{result: evidenceRunResult(t, validEvidenceReport())},
	}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.Partial || result.AgentRuns != 2 {
		t.Fatalf("unexpected retry result: %+v", result)
	}
	requests := invoker.snapshotRequests()
	if len(requests) != 2 || !strings.Contains(requests[1].UserQuery, "至少需要一条可追溯证据") ||
		!strings.Contains(requests[1].UserQuery, "<previous_report>") {
		t.Fatalf("supplemental request = %+v", requests)
	}
}

func TestEvidenceOrchestratorRejectsUnknownEvidenceReference(t *testing.T) {
	report := validEvidenceReport()
	report.Evidence[0].SourceRef = "evidence:missing"
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{{result: evidenceRunResult(t, report)}}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{MaxAgentRuns: 1})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.Partial || !containsText(result.MissingEvidence, "未对应本次运行的 EvidenceItem") {
		t.Fatalf("unknown evidence reference was accepted: %+v", result)
	}
}

func TestEvidenceOrchestratorReturnsPartialAtAgentRunLimit(t *testing.T) {
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{{result: RunResult{
		Answer: "这不是结构化报告", Usage: ModelUsage{ModelCalls: 1, TotalTokens: 100},
	}}}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{MaxAgentRuns: 1})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.Partial || result.AgentRuns != 1 || result.Report.ConclusionStatus != ConclusionInconclusive ||
		result.Report.Confidence != ConfidenceLow {
		t.Fatalf("unexpected partial report: %+v", result)
	}
	if result.StopReason != "evidence_gate_partial" {
		t.Fatalf("stop reason = %q, want evidence_gate_partial", result.StopReason)
	}
	if !containsText(result.MissingEvidence, "结构化报告") {
		t.Fatalf("missing evidence = %v", result.MissingEvidence)
	}
}

func TestEvidenceOrchestratorStopsBeforeSecondRunAtTokenLimit(t *testing.T) {
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{{result: RunResult{
		Answer: "{}", Usage: ModelUsage{ModelCalls: 1, TotalTokens: 1000},
	}}}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{
		MaxAgentRuns: 2, MaxTotalTokens: 1000,
	})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.Partial || result.AgentRuns != 1 || len(invoker.snapshotRequests()) != 1 {
		t.Fatalf("token budget did not stop retry: %+v", result)
	}
	if result.StopReason != "token_budget_exhausted" {
		t.Fatalf("stop reason = %q, want token_budget_exhausted", result.StopReason)
	}
	if !containsText(result.MissingEvidence, "Token 预算") {
		t.Fatalf("missing evidence = %v", result.MissingEvidence)
	}
}

func TestToolMiddlewareEnforcesBudgetAndCancellationBeforeExecution(t *testing.T) {
	executions := 0
	next := func(_ context.Context, _ *compose.ToolInput) (*compose.ToolOutput, error) {
		executions++
		return &compose.ToolOutput{Result: "ok"}, nil
	}
	endpoint := newToolTraceMiddleware(1024).Invokable(next)
	budgetCtx := withExecutionBudget(context.Background(), newExecutionBudget(1, 1000))
	budgetCtx = withExecutionTrace(budgetCtx, &executionTrace{})
	input := &compose.ToolInput{Name: ToolReadExternalCase, Arguments: `{}`}
	if _, err := endpoint(budgetCtx, input); err != nil {
		t.Fatalf("first tool call: %v", err)
	}
	if _, err := endpoint(budgetCtx, input); !errors.Is(err, ErrToolCallBudgetExhausted) {
		t.Fatalf("second tool call error = %v", err)
	}
	if executions != 1 {
		t.Fatalf("tool executions = %d", executions)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled = withExecutionBudget(cancelled, newExecutionBudget(1, 1000))
	if _, err := endpoint(cancelled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled tool call error = %v", err)
	}
	if executions != 1 {
		t.Fatalf("cancelled request started a tool; executions = %d", executions)
	}
}

func TestExecutionBudgetCancelsCurrentRunAfterTokenSettlement(t *testing.T) {
	budget := newExecutionBudget(8, 1000)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generation := budget.bindRunCancel(cancel)
	defer budget.unbindRunCancel(generation)
	budget.recordUsage(ModelUsage{TotalTokens: 1000})
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("token settlement did not cancel current run")
	}
	if !budget.exhausted() {
		t.Fatal("token budget is not exhausted")
	}
}

func TestStructuredReportRejectsUnknownFieldsAndUnexecutedTools(t *testing.T) {
	answer := `{"conclusionStatus":"probable","riskLevel":"medium","conclusion":"x","businessSummary":"x","technicalSummary":"x","evidence":[],"limitations":[],"confidence":"medium","unknown":true}`
	if _, err := decodeStructuredReport(answer); err == nil {
		t.Fatal("decoder accepted unknown report field")
	}
	report := validEvidenceReport()
	report.Evidence[0].SourceTool = "not_executed"
	if gaps := validateStructuredReport(&report, []string{ToolReadExternalCase}, 16); !containsText(gaps, "未在本次任务中成功执行") {
		t.Fatalf("validation gaps = %v", gaps)
	}
}

func newEvidenceOrchestratorTest(
	t *testing.T,
	invoker AgentInvoker,
	overrides EvidenceOrchestratorConfig,
) *EvidenceOrchestrator {
	t.Helper()
	overrides.Runner = invoker
	overrides.Logger = zap.NewNop()
	orchestrator, err := NewEvidenceOrchestrator(context.Background(), overrides)
	if err != nil {
		t.Fatalf("NewEvidenceOrchestrator: %v", err)
	}
	return orchestrator
}

func evidenceTestContext(t *testing.T) context.Context {
	t.Helper()
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources: []ScopedDataSource{{
			ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly,
		}},
		AvailableDependencies: []ToolDependency{ToolDependencyExternalCase},
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}
	return WithTaskScope(context.Background(), scope)
}

func evidenceRunResult(t *testing.T, report StructuredReport) RunResult {
	t.Helper()
	answer, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal report: %v", err)
	}
	return RunResult{
		Answer: string(answer),
		ToolExecutions: []ToolExecution{{
			Name: ToolReadExternalCase, Succeeded: true, DurationMS: 2, EvidenceID: "evidence:test-case",
		}},
		EvidenceItems: []EvidenceItem{{
			ID: "evidence:test-case", SourceType: EvidenceSourceCaseSnapshot,
			SourceTool: ToolReadExternalCase, SourceRef: "evidence:test-case",
			CollectedAt: time.Unix(1, 0).UTC(), Summary: "test case snapshot",
			Snapshot: `{"externalCaseKey":"TKT-1"}`, ContentHash: "sha256:test",
		}},
		Usage:          ModelUsage{ModelCalls: 2, PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
		AllowedTools:   []string{ToolReadExternalCase, ToolSkill},
		ExecutedSkills: []SkillID{SkillTicketDiagnosis},
	}
}

func validEvidenceReport() StructuredReport {
	return StructuredReport{
		ConclusionStatus: ConclusionProbable,
		RiskLevel:        RiskMedium,
		Conclusion:       "报工状态同步链路可能异常。",
		BusinessSummary:  "报工已经提交，但状态同步尚未完成。",
		TechnicalSummary: "工单证据指向报工后的状态流转环节。",
		Evidence: []ReportEvidence{{
			Claim: "报工后状态未更新", SourceTool: ToolReadExternalCase,
			SourceRef: "evidence:test-case", SupportType: EvidenceSupports,
		}},
		Limitations: []string{"尚未接入 SQL 调查工具"},
		Confidence:  ConfidenceMedium,
	}
}

func containsText(values []string, fragment string) bool {
	return slices.ContainsFunc(values, func(value string) bool { return strings.Contains(value, fragment) })
}
