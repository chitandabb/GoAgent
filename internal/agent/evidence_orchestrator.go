package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
	"go.uber.org/zap"
)

const (
	defaultEvidenceMaxAgentRuns   = 2
	defaultEvidenceMaxToolCalls   = 8
	defaultEvidenceMaxItems       = 16
	defaultEvidenceMaxTotalTokens = 16_000
	defaultEvidenceTimeout        = 90 * time.Second
	defaultEvidenceMaxPromptBytes = 16 * 1024
	evidenceNodePrepareContext    = "prepare_context"
	evidenceNodeAgentLoop         = "agent_loop"
	evidenceNodeGate              = "evidence_gate"
	evidenceNodeReport            = "report"
	evidenceNodePartialReport     = "partial_report"
	evidenceGraphName             = "mesguard-evidence-gate"
)

var (
	ErrToolCallBudgetExhausted = errors.New("agent tool call budget exhausted")
	ErrTokenBudgetExhausted    = errors.New("agent token budget exhausted")
)

type AgentInvoker interface {
	Invoke(ctx context.Context, request RunRequest) (RunResult, error)
}

type EvidenceOrchestratorConfig struct {
	Runner                    AgentInvoker
	Logger                    *zap.Logger
	MaxAgentRuns              int
	MaxToolCalls              int
	MaxEvidenceItems          int
	MaxTotalTokens            int
	Timeout                   time.Duration
	ReportContractInstruction string
}

type InvestigationStepKind string

const (
	InvestigationAgentRun  InvestigationStepKind = "agent_run"
	InvestigationTool      InvestigationStepKind = "tool"
	InvestigationGate      InvestigationStepKind = "evidence_gate"
	InvestigationRetrieval InvestigationStepKind = "agentic_retrieval"
	InvestigationReport    InvestigationStepKind = "report"
)

// InvestigationStep 是可以展示给用户的脱敏调查轨迹，不包含模型原始 ReasoningContent。
type InvestigationStep struct {
	Sequence   int                   `json:"sequence"`
	Kind       InvestigationStepKind `json:"kind"`
	Title      string                `json:"title"`
	Summary    string                `json:"summary"`
	Status     string                `json:"status"`
	ToolName   string                `json:"toolName,omitempty"`
	DurationMS int64                 `json:"durationMs,omitempty"`
}

type OrchestrationResult struct {
	Report                        StructuredReport    `json:"report"`
	Partial                       bool                `json:"partial"`
	MissingEvidence               []string            `json:"missingEvidence"`
	AgentRuns                     int                 `json:"agentRuns"`
	ToolExecutions                []ToolExecution     `json:"toolExecutions"`
	EvidenceItems                 []EvidenceItem      `json:"evidenceItems"`
	Usage                         ModelUsage          `json:"usage"`
	AllowedTools                  []string            `json:"allowedTools"`
	SelectedSkill                 SkillID             `json:"selectedSkill"`
	StopReason                    string              `json:"stopReason,omitempty"`
	ExecutedSkills                []SkillID           `json:"executedSkills"`
	Investigation                 []InvestigationStep `json:"investigation"`
	AgenticRetrievalAttempted     bool                `json:"agenticRetrievalAttempted"`
	AgenticRetrievalAddedEvidence bool                `json:"agenticRetrievalAddedEvidence"`
	AgenticRetrievalStopReason    string              `json:"agenticRetrievalStopReason"`
}

type EvidenceOrchestrator struct {
	runner                    AgentInvoker
	log                       *zap.Logger
	maxAgentRuns              int
	maxToolCalls              int
	maxEvidenceItems          int
	maxTotalTokens            int
	timeout                   time.Duration
	reportContractInstruction string
	graph                     compose.Runnable[*evidenceState, *evidenceState]
}

type evidenceState struct {
	request                       RunRequest
	budget                        *executionBudget
	agentRuns                     int
	draft                         string
	parsedReport                  *StructuredReport
	parseError                    error
	report                        StructuredReport
	partial                       bool
	gaps                          []string
	nextNode                      string
	toolExecutions                []ToolExecution
	evidenceItems                 []EvidenceItem
	usage                         ModelUsage
	allowedTools                  []string
	selectedSkill                 SkillID
	stopReason                    string
	executedSkills                []SkillID
	investigation                 []InvestigationStep
	lastAgentFailure              string
	agenticRetrievalEligible      bool
	agenticRetrievalAttempted     bool
	agenticRetrievalAddedEvidence bool
	agenticRetrievalStopReason    string
	maxEvidenceItems              int
	reportContractInstruction     string
}

func NewEvidenceOrchestrator(ctx context.Context, cfg EvidenceOrchestratorConfig) (*EvidenceOrchestrator, error) {
	if cfg.Runner == nil || cfg.Logger == nil {
		return nil, errors.New("evidence orchestrator runner and logger are required")
	}
	applyEvidenceDefaults(&cfg)
	if err := validateEvidenceConfig(cfg); err != nil {
		return nil, err
	}
	orchestrator := &EvidenceOrchestrator{
		runner: cfg.Runner, log: cfg.Logger,
		maxAgentRuns: cfg.MaxAgentRuns, maxToolCalls: cfg.MaxToolCalls,
		maxEvidenceItems: cfg.MaxEvidenceItems, maxTotalTokens: cfg.MaxTotalTokens,
		timeout:                   cfg.Timeout,
		reportContractInstruction: strings.TrimSpace(cfg.ReportContractInstruction),
	}
	runnable, err := orchestrator.buildGraph(ctx)
	if err != nil {
		return nil, fmt.Errorf("build Evidence Gate graph: %w", err)
	}
	orchestrator.graph = runnable
	return orchestrator, nil
}

func applyEvidenceDefaults(cfg *EvidenceOrchestratorConfig) {
	if cfg.MaxAgentRuns == 0 {
		cfg.MaxAgentRuns = defaultEvidenceMaxAgentRuns
	}
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = defaultEvidenceMaxToolCalls
	}
	if cfg.MaxEvidenceItems == 0 {
		cfg.MaxEvidenceItems = defaultEvidenceMaxItems
	}
	if cfg.MaxTotalTokens == 0 {
		cfg.MaxTotalTokens = defaultEvidenceMaxTotalTokens
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultEvidenceTimeout
	}
}

func validateEvidenceConfig(cfg EvidenceOrchestratorConfig) error {
	if strings.TrimSpace(cfg.ReportContractInstruction) == "" {
		return errors.New("evidence report contract instruction is required")
	}
	if cfg.MaxAgentRuns < 1 || cfg.MaxAgentRuns > 4 {
		return errors.New("evidence max Agent runs must be between 1 and 4")
	}
	if cfg.MaxToolCalls < 1 || cfg.MaxToolCalls > 64 {
		return errors.New("evidence max tool calls must be between 1 and 64")
	}
	if cfg.MaxEvidenceItems < 1 || cfg.MaxEvidenceItems > 128 {
		return errors.New("evidence max items must be between 1 and 128")
	}
	if cfg.MaxTotalTokens < 1000 || cfg.MaxTotalTokens > 1_000_000 {
		return errors.New("evidence max total tokens must be between 1000 and 1000000")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 10*time.Minute {
		return errors.New("evidence timeout must be between 1 second and 10 minutes")
	}
	return nil
}

func (o *EvidenceOrchestrator) buildGraph(ctx context.Context) (compose.Runnable[*evidenceState, *evidenceState], error) {
	graph := compose.NewGraph[*evidenceState, *evidenceState]()
	if err := graph.AddLambdaNode(evidenceNodePrepareContext, compose.InvokableLambda(o.prepareContext)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(evidenceNodeAgentLoop, compose.InvokableLambda(o.runAgent)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(evidenceNodeGate, compose.InvokableLambda(o.checkEvidence)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(evidenceNodeReport, compose.InvokableLambda(o.finishReport)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode(evidenceNodePartialReport, compose.InvokableLambda(o.finishPartialReport)); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(compose.START, evidenceNodePrepareContext); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(evidenceNodePrepareContext, evidenceNodeAgentLoop); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(evidenceNodeAgentLoop, evidenceNodeGate); err != nil {
		return nil, err
	}
	if err := graph.AddBranch(evidenceNodeGate, compose.NewGraphBranch(
		func(_ context.Context, state *evidenceState) (string, error) {
			return state.nextNode, nil
		},
		map[string]bool{
			evidenceNodeAgentLoop:     true,
			evidenceNodeReport:        true,
			evidenceNodePartialReport: true,
		},
	)); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(evidenceNodeReport, compose.END); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(evidenceNodePartialReport, compose.END); err != nil {
		return nil, err
	}
	// prepare + N * (agent + gate) + report，再加一步作为框架级死循环保护。
	maxRunSteps := 2*o.maxAgentRuns + 3
	return graph.Compile(ctx, compose.WithGraphName(evidenceGraphName), compose.WithMaxRunSteps(maxRunSteps))
}

func (o *EvidenceOrchestrator) Invoke(ctx context.Context, request RunRequest) (OrchestrationResult, error) {
	if o == nil || o.graph == nil {
		return OrchestrationResult{}, errors.New("evidence orchestrator is nil")
	}
	state := &evidenceState{
		request: request, maxEvidenceItems: o.maxEvidenceItems,
		budget:                    newExecutionBudget(o.maxToolCalls, o.maxTotalTokens),
		reportContractInstruction: o.reportContractInstruction,
	}
	taskCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	startedAt := time.Now()
	output, err := o.graph.Invoke(taskCtx, state)
	if err != nil {
		if ctx.Err() != nil {
			return OrchestrationResult{}, ctx.Err()
		}
		if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
			state.gaps = append(state.gaps, "诊断总耗时预算已耗尽")
			state.finishPartial()
			output = state
		} else {
			return OrchestrationResult{}, fmt.Errorf("run Evidence Gate graph: %w", err)
		}
	}
	result := output.result()
	o.log.Info("Evidence orchestration completed",
		zap.Bool("partial", result.Partial),
		zap.Int("agent_runs", result.AgentRuns),
		zap.Int("tool_calls", len(result.ToolExecutions)),
		zap.Int("total_tokens", result.Usage.TotalTokens),
		zap.Duration("duration", time.Since(startedAt)),
	)
	return result, nil
}

func (o *EvidenceOrchestrator) prepareContext(ctx context.Context, state *evidenceState) (*evidenceState, error) {
	if state == nil {
		return nil, errors.New("evidence graph state is nil")
	}
	if err := state.request.Validate(); err != nil {
		return nil, err
	}
	if _, ok := TaskScopeFromContext(ctx); !ok {
		return nil, ErrTaskScopeRequired
	}
	return state, nil
}

func (o *EvidenceOrchestrator) runAgent(ctx context.Context, state *evidenceState) (*evidenceState, error) {
	if err := ctx.Err(); err != nil {
		return state, err
	}
	state.agentRuns++
	var runToolPolicy *agentToolRunPolicy
	if state.agentRuns > 1 {
		state.agenticRetrievalEligible = state.agentRuns == 2 && evidenceGapsNeedRetrieval(state.gaps)
		if state.agenticRetrievalEligible {
			runToolPolicy = newAgentToolRunPolicy(nil, map[string]int{ToolSearchKnowledge: 1})
		} else {
			runToolPolicy = newAgentToolRunPolicy([]string{ToolSearchKnowledge}, nil)
		}
	}
	request := state.request
	request.UserQuery = state.nextAgentQuery()
	runCtx := withExecutionBudget(ctx, state.budget)
	runCtx = withAgentToolRunPolicy(runCtx, runToolPolicy)
	knownKnowledge := knowledgeEvidenceKeySet(state.evidenceItems)
	result, err := o.runner.Invoke(runCtx, request)
	state.mergeRunResult(result)
	state.budget.reconcileToolCalls(len(state.toolExecutions))
	state.budget.reconcileTotalTokens(state.usage.TotalTokens)
	state.draft = result.Answer
	state.parsedReport, state.parseError = decodeStructuredReport(result.Answer)
	state.appendRunSteps(result, err)
	state.observeAgenticRetrieval(result, knownKnowledge, err)
	if err != nil {
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
		if errors.Is(err, ErrToolCallBudgetExhausted) || errors.Is(err, ErrTokenBudgetExhausted) ||
			state.budget.exhausted() {
			state.lastAgentFailure = "本轮调查因总预算停止"
			state.gaps = append(state.gaps, state.lastAgentFailure)
			return state, nil
		}
		return state, fmt.Errorf("run evidence Agent: %w", err)
	}
	return state, nil
}

func (o *EvidenceOrchestrator) checkEvidence(_ context.Context, state *evidenceState) (*evidenceState, error) {
	gaps := make([]string, 0, 8)
	if state.parseError != nil {
		gaps = append(gaps, "模型输出不是可校验的结构化报告")
	}
	gaps = append(gaps, validateStructuredReport(
		state.parsedReport,
		state.successfulTools(),
		o.maxEvidenceItems,
	)...)
	gaps = append(gaps, validateEvidenceReferences(state.parsedReport, state.evidenceItems)...)
	state.gaps = uniqueStrings(append(state.gaps, gaps...))
	if len(gaps) == 0 && state.parsedReport != nil {
		state.nextNode = evidenceNodeReport
		state.appendStep(InvestigationGate, "证据门禁", "报告字段与证据引用校验通过", "completed", "", 0)
		return state, nil
	}
	if state.canRunAgain(o.maxAgentRuns) {
		state.nextNode = evidenceNodeAgentLoop
		state.appendStep(InvestigationGate, "证据门禁", strings.Join(gaps, "；"), "needs_evidence", "", 0)
		return state, nil
	}
	if state.agenticRetrievalStopReason == "" && evidenceGapsNeedRetrieval(gaps) {
		state.agenticRetrievalStopReason = "run_limit_or_budget"
	}
	state.nextNode = evidenceNodePartialReport
	state.gaps = uniqueStrings(append(state.gaps, state.budget.exhaustionReasons()...))
	state.appendStep(InvestigationGate, "证据门禁", strings.Join(state.gaps, "；"), "partial", "", 0)
	return state, nil
}

func (o *EvidenceOrchestrator) finishReport(_ context.Context, state *evidenceState) (*evidenceState, error) {
	state.report = *state.parsedReport
	state.partial = false
	state.gaps = nil
	state.appendStep(InvestigationReport, "诊断报告", "已生成通过证据门禁的诊断报告", "completed", "", 0)
	return state, nil
}

func (o *EvidenceOrchestrator) finishPartialReport(_ context.Context, state *evidenceState) (*evidenceState, error) {
	state.finishPartial()
	return state, nil
}

func (s *evidenceState) nextAgentQuery() string {
	if s.agentRuns == 1 {
		return strings.TrimSpace(s.request.UserQuery) + "\n\n" + s.reportContractInstruction
	}
	draft := truncatePromptValue(s.draft, defaultEvidenceMaxPromptBytes)
	retrievalInstruction := "本轮只修正报告结构与已有证据引用，不要重新检索企业知识。"
	if s.agentRuns == 2 && s.agenticRetrievalEligible {
		retrievalInstruction = "如现有证据仍不足，可以调用 search_knowledge，但最多一次；若没有新增证据，必须明确保留限制。"
	}
	return strings.TrimSpace(s.request.UserQuery) +
		"\n\n上一轮报告未通过 Evidence Gate。请只针对以下缺口补充必要调查，并重新输出完整 JSON 报告：\n- " +
		strings.Join(s.gaps, "\n- ") +
		"\n\n" + retrievalInstruction +
		"\n\n<previous_report>\n" + draft + "\n</previous_report>\n\n" + s.reportContractInstruction
}

func evidenceGapsNeedRetrieval(gaps []string) bool {
	markers := []string{
		"可追溯证据", "sourcetool 未在本次任务中成功执行",
		"sourceref 未对应本次运行", "来源不一致",
	}
	for _, gap := range gaps {
		lower := strings.ToLower(gap)
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func knowledgeEvidenceKeySet(items []EvidenceItem) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range items {
		if item.SourceTool != ToolSearchKnowledge {
			continue
		}
		for _, key := range knowledgeEvidenceKeys(item.Snapshot) {
			result[key] = struct{}{}
		}
	}
	return result
}

func knowledgeEvidenceKeys(snapshot string) []string {
	var response searchKnowledgeResponse
	if json.Unmarshal([]byte(snapshot), &response) != nil {
		return nil
	}
	seen := make(map[string]struct{})
	keys := make([]string, 0, len(response.Results))
	appendKey := func(versionID, chunkID, contentSHA256 string) {
		if versionID == "" || chunkID == "" || contentSHA256 == "" {
			return
		}
		key := versionID + "/" + chunkID + "/" + contentSHA256
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, item := range response.Results {
		appendKey(item.DocumentVersionID, item.ChunkID, item.ContentSHA256)
	}
	for _, group := range response.ContextGroups {
		for _, chunk := range group.Chunks {
			appendKey(group.DocumentVersionID, chunk.ChunkID, chunk.ContentSHA256)
		}
	}
	return keys
}

func (s *evidenceState) observeAgenticRetrieval(
	result RunResult,
	knownKnowledge map[string]struct{},
	runErr error,
) {
	if s.agentRuns != 2 {
		return
	}
	if !s.agenticRetrievalEligible {
		s.agenticRetrievalStopReason = "not_eligible"
		return
	}
	failed := false
	for _, execution := range result.ToolExecutions {
		if execution.Name != ToolSearchKnowledge {
			continue
		}
		s.agenticRetrievalAttempted = true
		if !execution.Succeeded {
			failed = true
		}
	}
	if !s.agenticRetrievalAttempted {
		if runErr != nil {
			s.agenticRetrievalStopReason = "agent_run_failed"
		} else {
			s.agenticRetrievalStopReason = "not_selected"
		}
		s.appendStep(InvestigationRetrieval, "Agentic 二次检索", "模型未选择企业知识检索", "skipped", ToolSearchKnowledge, 0)
		return
	}
	for key := range knowledgeEvidenceKeySet(s.evidenceItems) {
		if _, exists := knownKnowledge[key]; !exists {
			s.agenticRetrievalAddedEvidence = true
			break
		}
	}
	if s.agenticRetrievalAddedEvidence {
		s.agenticRetrievalStopReason = "new_evidence_added"
		s.appendStep(InvestigationRetrieval, "Agentic 二次检索", "补充了新的企业知识证据", "completed", ToolSearchKnowledge, 0)
		return
	}
	if failed {
		s.agenticRetrievalStopReason = "tool_failed"
		s.appendStep(InvestigationRetrieval, "Agentic 二次检索", "企业知识检索失败，未补充证据", "failed", ToolSearchKnowledge, 0)
		return
	}
	s.agenticRetrievalStopReason = "no_new_evidence"
	s.appendStep(InvestigationRetrieval, "Agentic 二次检索", "检索未产生新的可追溯证据", "stopped", ToolSearchKnowledge, 0)
}

func (s *evidenceState) mergeRunResult(result RunResult) {
	if s.selectedSkill == "" {
		s.selectedSkill = result.SkillID
	}
	s.toolExecutions = append(s.toolExecutions, result.ToolExecutions...)
	for _, item := range result.EvidenceItems {
		if item.SourceRef == "" || slices.ContainsFunc(s.evidenceItems, func(current EvidenceItem) bool {
			return current.SourceRef == item.SourceRef
		}) {
			continue
		}
		if len(s.evidenceItems) >= s.maxEvidenceItems {
			s.gaps = append(s.gaps, fmt.Sprintf("证据快照数量超过上限 %d", s.maxEvidenceItems))
			break
		}
		s.evidenceItems = append(s.evidenceItems, item)
	}
	s.usage.Add(result.Usage)
	s.allowedTools = uniqueStrings(append(s.allowedTools, result.AllowedTools...))
	for _, skillID := range result.ExecutedSkills {
		if skillID != "" && !slices.Contains(s.executedSkills, skillID) {
			s.executedSkills = append(s.executedSkills, skillID)
		}
	}
}

func (s *evidenceState) appendRunSteps(result RunResult, runErr error) {
	status := "completed"
	summary := fmt.Sprintf("第 %d 轮调查完成，执行 %d 个工具", s.agentRuns, len(result.ToolExecutions))
	if runErr != nil {
		status = "stopped"
		summary = fmt.Sprintf("第 %d 轮调查提前停止", s.agentRuns)
	}
	s.appendStep(InvestigationAgentRun, "Agent 调查", summary, status, "", 0)
	for _, execution := range result.ToolExecutions {
		toolStatus := "completed"
		toolSummary := "只读工具执行完成"
		if !execution.Succeeded {
			toolStatus = "failed"
			toolSummary = "只读工具执行失败，未将结果作为证据"
		}
		s.appendStep(
			InvestigationTool,
			"工具调用",
			toolSummary,
			toolStatus,
			execution.Name,
			execution.DurationMS,
		)
	}
}

func (s *evidenceState) successfulTools() []string {
	tools := make([]string, 0, len(s.toolExecutions))
	for _, execution := range s.toolExecutions {
		if execution.Succeeded && !slices.Contains(tools, execution.Name) {
			tools = append(tools, execution.Name)
		}
	}
	return tools
}

func (s *evidenceState) canRunAgain(maxAgentRuns int) bool {
	return s.agentRuns < maxAgentRuns && !s.budget.exhausted()
}

func (s *evidenceState) finishPartial() {
	s.partial = true
	exhaustionReasons := s.budget.exhaustionReasons()
	s.gaps = uniqueStrings(append(s.gaps, exhaustionReasons...))
	if s.stopReason == "" {
		s.stopReason = stopReasonForPartial(exhaustionReasons)
	}
	if len(s.gaps) == 0 {
		s.gaps = []string{"现有证据不足以形成可靠结论"}
	}
	partial := StructuredReport{
		ConclusionStatus: ConclusionInconclusive,
		RiskLevel:        RiskHigh,
		Conclusion:       "当前证据不足，无法形成可靠结论。",
		BusinessSummary:  "本次调查已停止，需要补充证据后再判断。",
		TechnicalSummary: "Evidence Gate 未通过，当前内容仅作为待验证线索。",
		Limitations:      append([]string(nil), s.gaps...),
		Confidence:       ConfidenceLow,
	}
	if s.parsedReport != nil {
		if conclusion := strings.TrimSpace(s.parsedReport.Conclusion); conclusion != "" {
			partial.Conclusion = conclusion
		}
		if summary := strings.TrimSpace(s.parsedReport.BusinessSummary); summary != "" {
			partial.BusinessSummary = summary
		}
		if summary := strings.TrimSpace(s.parsedReport.TechnicalSummary); summary != "" {
			partial.TechnicalSummary = summary
		}
		if s.parsedReport.RiskLevel.Valid() {
			partial.RiskLevel = s.parsedReport.RiskLevel
		}
		partial.Evidence = validEvidenceSubset(
			s.parsedReport.Evidence,
			s.successfulTools(),
			s.evidenceItems,
			s.maxEvidenceItems,
		)
		partial.Limitations = uniqueStrings(append(partial.Limitations, s.parsedReport.Limitations...))
	}
	s.report = partial
	s.appendStep(InvestigationReport, "部分报告", "证据或预算不足，已明确列出限制", "partial", "", 0)
}

func validEvidenceSubset(
	evidence []ReportEvidence,
	successfulTools []string,
	evidenceItems []EvidenceItem,
	maxItems int,
) []ReportEvidence {
	result := make([]ReportEvidence, 0, min(len(evidence), maxItems))
	for _, item := range evidence {
		if strings.TrimSpace(item.Claim) != "" && strings.TrimSpace(item.SourceRef) != "" &&
			item.SupportType.Valid() && slices.Contains(successfulTools, item.SourceTool) &&
			containsEvidenceReference(evidenceItems, item.SourceRef, item.SourceTool) {
			result = append(result, item)
			if len(result) == maxItems {
				break
			}
		}
	}
	return result
}

func containsEvidenceReference(items []EvidenceItem, sourceRef, sourceTool string) bool {
	return slices.ContainsFunc(items, func(item EvidenceItem) bool {
		return item.SourceRef == sourceRef && item.SourceTool == sourceTool
	})
}

func (s *evidenceState) appendStep(
	kind InvestigationStepKind,
	title string,
	summary string,
	status string,
	toolName string,
	durationMS int64,
) {
	s.investigation = append(s.investigation, InvestigationStep{
		Sequence: len(s.investigation) + 1, Kind: kind, Title: title,
		Summary: summary, Status: status, ToolName: toolName, DurationMS: durationMS,
	})
}

func (s *evidenceState) result() OrchestrationResult {
	return OrchestrationResult{
		Report: s.report, Partial: s.partial,
		MissingEvidence:               append([]string(nil), s.gaps...),
		AgentRuns:                     s.agentRuns,
		ToolExecutions:                append([]ToolExecution(nil), s.toolExecutions...),
		EvidenceItems:                 append([]EvidenceItem(nil), s.evidenceItems...),
		Usage:                         s.usage,
		AllowedTools:                  append([]string(nil), s.allowedTools...),
		SelectedSkill:                 s.selectedSkill,
		StopReason:                    s.stopReason,
		ExecutedSkills:                append([]SkillID(nil), s.executedSkills...),
		Investigation:                 append([]InvestigationStep(nil), s.investigation...),
		AgenticRetrievalAttempted:     s.agenticRetrievalAttempted,
		AgenticRetrievalAddedEvidence: s.agenticRetrievalAddedEvidence,
		AgenticRetrievalStopReason:    s.agenticRetrievalResultStopReason(),
	}
}

func (s *evidenceState) agenticRetrievalResultStopReason() string {
	if s.agenticRetrievalStopReason != "" {
		return s.agenticRetrievalStopReason
	}
	if s.agentRuns < 2 {
		return "not_needed"
	}
	return "not_attempted"
}

func stopReasonForPartial(exhaustionReasons []string) string {
	for _, reason := range exhaustionReasons {
		if strings.Contains(reason, "Token") {
			return "token_budget_exhausted"
		}
	}
	for _, reason := range exhaustionReasons {
		if strings.Contains(reason, "工具调用") {
			return "tool_budget_exhausted"
		}
	}
	return "evidence_gate_partial"
}

func truncatePromptValue(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "?") + "\n[previous report truncated]"
}

type executionBudgetContextKey struct{}

type executionBudget struct {
	mu               sync.Mutex
	maxToolCalls     int
	maxTotalTokens   int
	toolCalls        int
	totalTokens      int
	toolExhausted    bool
	tokenExhausted   bool
	runGeneration    uint64
	currentRunCancel context.CancelFunc
}

func newExecutionBudget(maxToolCalls, maxTotalTokens int) *executionBudget {
	return &executionBudget{maxToolCalls: maxToolCalls, maxTotalTokens: maxTotalTokens}
}

func withExecutionBudget(ctx context.Context, budget *executionBudget) context.Context {
	return context.WithValue(ctx, executionBudgetContextKey{}, budget)
}

func executionBudgetFromContext(ctx context.Context) *executionBudget {
	budget, _ := ctx.Value(executionBudgetContextKey{}).(*executionBudget)
	return budget
}

func (b *executionBudget) reserveToolCall() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokenExhausted || b.totalTokens >= b.maxTotalTokens {
		b.tokenExhausted = true
		return ErrTokenBudgetExhausted
	}
	if b.toolCalls >= b.maxToolCalls {
		b.toolExhausted = true
		return ErrToolCallBudgetExhausted
	}
	b.toolCalls++
	return nil
}

func (b *executionBudget) bindRunCancel(cancel context.CancelFunc) uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runGeneration++
	b.currentRunCancel = cancel
	return b.runGeneration
}

func (b *executionBudget) unbindRunCancel(generation uint64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runGeneration == generation {
		b.currentRunCancel = nil
	}
}

func (b *executionBudget) recordUsage(usage ModelUsage) {
	if b == nil || usage.TotalTokens <= 0 {
		return
	}
	b.mu.Lock()
	b.totalTokens += usage.TotalTokens
	if b.totalTokens >= b.maxTotalTokens {
		b.tokenExhausted = true
	}
	cancel := b.currentRunCancel
	exhausted := b.tokenExhausted
	b.mu.Unlock()
	if exhausted && cancel != nil {
		cancel()
	}
}

func (b *executionBudget) reconcileTotalTokens(total int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if total > b.totalTokens {
		b.totalTokens = total
	}
	if b.totalTokens >= b.maxTotalTokens {
		b.tokenExhausted = true
	}
	b.mu.Unlock()
}

func (b *executionBudget) reconcileToolCalls(total int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if total > b.toolCalls {
		b.toolCalls = total
	}
	if b.toolCalls >= b.maxToolCalls {
		b.toolExhausted = true
	}
	b.mu.Unlock()
}

func (b *executionBudget) exhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.toolExhausted || b.tokenExhausted || b.toolCalls >= b.maxToolCalls || b.totalTokens >= b.maxTotalTokens
}

func (b *executionBudget) exhaustionReasons() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	reasons := make([]string, 0, 2)
	if b.toolExhausted || b.toolCalls >= b.maxToolCalls {
		reasons = append(reasons, fmt.Sprintf("工具调用预算已达到上限 %d", b.maxToolCalls))
	}
	if b.tokenExhausted || b.totalTokens >= b.maxTotalTokens {
		reasons = append(reasons, fmt.Sprintf("模型 Token 预算已达到上限 %d", b.maxTotalTokens))
	}
	return reasons
}
