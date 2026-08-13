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
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const evidenceTestReportContract = "configured report contract"

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
	requests := invoker.snapshotRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].UserQuery, evidenceTestReportContract) {
		t.Fatalf("configured report contract was not injected: %+v", requests)
	}
	for _, step := range result.Investigation {
		if strings.Contains(strings.ToLower(step.Summary), "reasoningcontent") {
			t.Fatalf("investigation leaked raw reasoning: %+v", step)
		}
	}
}

func TestEvidenceOrchestratorCanDisableEarlyExitForPairedEvaluation(t *testing.T) {
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{
		{result: evidenceRunResult(t, validEvidenceReport())},
		{result: evidenceRunResult(t, validEvidenceReport())},
	}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{
		DisableEarlyExit: true,
		MaxAgentRuns:     2,
	})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.Partial || result.AgentRuns != 2 {
		t.Fatalf("disabled Early Exit did not use the paired-run budget: %+v", result)
	}
	requests := invoker.snapshotRequests()
	if len(requests) != 2 || !strings.Contains(requests[1].UserQuery, "Early Exit 已关闭") {
		t.Fatalf("baseline continuation request = %+v", requests)
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
		!strings.Contains(requests[1].UserQuery, "<previous_report>") ||
		!strings.Contains(requests[1].UserQuery, "search_knowledge，但最多一次") {
		t.Fatalf("supplemental request = %+v", requests)
	}
	if result.AgenticRetrievalAttempted || result.AgenticRetrievalAddedEvidence ||
		result.AgenticRetrievalStopReason != "not_selected" {
		t.Fatalf("unexpected Agentic retrieval observation: %+v", result)
	}
}

func TestEvidenceOrchestratorRecordsAgenticKnowledgeEvidence(t *testing.T) {
	versionID, chunkID := uuid.NewString(), uuid.NewString()
	contentHash := strings.Repeat("a", 64)
	invalid := validEvidenceReport()
	invalid.Evidence = nil
	knowledgeReport := validEvidenceReport()
	knowledgeReport.Evidence = []ReportEvidence{{
		Claim: "报工接口需要核对网关超时", SourceTool: ToolSearchKnowledge,
		SourceRef: "evidence:knowledge-2", SupportType: EvidenceSupports,
	}}
	knowledgeRun := evidenceRunResult(t, knowledgeReport)
	knowledgeRun.ToolExecutions = []ToolExecution{{
		Name: ToolSearchKnowledge, Succeeded: true, EvidenceID: "evidence:knowledge-2",
	}}
	knowledgeRun.EvidenceItems = []EvidenceItem{{
		ID: "evidence:knowledge-2", SourceType: EvidenceSourceKnowledgeChunk,
		SourceTool: ToolSearchKnowledge, SourceRef: "evidence:knowledge-2",
		CollectedAt: time.Unix(2, 0).UTC(), Summary: "knowledge evidence",
		Snapshot: `{"results":[{"documentVersionId":"` + versionID + `","chunkId":"` + chunkID +
			`","contentSha256":"` + contentHash + `"}]}`, ContentHash: "sha256:knowledge-2",
	}}
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{
		{result: evidenceRunResult(t, invalid)}, {result: knowledgeRun},
	}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || !result.AgenticRetrievalAttempted || !result.AgenticRetrievalAddedEvidence ||
		result.AgenticRetrievalStopReason != "new_evidence_added" ||
		!slices.ContainsFunc(result.Investigation, func(step InvestigationStep) bool {
			return step.Kind == InvestigationRetrieval && step.Status == "completed"
		}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestAgenticRetrievalDoesNotCountRepeatedChunkAsNewEvidence(t *testing.T) {
	versionID, chunkID := uuid.NewString(), uuid.NewString()
	contentHash := strings.Repeat("b", 64)
	snapshot := `{"query":"first","results":[{"documentVersionId":"` + versionID +
		`","chunkId":"` + chunkID + `","contentSha256":"` + contentHash + `"}]}`
	known := knowledgeEvidenceKeySet([]EvidenceItem{{
		SourceTool: ToolSearchKnowledge, Snapshot: snapshot,
	}})
	repeatedSnapshot := strings.Replace(snapshot, `"first"`, `"rewritten"`, 1)
	state := &evidenceState{agentRuns: 2, agenticRetrievalEligible: true}
	state.observeAgenticRetrieval(RunResult{
		ToolExecutions: []ToolExecution{{Name: ToolSearchKnowledge, Succeeded: true}},
		EvidenceItems:  []EvidenceItem{{SourceTool: ToolSearchKnowledge, Snapshot: repeatedSnapshot}},
	}, known, nil)
	if !state.agenticRetrievalAttempted || state.agenticRetrievalAddedEvidence ||
		state.agenticRetrievalStopReason != "no_new_evidence" {
		t.Fatalf("state = %+v", state)
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

func TestEvidenceOrchestratorBoundsMalformedReportRepair(t *testing.T) {
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{
		{result: RunResult{Answer: "not-json-1"}},
		{result: RunResult{Answer: "not-json-2"}},
		{result: evidenceRunResult(t, validEvidenceReport())},
	}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{MaxAgentRuns: 4})
	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.AgentRuns != 2 || len(invoker.snapshotRequests()) != 2 {
		t.Fatalf("malformed report repair was not bounded: %+v", result)
	}
}

func TestEvidenceOrchestratorBoundsParsedContractRepair(t *testing.T) {
	invalid := validEvidenceReport()
	invalid.Evidence = nil
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{
		{result: evidenceRunResult(t, invalid)},
		{result: evidenceRunResult(t, invalid)},
		{result: evidenceRunResult(t, validEvidenceReport())},
	}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{MaxAgentRuns: 4})
	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.AgentRuns != 2 || len(invoker.snapshotRequests()) != 2 {
		t.Fatalf("parsed report contract repair was not bounded: %+v", result)
	}
}

func TestEvidenceOrchestratorRequiresRepairThenFailPolicy(t *testing.T) {
	_, err := NewEvidenceOrchestrator(context.Background(), EvidenceOrchestratorConfig{
		Runner: &scriptedAgentInvoker{}, Logger: zap.NewNop(),
		ReportContractInstruction: evidenceTestReportContract,
	})
	if err == nil {
		t.Fatal("NewEvidenceOrchestrator accepted a missing report policy")
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

func TestEvidenceOrchestratorReturnsPartialWhenContextWindowIsBlocked(t *testing.T) {
	observation := DiagnosisContextObservation{
		PreflightCalls: 1, HighWaterTokens: 190, AvailableInputTokens: 176,
		HighWaterRatio: 190.0 / 176.0, HardWindowBlockedCount: 1,
		LastEstimatedUpperBoundTokens: 190, ReportOutputReserveTokens: 64,
		ToolGrowthReserveTokens: 16,
	}
	invoker := &scriptedAgentInvoker{runs: []scriptedAgentRun{{
		result: RunResult{ContextObservation: observation},
		err:    ErrDiagnosisPromptWindowExceeded,
	}}}
	orchestrator := newEvidenceOrchestratorTest(t, invoker, EvidenceOrchestratorConfig{})

	result, err := orchestrator.Invoke(evidenceTestContext(t), RunRequest{UserQuery: "诊断工单"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.Partial || result.AgentRuns != 1 || result.StopReason != "context_window_exceeded" ||
		len(invoker.snapshotRequests()) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.ContextObservation.HardWindowBlockedCount != 1 ||
		result.ContextObservation.HighWaterTokens != 190 ||
		!containsText(result.MissingEvidence, "超过模型硬窗口") {
		t.Fatalf("context result = %+v", result)
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

	limitedCtx := withExecutionBudget(context.Background(), newExecutionBudget(8, 1000))
	limitedCtx = withExecutionTrace(limitedCtx, &executionTrace{})
	limitedCtx = withAgentToolRunPolicy(limitedCtx, newAgentToolRunPolicy(nil, map[string]int{
		ToolSearchKnowledge: 1,
	}))
	knowledgeInput := &compose.ToolInput{Name: ToolSearchKnowledge, Arguments: `{}`}
	if _, err := endpoint(limitedCtx, knowledgeInput); err != nil {
		t.Fatalf("first bounded knowledge call: %v", err)
	}
	if _, err := endpoint(limitedCtx, knowledgeInput); !errors.Is(err, ErrAgentToolRunLimitExhausted) {
		t.Fatalf("second bounded knowledge call error = %v", err)
	}
	if executions != 2 {
		t.Fatalf("bounded policy allowed %d total endpoint executions, want 2", executions)
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

func TestEvidenceGapClassifierSeparatesFormattingFromRetrieval(t *testing.T) {
	if evidenceGapsNeedRetrieval([]string{"缺少结论", "confidence 必须是 high、medium 或 low"}) {
		t.Fatal("format-only gaps enabled Agentic retrieval")
	}
	if !evidenceGapsNeedRetrieval([]string{"至少需要一条可追溯证据"}) {
		t.Fatal("evidence gap did not enable Agentic retrieval")
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
	overrides.ReportPolicy = resilience.PolicyRepairThenFail
	overrides.ReportContractInstruction = evidenceTestReportContract
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
		AllowedCapabilities:   []ToolCapability{ToolCapabilityCase},
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
