package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/externalcase"
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
	result, err := runner.Invoke(WithTaskScope(context.Background(), scope), RunRequest{
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
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	result, err := runner.Invoke(WithTaskScope(context.Background(), scope), RunRequest{
		UserQuery: "请诊断工单", ExternalCaseID: runnerTestCaseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(result.Answer, "已根据工单证据") || !strings.Contains(result.Answer, githubUnavailableMessage) {
		t.Fatalf("degraded answer = %q", result.Answer)
	}
	if slices.Contains(result.AllowedTools, "search_code") {
		t.Fatal("unavailable GitHub Tool was exposed")
	}
}

func TestRunnerBaselineBindsBroadRoleTaskToolsWithoutSkillMiddleware(t *testing.T) {
	runner := newRunnerTestWithMode(t, &runnerModelState{baseline: true}, RunnerModeBaseline)
	result, err := runner.Invoke(WithTaskScope(context.Background(), runnerTestScope(t, ToolDependencyExternalCase)), RunRequest{
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
		SkillRoot: filepath.Join("..", "..", "config", "skills"), Mode: RunnerMode("invalid"), Logger: zap.NewNop(),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid runner mode") {
		t.Fatalf("invalid mode error = %v", err)
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
		SkillRoot:   filepath.Join("..", "..", "config", "skills"),
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
	if len(output.Result) >= 2048 || !strings.Contains(output.Result, "truncated by MESGuard") {
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
	ctx, cancel := context.WithCancel(context.Background())
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Invoke(WithTaskScope(ctx, scope), RunRequest{UserQuery: "等待取消"})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	loopRunner := newRunnerTest(t, &runnerModelState{loop: true})
	loopRunner.maxIterations = 2
	_, err := loopRunner.Invoke(WithTaskScope(context.Background(), runnerTestScope(t, ToolDependencyExternalCase)), RunRequest{UserQuery: "循环"})
	if !errors.Is(err, adk.ErrExceedMaxIterations) {
		t.Fatalf("max iterations error = %v", err)
	}
}

func TestRunnerCreatesIsolatedAgentForConcurrentRuns(t *testing.T) {
	runner := newRunnerTest(t, &runnerModelState{})
	scope := runnerTestScope(t, ToolDependencyExternalCase)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Invoke(WithTaskScope(context.Background(), scope), RunRequest{UserQuery: "并发诊断"})
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
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: uuid.New(), Role: auth.RoleAnalyst, TaskType: TaskTypeDiagnosis,
		DataSources:           []ScopedDataSource{{ID: uuid.New(), Role: DataSourceRoleCaseSource, SafetyMode: DataSourceSafetyReadOnly}},
		AvailableDependencies: dependencies,
	})
	if err != nil {
		t.Fatalf("NewTaskScope: %v", err)
	}
	return scope
}
