package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var runnerTestCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

const (
	runnerTestSystemInstruction   = "configured system instruction"
	runnerTestBaselineInstruction = "configured baseline instruction"
)

type runnerTestCaseGetter struct{}

func (runnerTestCaseGetter) Get(_ context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	if id != runnerTestCaseID {
		return nil, errors.New("case not found")
	}
	return &externalcase.ExternalCase{ID: id, ExternalCaseKey: "TKT-1", Title: "报工状态未更新"}, nil
}

type runnerModelState struct {
	mu       sync.Mutex
	github   bool
	baseline bool
	loop     bool
	block    chan struct{}
	calls    int
	schemas  [][]string
}

type runnerTestModel struct {
	state *runnerModelState
	tools []*schema.ToolInfo
}

func (m *runnerTestModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &runnerTestModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *runnerTestModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.state.block != nil {
		select {
		case <-m.state.block:
			return nil, errors.New("unblocked")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	common := model.GetCommonOptions(nil, opts...)
	toolInfos := common.Tools
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	names := make([]string, 0, len(toolInfos))
	for _, info := range toolInfos {
		names = append(names, info.Name)
	}
	m.state.mu.Lock()
	m.state.calls++
	m.state.schemas = append(m.state.schemas, names)
	m.state.mu.Unlock()
	if m.state.loop {
		return runnerTestToolCall(ToolReadExternalCase, `{"externalCaseId":"11111111-1111-1111-1111-111111111111"}`), nil
	}
	var lastTool string
	for _, message := range input {
		if message.Role == schema.Tool {
			lastTool = message.ToolName
		}
	}
	if lastTool == "" {
		return runnerTestToolCall(ToolReadExternalCase, `{"externalCaseId":"11111111-1111-1111-1111-111111111111"}`), nil
	}
	if lastTool == ToolReadExternalCase {
		if m.state.baseline {
			return withRunnerTestUsage(schema.AssistantMessage("已根据工单证据形成初步诊断。", nil)), nil
		}
		return runnerTestToolCall(ToolSkill, `{"skill":"code-investigation"}`), nil
	}
	if lastTool == ToolSkill && m.state.github {
		return runnerTestToolCall("search_code", `{"query":"报工状态"}`), nil
	}
	return withRunnerTestUsage(schema.AssistantMessage("已根据工单证据形成初步诊断。", nil)), nil
}

func (m *runnerTestModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func runnerTestToolCall(name, arguments string) *schema.Message {
	return withRunnerTestUsage(schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-" + name, Function: schema.FunctionCall{Name: name, Arguments: arguments},
	}}))
}

func withRunnerTestUsage(message *schema.Message) *schema.Message {
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
	}}
	return message
}

func TestRunnerUsesOneADKLoopForMultipleSkillsAndTools(t *testing.T) {
	state := &runnerModelState{github: true}
	runner := newRunnerTest(t, state)
	scope := runnerTestScope(t, ToolDependencyExternalCase, ToolDependencyGitHubMCP)
	result, err := runner.Invoke(withRunnerTestRunAccess(context.Background(), scope), RunRequest{
		UserQuery: "请诊断工单并在有明确线索时查找代码", ExternalCaseID: runnerTestCaseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.SkillID != SkillTicketDiagnosis || result.RouteReason != "task_scope_default" {
		t.Fatalf("entry route = %s/%s", result.SkillID, result.RouteReason)
	}
	wantTools := []string{ToolReadExternalCase, ToolSkill, "search_code"}
	gotTools := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		gotTools = append(gotTools, execution.Name)
	}
	if !slices.Equal(gotTools, wantTools) {
		t.Fatalf("tool execution order = %v, want %v", gotTools, wantTools)
	}
	if !slices.Equal(result.ExecutedSkills, []SkillID{SkillTicketDiagnosis, SkillCodeInvestigation}) {
		t.Fatalf("executed Skills = %v", result.ExecutedSkills)
	}
	if !slices.Contains(result.AllowedTools, ToolSkill) || !slices.Contains(result.AllowedTools, "search_code") {
		t.Fatalf("allowed Tools = %v", result.AllowedTools)
	}
	if strings.Contains(strings.Join(gotTools, ","), "request_code_investigation") {
		t.Fatal("legacy handoff Tool was still used")
	}
	if result.Usage.ModelCalls != 4 || result.Usage.TotalTokens != 48 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.schemas) == 0 || !slices.Contains(state.schemas[0], ToolSkill) || !slices.Contains(state.schemas[0], "search_code") {
		t.Fatalf("initial Tool schema = %v", state.schemas)
	}
	if slices.Contains(state.schemas[0], "request_code_investigation") {
		t.Fatal("legacy handoff Tool leaked into schema")
	}
}

func TestRunnerReportsGitHubDegradationWithoutDroppingTicketEvidence(t *testing.T) {
	state := &runnerModelState{}
	runner := newRunnerTest(t, state)
	scope := runnerTestScopeWithCapabilities(t, []ToolCapability{ToolCapabilityCase, ToolCapabilityCode}, ToolDependencyExternalCase)
	result, err := runner.Invoke(withRunnerTestRunAccess(context.Background(), scope), RunRequest{
		UserQuery: "请诊断工单", ExternalCaseID: runnerTestCaseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(result.Answer, "已根据工单证据") || !strings.Contains(result.Answer, githubUnavailableMessage) {
		t.Fatalf("degraded answer = %q", result.Answer)
	}
	// 固定 Diagnosis Profile：依赖健康状态不能删除 GitHub Tool Schema，
	// 但执行由 RunAccess/降级链路 fail-closed。
	if !slices.Contains(result.AllowedTools, "search_code") {
		t.Fatalf("GitHub Tool Schema must stay visible with the stable profile: %v", result.AllowedTools)
	}
}

func TestRunnerBaselineBindsBroadRoleTaskToolsWithoutSkillMiddleware(t *testing.T) {
	runner := newRunnerTestWithMode(t, &runnerModelState{baseline: true}, RunnerModeBaseline)
	result, err := runner.Invoke(withRunnerTestRunAccess(context.Background(), runnerTestScope(t, ToolDependencyExternalCase)), RunRequest{
		UserQuery: "请诊断工单", ExternalCaseID: runnerTestCaseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke baseline: %v", err)
	}
	if !slices.Equal(result.AllowedTools, []string{ToolReadExternalCase, "search_code"}) {
		t.Fatalf("baseline allowed tools = %v", result.AllowedTools)
	}
	if slices.Contains(result.AllowedTools, ToolSkill) || len(result.ExecutedSkills) != 0 {
		t.Fatalf("baseline unexpectedly exposed or executed Skill: allowed=%v executed=%v", result.AllowedTools, result.ExecutedSkills)
	}
}

func TestNewRunnerRejectsInvalidMode(t *testing.T) {
	_, err := NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: &runnerTestModel{state: &runnerModelState{}}, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:         filepath.Join("..", "..", "config", "skills"),
		SystemInstruction: runnerTestSystemInstruction, BaselineInstruction: runnerTestBaselineInstruction,
		Mode: RunnerMode("invalid"), Logger: zap.NewNop(),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid runner mode") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestNewRunnerRejectsConversationBoundCatalog(t *testing.T) {
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
	})
	if err != nil {
		t.Fatalf("NewConversationDefaultToolCatalog: %v", err)
	}
	skillRuntime, err := NewNativeSkillRuntime(context.Background(), filepath.Join("..", "..", "config", "skills"))
	if err != nil {
		t.Fatalf("NewNativeSkillRuntime: %v", err)
	}
	// 诊断 Runner 只能使用 diagnosis-default Catalog；传入会话 Catalog 必须构造失败。
	_, err = NewRunner(RunnerConfig{
		ChatModel: &runnerTestModel{state: &runnerModelState{}}, ToolCatalog: catalog,
		SkillRuntime: skillRuntime, SystemInstruction: runnerTestSystemInstruction,
		BaselineInstruction: runnerTestBaselineInstruction, Logger: zap.NewNop(),
	})
	if err == nil || !strings.Contains(err.Error(), string(agentruntime.ToolProfileDiagnosis)) {
		t.Fatalf("NewRunner accepted a conversation-bound catalog, error = %v", err)
	}
}

func TestNewRunnerDefaultsToExperimentMode(t *testing.T) {
	runner := newRunnerTestWithMode(t, &runnerModelState{}, "")
	if runner.mode != RunnerModeExperiment {
		t.Fatalf("default runner mode = %q, want %q", runner.mode, RunnerModeExperiment)
	}
}

func TestRunnerRequiresTaskScope(t *testing.T) {
	runner := newRunnerTest(t, &runnerModelState{})
	_, err := runner.Invoke(context.Background(), RunRequest{UserQuery: "诊断"})
	if !errors.Is(err, ErrTaskScopeRequired) {
		t.Fatalf("missing TaskScope error = %v", err)
	}
}

func TestDefaultRunnerRejectsGitHubToolOutsideReadOnlyAllowlist(t *testing.T) {
	unsafeTool, err := toolutils.InferTool("delete_file", "unsafe", func(context.Context, struct{}) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatalf("build unsafe Tool: %v", err)
	}
	_, err = NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: &runnerTestModel{state: &runnerModelState{}}, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:         filepath.Join("..", "..", "config", "skills"),
		SystemInstruction: runnerTestSystemInstruction, BaselineInstruction: runnerTestBaselineInstruction,
		GitHubTools: []tool.BaseTool{unsafeTool},
		GitHubArgumentRewrite: func(_ context.Context, _, arguments string) (string, error) {
			return arguments, nil
		},
		Logger: zap.NewNop(),
	})
	if err == nil || !strings.Contains(err.Error(), "outside the read-only allowlist") {
		t.Fatalf("NewDefaultRunner error = %v", err)
	}
}

func TestToolTraceMiddlewareTruncatesResultAndKeepsStableError(t *testing.T) {
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	middleware := newToolTraceMiddleware(1024)
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: strings.Repeat("x", 2048)}, nil
	})
	output, err := endpoint(ctx, &compose.ToolInput{Name: "large_tool", Arguments: `{}`})
	if err != nil {
		t.Fatalf("invoke middleware: %v", err)
	}
	if len(output.Result) > 1024 || !strings.Contains(output.Result, "truncated by MESGuard") {
		t.Fatalf("tool output was not truncated: %d bytes", len(output.Result))
	}
	entries := trace.snapshot()
	if len(entries) != 1 || !entries[0].Succeeded || entries[0].Name != "large_tool" {
		t.Fatalf("trace = %+v", entries)
	}
}

func TestToolTraceMiddlewareCapturesEvidenceReferenceAndHash(t *testing.T) {
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	middleware := newToolTraceMiddleware(4096)
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: `{"returnedRows":1,"truncated":true,"rows":[["TKT-1001"]]}`}, nil
	})
	output, err := endpoint(ctx, &compose.ToolInput{Name: ToolExecuteReadonlyQuery, Arguments: `{}`})
	if err != nil {
		t.Fatalf("invoke middleware: %v", err)
	}
	items := trace.evidenceSnapshot()
	entries := trace.snapshot()
	if len(items) != 1 || len(entries) != 1 || entries[0].EvidenceID != items[0].ID {
		t.Fatalf("evidence trace = items=%+v entries=%+v", items, entries)
	}
	item := items[0]
	if item.SourceType != EvidenceSourceSQLQuery || !item.Truncated || item.ContentHash == "" || item.Snapshot == "" {
		t.Fatalf("unexpected EvidenceItem: %+v", item)
	}
	var envelope struct {
		EvidenceRef string          `json:"evidenceRef"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(output.Result), &envelope); err != nil {
		t.Fatalf("decode wrapped tool result: %v; result=%s", err, output.Result)
	}
	if envelope.EvidenceRef != item.SourceRef || !json.Valid(envelope.Data) {
		t.Fatalf("wrapped evidence reference = %+v, item=%+v", envelope, item)
	}
}

func TestToolTraceMiddlewarePersistsCompleteEvidenceAndBoundsModelResult(t *testing.T) {
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	raw := fmt.Sprintf(`{"returnedRows":1,"truncated":false,"hasMore":true,"nextCursor":"cursor-2","rows":[[%q]]}`, strings.Repeat("x", 2048))
	output, err := newToolTraceMiddleware(1024).Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: raw}, nil
	})(ctx, &compose.ToolInput{Name: ToolExecuteReadonlyQuery, Arguments: `{}`})
	if err != nil {
		t.Fatalf("invoke middleware: %v", err)
	}
	items := trace.evidenceSnapshot()
	entries := trace.snapshot()
	if len(items) != 1 || items[0].Snapshot != raw || items[0].Truncated {
		t.Fatalf("persisted evidence = %+v", items)
	}
	if len(entries) != 1 || !entries[0].Degraded {
		t.Fatalf("Tool execution = %+v", entries)
	}
	if len(output.Result) > 1024 || !strings.Contains(output.Result, `"evidenceRef"`) ||
		!strings.Contains(output.Result, `"truncated":true`) ||
		!strings.Contains(output.Result, `"preview"`) ||
		!strings.Contains(output.Result, `"nextCursor":"cursor-2"`) ||
		!strings.Contains(output.Result, `"hasMore":true`) {
		t.Fatalf("model-visible result = %s", output.Result)
	}
	if got := trace.toolResultTruncatedSnapshot(); got != 1 {
		t.Fatalf("Tool result truncation count = %d, want 1", got)
	}
}

func TestToolTraceMiddlewareCapturesKnowledgeChunkEvidence(t *testing.T) {
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	documentVersionID, chunkID := uuid.New(), uuid.New()
	content := "事务超时需要检查连接池。"
	snapshot, err := json.Marshal(searchKnowledgeResponse{
		Query: "事务超时", QueryPlan: searchKnowledgeQueryPlan{
			OriginalQuery: "事务超时", LexicalQuery: "事务超时", SemanticQuery: "事务超时",
			RewriteStatus: knowledge.QueryRewriteDisabled,
		}, Results: []searchKnowledgeResult{{
			DocumentID: uuid.NewString(), DocumentVersionID: documentVersionID.String(), ChunkID: chunkID.String(),
			Title: "生产手册", Scope: knowledge.ScopeGlobal, Ordinal: 2, ElementType: knowledge.ElementText,
			SectionPath: []string{"网络", "超时"}, ContentText: content, ContentSHA256: knowledge.SHA256Hex(content),
		}}, Sources: []string{"fts"},
	})
	if err != nil {
		t.Fatalf("marshal knowledge result: %v", err)
	}
	output, err := newToolTraceMiddleware(4096).Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: string(snapshot)}, nil
	})(ctx, &compose.ToolInput{Name: ToolSearchKnowledge, Arguments: `{"query":"事务超时"}`})
	if err != nil {
		t.Fatalf("invoke middleware: %v", err)
	}
	items := trace.evidenceSnapshot()
	if len(items) != 1 || items[0].SourceType != EvidenceSourceKnowledgeChunk ||
		!strings.Contains(items[0].Location, documentVersionID.String()) || !strings.Contains(items[0].Location, chunkID.String()) {
		t.Fatalf("knowledge evidence = %+v", items)
	}
	if !strings.Contains(output.Result, `"sourceType":"knowledge_chunk"`) {
		t.Fatalf("wrapped knowledge result = %s", output.Result)
	}
}

func TestToolTraceMiddlewareDoesNotCiteEmptyOrMalformedKnowledgeSearch(t *testing.T) {
	content := "内容"
	contentHash := knowledge.SHA256Hex(content)
	for _, test := range []struct {
		name     string
		snapshot string
	}{
		{name: "empty", snapshot: `{"query":"问题","results":[]}`},
		{name: "missing chunk", snapshot: `{"query":"问题","results":[{"documentId":"00000000-0000-0000-0000-000000000001","documentVersionId":"00000000-0000-0000-0000-000000000002","title":"手册","scope":"global","ordinal":0,"elementType":"text","contentText":"内容","contentSha256":""}]}`},
		{name: "malformed context hash", snapshot: fmt.Sprintf(`{"query":"问题","results":[{"documentId":"00000000-0000-0000-0000-000000000001","documentVersionId":"00000000-0000-0000-0000-000000000002","chunkId":"00000000-0000-0000-0000-000000000003","title":"手册","scope":"global","ordinal":1,"elementType":"text","sectionPath":["章节"],"contentText":%q,"contentSha256":%q}],"contextExpanded":true,"contextGroups":[{"documentId":"00000000-0000-0000-0000-000000000001","documentVersionId":"00000000-0000-0000-0000-000000000002","sectionPath":["章节"],"hitChunkIds":["00000000-0000-0000-0000-000000000003"],"chunks":[{"chunkId":"00000000-0000-0000-0000-000000000004","ordinal":2,"elementType":"text","contentText":"邻接内容","contentSha256":"bad"}]}]}`, content, contentHash)},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace := &executionTrace{}
			ctx := withExecutionTrace(context.Background(), trace)
			_, err := newToolTraceMiddleware(4096).Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
				return &compose.ToolOutput{Result: test.snapshot}, nil
			})(ctx, &compose.ToolInput{Name: ToolSearchKnowledge, Arguments: `{"query":"问题"}`})
			if err != nil {
				t.Fatalf("invoke middleware: %v", err)
			}
			if items := trace.evidenceSnapshot(); len(items) != 0 {
				t.Fatalf("invalid knowledge response became evidence: %+v", items)
			}
		})
	}
}

func TestToolTraceMiddlewareMarksIncompleteCodeSearchAsDegraded(t *testing.T) {
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	middleware := newToolTraceMiddleware(4096)
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: `{"status":"index_pending","tool":"search_code","attempts":3,"incomplete_results":true}`}, nil
	})
	if _, err := endpoint(ctx, &compose.ToolInput{Name: "search_code", Arguments: `{}`}); err != nil {
		t.Fatalf("invoke middleware: %v", err)
	}
	entries := trace.snapshot()
	if len(entries) != 1 || !entries[0].Succeeded || !entries[0].Degraded {
		t.Fatalf("trace = %+v", entries)
	}
	if items := trace.evidenceSnapshot(); len(items) != 0 {
		t.Fatalf("incomplete search unexpectedly became evidence: %+v", items)
	}
	if !trace.codeSearchIndexPendingSnapshot() {
		t.Fatal("trace did not retain index pending status")
	}
}

func TestRunnerHonorsCancellationAndMaxIterations(t *testing.T) {
	state := &runnerModelState{block: make(chan struct{})}
	runner := newRunnerTest(t, state)
	baseCtx, cancel := context.WithCancel(context.Background())
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	runCtx := withRunnerTestRunAccess(baseCtx, scope)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Invoke(runCtx, RunRequest{UserQuery: "等待取消"})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	loopRunner := newRunnerTest(t, &runnerModelState{loop: true})
	loopRunner.maxIterations = 2
	_, err := loopRunner.Invoke(withRunnerTestRunAccess(context.Background(), runnerTestScope(t, ToolDependencyExternalCase)), RunRequest{UserQuery: "循环"})
	if !errors.Is(err, adk.ErrExceedMaxIterations) {
		t.Fatalf("max iterations error = %v", err)
	}
}

func TestRunnerBlocksGrowingDiagnosisPromptBeforeSecondProviderCall(t *testing.T) {
	state := &runnerModelState{}
	runner := newRunnerTest(t, state)
	planner := &diagnosisGuardPlanner{plans: []contextgovernance.TokenBudgetPlan{
		{
			AvailableInputTokens: 176, EstimatedUpperBoundTokens: 100, ReservedTokens: 16,
			EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
		},
		{
			AvailableInputTokens: 176, EstimatedUpperBoundTokens: 190, ReservedTokens: 16,
			ExceedsHardWindow: true, EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
		},
	}}
	runner.contextPreflight = diagnosisContextPreflightForTest(planner)
	result, err := runner.Invoke(
		withRunnerTestRunAccess(context.Background(), runnerTestScope(t, ToolDependencyExternalCase)),
		RunRequest{
			UserQuery: "诊断工单", ExternalCaseID: runnerTestCaseID.String(),
			CaseSnapshot: `{"id":"11111111-1111-1111-1111-111111111111","title":"报工状态未更新"}`,
		},
	)
	if !errors.Is(err, ErrDiagnosisPromptWindowExceeded) {
		t.Fatalf("Invoke error = %v", err)
	}
	state.mu.Lock()
	calls := state.calls
	state.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if result.ContextObservation.PreflightCalls != 2 ||
		result.ContextObservation.HardWindowBlockedCount != 1 ||
		result.ContextObservation.HighWaterTokens != 190 {
		t.Fatalf("context observation = %+v", result.ContextObservation)
	}
}

func TestRunnerDoesNotApplyDiagnosisPreflightToKnowledgeTask(t *testing.T) {
	runner := newRunnerTest(t, &runnerModelState{})
	inner := &diagnosisGuardModel{}
	runner.chatModel = inner
	planner := &diagnosisGuardPlanner{plans: []contextgovernance.TokenBudgetPlan{{
		AvailableInputTokens: 176, EstimatedUpperBoundTokens: 190, ReservedTokens: 16,
		ExceedsHardWindow: true, EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
	}}}
	runner.contextPreflight = diagnosisContextPreflightForTest(planner)
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	scope.taskType = TaskTypeKnowledge

	result, err := runner.Invoke(
		WithTaskScope(context.Background(), scope),
		RunRequest{UserQuery: "解释事务超时"},
	)
	if err != nil {
		t.Fatalf("Invoke knowledge task: %v", err)
	}
	if result.Answer != "ok" || inner.callCount() != 1 || len(planner.requestSnapshot()) != 0 {
		t.Fatalf("result=%+v modelCalls=%d preflights=%d", result, inner.callCount(), len(planner.requestSnapshot()))
	}
}

func TestRunnerCreatesIsolatedAgentForConcurrentRuns(t *testing.T) {
	runner := newRunnerTest(t, &runnerModelState{})
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	runCtx := withRunnerTestRunAccess(context.Background(), scope)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Invoke(runCtx, RunRequest{UserQuery: "并发诊断"})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func newRunnerTest(t *testing.T, state *runnerModelState) *Runner {
	return newRunnerTestWithMode(t, state, RunnerModeExperiment)
}

func newRunnerTestWithMode(t *testing.T, state *runnerModelState, mode RunnerMode) *Runner {
	t.Helper()
	modelInstance := &runnerTestModel{state: state}
	searchTool, err := toolutils.InferTool("search_code", "只读代码搜索", func(context.Context, struct {
		Query string `json:"query" jsonschema:"required"`
	}) (string, error) {
		return "code evidence", nil
	})
	if err != nil {
		t.Fatalf("build search Tool: %v", err)
	}
	runner, err := NewDefaultRunner(context.Background(), DefaultRunnerDependencies{
		ChatModel: modelInstance, ExternalCases: runnerTestCaseGetter{},
		SkillRoot:             filepath.Join("..", "..", "config", "skills"),
		SystemInstruction:     runnerTestSystemInstruction,
		BaselineInstruction:   runnerTestBaselineInstruction,
		Mode:                  mode,
		GitHubTools:           []tool.BaseTool{searchTool},
		GitHubArgumentRewrite: func(_ context.Context, _, arguments string) (string, error) { return arguments, nil },
		Logger:                zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewDefaultRunner: %v", err)
	}
	return runner
}

func runnerTestScope(t *testing.T, dependencies ...ToolDependency) TaskScope {
	t.Helper()
	capabilities := []ToolCapability{ToolCapabilityCase}
	for _, dependency := range dependencies {
		switch dependency {
		case ToolDependencyGitHubMCP:
			capabilities = append(capabilities, ToolCapabilityCode)
		case ToolDependencySQLServer:
			capabilities = append(capabilities, ToolCapabilitySQL)
		}
	}
	return runnerTestScopeWithCapabilities(t, capabilities, dependencies...)
}

func runnerTestScopeWithCapabilities(t *testing.T, capabilities []ToolCapability, dependencies ...ToolDependency) TaskScope {
	t.Helper()
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources:           []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly}},
		AllowedCapabilities:   capabilities,
		AvailableDependencies: dependencies,
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}
	return scope
}

// withRunnerTestRunAccess 按生产绑定顺序构造测试 Context：WithTaskScope 先
// 写入兼容上下文，权威 v2 Diagnosis RunAccess（Policy 镜像测试 scope 能力 +
// runnerTestCaseID case Grant + 只读数据源 Grant）最后覆盖。它模拟 Worker
// 从有效 RunAccess 反向生成 TaskScope 后的绑定结果。
func withRunnerTestRunAccess(ctx context.Context, scope TaskScope) context.Context {
	ctx = WithTaskScope(ctx, scope)
	permissions := []agentruntime.Permission{agentruntime.PermissionCaseRead}
	if scope.CapabilityAllowed(ToolCapabilityCode) {
		permissions = append(permissions, agentruntime.PermissionCodeRead)
	}
	if scope.CapabilityAllowed(ToolCapabilitySQL) {
		permissions = append(permissions, agentruntime.PermissionSQLRead)
	}
	if scope.CapabilityAllowed(ToolCapabilityKnowledge) {
		permissions = append(permissions, agentruntime.PermissionKnowledgeRead)
	}
	if scope.CapabilityAllowed(ToolCapabilityWebSearch) {
		permissions = append(permissions, agentruntime.PermissionWebRead)
	}
	if scope.CapabilityAllowed(ToolCapabilityAttachment) {
		permissions = append(permissions, agentruntime.PermissionAttachmentRead)
	}
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		panic(err)
	}
	dataSourceIDs := make([]uuid.UUID, 0, len(scope.DataSources()))
	for _, source := range scope.DataSources() {
		if source.SafetyMode == DataSourceSafetyReadOnly {
			dataSourceIDs = append(dataSourceIDs, source.ID)
		}
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{runnerTestCaseID},
		DataSourceIDs:   dataSourceIDs,
	})
	if err != nil {
		panic(err)
	}
	policy, err := agentruntime.NewInvestigationPolicy(1, permissionSet, grants)
	if err != nil {
		panic(err)
	}
	access, err := agentruntime.DeriveDiagnosisRunAccess(
		policy,
		agentruntime.Actor{UserID: scope.UserID(), Role: scope.Role()},
		agentruntime.AccessCeiling{Permissions: permissionSet, Grants: grants},
	)
	if err != nil {
		panic(err)
	}
	return agentruntime.WithRunAccess(ctx, access)
}
