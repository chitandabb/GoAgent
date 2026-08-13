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

	"github.com/chitandabb/GoAgent/internal/observability"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultAgentMaxIterations      = 12
	defaultAgentTimeout            = 90 * time.Second
	defaultMaxToolResultBytes      = 32 * 1024
	ToolSkill                      = "skill"
	githubUnavailableMessage       = "GitHub MCP 工具暂时不可用"
	githubNotAuthorizedMessage     = "当前任务未授权代码调查能力"
	githubCodeSearchPendingMessage = "GitHub Code Search 返回不完整；当前搜索不能证明没有匹配代码，也不能据此判断仓库索引状态。已知路径或提交可继续通过仓库树、文件和提交读取核验。"
	sqlServerUnavailableMessage    = "SQL Server 调查工具暂时不可用"
	sqlNotAuthorizedMessage        = "当前任务未授权 SQL 调查能力"
)

var (
	ErrSkillUnavailable = errors.New("skill is unavailable")
	ErrToolNotAllowed   = errors.New("tool is not registered or allowed")
)

type ArgumentRewriter func(ctx context.Context, toolName, arguments string) (string, error)

// RunnerMode controls how a Runner is assembled for a single evaluation.
// Production callers should leave it empty, which defaults to experiment.
type RunnerMode string

const (
	RunnerModeExperiment RunnerMode = "experiment"
	RunnerModeBaseline   RunnerMode = "baseline"
)

func (m RunnerMode) Valid() bool {
	return m == RunnerModeExperiment || m == RunnerModeBaseline
}

type RunRequest struct {
	UserQuery      string  `json:"userQuery"`
	ExternalCaseID string  `json:"externalCaseId,omitempty"`
	RequestedSkill SkillID `json:"requestedSkill,omitempty"`
	CaseSnapshot   string  `json:"-"`
}

func (r RunRequest) Validate() error {
	if strings.TrimSpace(r.UserQuery) == "" {
		return errors.New("user query is required")
	}
	if r.RequestedSkill != "" && !skillIDPattern.MatchString(string(r.RequestedSkill)) {
		return fmt.Errorf("invalid requested skill %q", r.RequestedSkill)
	}
	return nil
}

type ToolExecution struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"durationMs"`
	Succeeded  bool   `json:"succeeded"`
	Degraded   bool   `json:"degraded,omitempty"`
	Error      string `json:"error,omitempty"`
	EvidenceID string `json:"evidenceId,omitempty"`
}

type RunResult struct {
	SkillID            SkillID                     `json:"skillId"`
	RouteReason        string                      `json:"routeReason"`
	Answer             string                      `json:"answer"`
	AllowedTools       []string                    `json:"allowedTools"`
	ToolExecutions     []ToolExecution             `json:"toolExecutions"`
	EvidenceItems      []EvidenceItem              `json:"evidenceItems"`
	Usage              ModelUsage                  `json:"usage"`
	ExecutedSkills     []SkillID                   `json:"executedSkills"`
	ContextObservation DiagnosisContextObservation `json:"contextObservation"`
}

type RunnerConfig struct {
	ChatModel             model.ToolCallingChatModel
	ToolCatalog           *ToolCatalog
	SkillRuntime          *NativeSkillRuntime
	SystemInstruction     string
	BaselineInstruction   string
	Mode                  RunnerMode
	GitHubArgumentRewrite ArgumentRewriter
	Logger                *zap.Logger
	MaxIterations         int
	Timeout               time.Duration
	MaxToolResultBytes    int
	ContextPreflight      DiagnosisContextPreflightConfig
	ModelProvider         string
	ModelID               string
}

// Runner 只保存可安全共享的只读依赖；ChatModelAgent 必须在每次 Invoke 时单独创建。
// Eino v0.9.13 会在首次 Run 时初始化 Agent 内部状态，共享同一实例并发执行会产生数据竞争。
type Runner struct {
	chatModel             model.ToolCallingChatModel
	toolCatalog           *ToolCatalog
	toolProvider          AgentToolProvider
	mode                  RunnerMode
	skillRuntime          *NativeSkillRuntime
	systemInstruction     string
	baselineInstruction   string
	toolAuthorization     *ToolAuthorizationMiddleware
	githubArgumentRewrite ArgumentRewriter
	log                   *zap.Logger
	maxIterations         int
	timeout               time.Duration
	maxToolResultBytes    int
	contextPreflight      DiagnosisContextPreflightConfig
	modelProvider         string
	modelID               string
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.ChatModel == nil || cfg.ToolCatalog == nil || cfg.SkillRuntime == nil || cfg.Logger == nil {
		return nil, errors.New("runner model, catalog, Skill runtime, and logger are required")
	}
	if strings.TrimSpace(cfg.SystemInstruction) == "" || strings.TrimSpace(cfg.BaselineInstruction) == "" {
		return nil, errors.New("runner system and baseline instructions are required")
	}
	if cfg.Mode == "" {
		cfg.Mode = RunnerModeExperiment
	}
	if !cfg.Mode.Valid() {
		return nil, fmt.Errorf("invalid runner mode %q", cfg.Mode)
	}
	var toolProvider AgentToolProvider = cfg.ToolCatalog
	if cfg.Mode == RunnerModeBaseline {
		toolProvider = evaluationBaselineToolProvider{catalog: cfg.ToolCatalog}
	}
	authorization, err := NewToolAuthorizationMiddleware(toolProvider)
	if err != nil {
		return nil, fmt.Errorf("build Tool authorization middleware: %w", err)
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = defaultAgentMaxIterations
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultAgentTimeout
	}
	if cfg.MaxToolResultBytes == 0 {
		cfg.MaxToolResultBytes = defaultMaxToolResultBytes
	}
	if cfg.MaxIterations < 1 || cfg.MaxIterations > 32 {
		return nil, errors.New("runner max iterations must be between 1 and 32")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 10*time.Minute {
		return nil, errors.New("runner timeout must be between 1 second and 10 minutes")
	}
	if cfg.MaxToolResultBytes < 1024 || cfg.MaxToolResultBytes > 1024*1024 {
		return nil, errors.New("runner max tool result bytes must be between 1024 and 1048576")
	}
	if err := cfg.ContextPreflight.validate(); err != nil {
		return nil, err
	}
	return &Runner{
		chatModel: cfg.ChatModel, toolCatalog: cfg.ToolCatalog,
		toolProvider: toolProvider, mode: cfg.Mode,
		skillRuntime:          cfg.SkillRuntime,
		systemInstruction:     strings.TrimSpace(cfg.SystemInstruction),
		baselineInstruction:   strings.TrimSpace(cfg.BaselineInstruction),
		toolAuthorization:     authorization,
		githubArgumentRewrite: cfg.GitHubArgumentRewrite, log: cfg.Logger,
		maxIterations: cfg.MaxIterations, timeout: cfg.Timeout,
		maxToolResultBytes: cfg.MaxToolResultBytes,
		contextPreflight:   cfg.ContextPreflight,
		modelProvider:      strings.TrimSpace(cfg.ModelProvider),
		modelID:            strings.TrimSpace(cfg.ModelID),
	}, nil
}

type evaluationBaselineToolProvider struct {
	catalog *ToolCatalog
}

func (p evaluationBaselineToolProvider) ToolsFor(ctx context.Context, scope TaskScope) ([]tool.BaseTool, error) {
	if p.catalog == nil {
		return nil, errors.New("evaluation baseline tool catalog is nil")
	}
	return p.catalog.EvaluationBaselineToolsFor(ctx, scope)
}

func (r *Runner) Invoke(ctx context.Context, request RunRequest) (result RunResult, err error) {
	if r == nil {
		return RunResult{}, errors.New("agent runner is nil")
	}
	startedAt := time.Now()
	traceID := ""
	runID := ""
	defer func() {
		fields := []zap.Field{
			zap.String("entry_skill", string(result.SkillID)),
			zap.Duration("duration", time.Since(startedAt)),
			zap.Int("tool_calls", len(result.ToolExecutions)),
			zap.Int("model_calls", result.Usage.ModelCalls),
			zap.Int("total_tokens", result.Usage.TotalTokens),
			zap.String("trace_id", traceID),
			zap.String("run_id", runID),
		}
		if err != nil {
			r.log.Warn("Agent run failed", append(fields, zap.Error(err))...)
			return
		}
		r.log.Info("Agent run completed", fields...)
	}()

	if err = request.Validate(); err != nil {
		return RunResult{}, err
	}
	scope, ok := TaskScopeFromContext(ctx)
	if !ok {
		return RunResult{}, ErrTaskScopeRequired
	}
	if _, ok := resilience.RunIdentityFromContext(ctx); !ok {
		ctx = resilience.WithRunIdentity(ctx, resilience.RunIdentity{RunID: uuid.NewString()})
	}
	ctx, agentSpan := observability.StartAgentRun(ctx, "diagnosis")
	ctx = observability.BindTraceIdentity(ctx)
	traceID = observability.TraceID(ctx)
	if identity, ok := resilience.RunIdentityFromContext(ctx); ok {
		runID = identity.RunID
	}
	defer func() { observability.End(agentSpan, err) }()
	allowedTools, resolveErr := r.toolProvider.ToolsFor(ctx, scope)
	if resolveErr != nil {
		return RunResult{}, fmt.Errorf("resolve run tools: %w", resolveErr)
	}
	allowedTools, err = filterAgentToolsForRun(ctx, allowedTools)
	if err != nil {
		return RunResult{}, fmt.Errorf("apply run tool policy: %w", err)
	}
	result.AllowedTools, err = toolNames(ctx, allowedTools)
	if err != nil {
		return RunResult{}, err
	}
	// Experiment 的 Skill Middleware 在 Tool 授权之后追加 skill Tool；评测必须记录
	// 模型实际看到的完整 Schema 列表。Baseline 不启用 Skill 渐进式读取。
	if r.mode == RunnerModeExperiment {
		result.AllowedTools = append(result.AllowedTools, ToolSkill)
	}
	result.SkillID, result.RouteReason, err = r.entrySkill(request, scope)
	if err != nil {
		return RunResult{}, err
	}
	entryInstruction := ""
	if r.mode == RunnerModeExperiment {
		entryInstruction, err = r.skillRuntime.Instruction(ctx, result.SkillID)
		if err != nil {
			return RunResult{}, err
		}
		result.ExecutedSkills = []SkillID{result.SkillID}
	}

	userPrompt, buildErr := BuildUserPrompt(request)
	if buildErr != nil {
		return RunResult{}, fmt.Errorf("build user prompt: %w", buildErr)
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	budget := executionBudgetFromContext(runCtx)
	budgetGeneration := budget.bindRunCancel(cancel)
	defer budget.unbindRunCancel(budgetGeneration)
	trace := &executionTrace{}
	runCtx = withExecutionTrace(runCtx, trace)
	usageTrace := &modelUsageTrace{onUsage: budget.recordUsage}

	handlers := []adk.ChatModelAgentMiddleware{r.toolAuthorization}
	instruction := buildBaselineAgentInstruction(r.baselineInstruction, result.SkillID, scope)
	if r.mode == RunnerModeExperiment {
		instruction = buildAgentInstruction(r.systemInstruction, result.SkillID, entryInstruction, scope)
		handlers = append(handlers, r.skillRuntime.Middleware)
	}
	chatModel := r.chatModel
	var contextObservation *diagnosisContextObservationRecorder
	if r.contextPreflight.Enabled && scope.taskType == TaskTypeDiagnosis {
		if strings.TrimSpace(request.CaseSnapshot) == "" {
			return result, errors.New("diagnosis case snapshot is required for context preflight")
		}
		preflightSystemInstruction := instruction
		preloadedSkill := ""
		if r.mode == RunnerModeExperiment {
			preflightSystemInstruction = buildAgentInstruction(
				r.systemInstruction, result.SkillID, "", scope,
			)
			preloadedSkill = entryInstruction
		}
		chatModel, contextObservation = newDiagnosisContextGuardModel(
			r.chatModel,
			r.contextPreflight,
			diagnosisPromptSeed{
				SystemInstruction: preflightSystemInstruction,
				PreloadedSkill:    preloadedSkill,
				CaseSnapshot:      request.CaseSnapshot,
			},
		)
		defer func() {
			result.ContextObservation = contextObservation.snapshot(trace.toolResultTruncatedSnapshot())
		}()
	}
	agentInstance, buildErr := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:        "mesguard-diagnosis",
		Description: "使用受控只读工具辅助分析工业软件工单",
		Instruction: instruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			ExecuteSequentially:  true,
			UnknownToolsHandler:  rejectUnknownTool,
			ToolArgumentsHandler: r.rewriteToolArguments,
			ToolCallMiddlewares: []compose.ToolMiddleware{
				newToolObservabilityMiddleware(),
				newToolTraceMiddleware(r.maxToolResultBytes),
			},
		}},
		MaxIterations: r.maxIterations,
		Handlers:      handlers,
	})
	if buildErr != nil {
		return result, fmt.Errorf("build per-run ADK Agent: %w", buildErr)
	}

	iterator := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: agentInstance}).Query(
		runCtx,
		userPrompt,
		adk.WithCallbacks(
			newModelUsageHandler(usageTrace),
			newModelTracingHandler(r.modelProvider, r.modelID),
		),
	)
	for {
		event, more := iterator.Next()
		if !more {
			break
		}
		if event.Err != nil {
			result.ToolExecutions = trace.snapshot()
			result.EvidenceItems = trace.evidenceSnapshot()
			result.ExecutedSkills = mergeSkillIDs(result.ExecutedSkills, trace.skillSnapshot())
			result.Usage = usageTrace.snapshot()
			return result, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		output := event.Output.MessageOutput
		message, messageErr := output.GetMessage()
		if messageErr != nil {
			result.ToolExecutions = trace.snapshot()
			result.EvidenceItems = trace.evidenceSnapshot()
			result.ExecutedSkills = mergeSkillIDs(result.ExecutedSkills, trace.skillSnapshot())
			result.Usage = usageTrace.snapshot()
			return result, fmt.Errorf("read ADK event: %w", messageErr)
		}
		if message != nil && output.Role == schema.Assistant && len(message.ToolCalls) == 0 &&
			strings.TrimSpace(message.Content) != "" {
			result.Answer = message.Content
		}
	}
	result.ToolExecutions = trace.snapshot()
	result.EvidenceItems = trace.evidenceSnapshot()
	result.ExecutedSkills = mergeSkillIDs(result.ExecutedSkills, trace.skillSnapshot())
	result.Usage = usageTrace.snapshot()
	if strings.TrimSpace(result.Answer) == "" {
		return result, errors.New("Agent returned no final answer")
	}
	if trace.codeSearchIndexPendingSnapshot() && !strings.Contains(result.Answer, githubCodeSearchPendingMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + githubCodeSearchPendingMessage
	}
	if !scope.CapabilityAllowed(ToolCapabilityCode) &&
		slices.Contains(result.ExecutedSkills, SkillCodeInvestigation) &&
		!strings.Contains(result.Answer, githubNotAuthorizedMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + githubNotAuthorizedMessage + "。"
	} else if !scope.DependencyAvailable(ToolDependencyGitHubMCP) &&
		slices.Contains(result.ExecutedSkills, SkillCodeInvestigation) &&
		!strings.Contains(result.Answer, githubUnavailableMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + githubUnavailableMessage + "。"
	}
	if !scope.CapabilityAllowed(ToolCapabilitySQL) &&
		slices.Contains(result.ExecutedSkills, SkillSQLInvestigation) &&
		!strings.Contains(result.Answer, sqlNotAuthorizedMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + sqlNotAuthorizedMessage + "。"
	} else if !scope.DependencyAvailable(ToolDependencySQLServer) &&
		slices.Contains(result.ExecutedSkills, SkillSQLInvestigation) &&
		!strings.Contains(result.Answer, sqlServerUnavailableMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + sqlServerUnavailableMessage + "。"
	}
	return result, nil
}

func (r *Runner) entrySkill(request RunRequest, scope TaskScope) (SkillID, string, error) {
	entry := request.RequestedSkill
	reason := "requested_skill"
	if entry == "" {
		reason = "task_scope_default"
		switch scope.taskType {
		case TaskTypeDiagnosis:
			entry = SkillTicketDiagnosis
		case TaskTypeKnowledge:
			entry = SkillKnowledgeQA
		default:
			return "", "", fmt.Errorf("unsupported task type %q", scope.taskType)
		}
	}
	if scope.taskType == TaskTypeDiagnosis && entry != SkillTicketDiagnosis && entry != SkillCodeInvestigation && entry != SkillSQLInvestigation {
		return "", "", fmt.Errorf("%w for diagnosis task: %s", ErrSkillUnavailable, entry)
	}
	if scope.taskType == TaskTypeKnowledge && entry != SkillKnowledgeQA {
		return "", "", fmt.Errorf("%w for knowledge task: %s", ErrSkillUnavailable, entry)
	}
	if entry == SkillCodeInvestigation && !scope.CapabilityAllowed(ToolCapabilityCode) {
		return "", "", fmt.Errorf("%w: code capability is not authorized", ErrSkillUnavailable)
	}
	if entry == SkillSQLInvestigation && !scope.CapabilityAllowed(ToolCapabilitySQL) {
		return "", "", fmt.Errorf("%w: SQL capability is not authorized", ErrSkillUnavailable)
	}
	if !r.skillRuntime.HasSkill(entry) {
		return "", "", fmt.Errorf("%w: %s", ErrSkillUnavailable, entry)
	}
	return entry, reason, nil
}

func buildAgentInstruction(baseInstruction string, entry SkillID, skillContent string, scope TaskScope) string {
	instruction := strings.TrimSpace(baseInstruction) +
		"\n\n<entry_skill name=\"" + string(entry) + "\">\n" + strings.TrimSpace(skillContent) + "\n</entry_skill>"
	if sources := scope.DataSources(); len(sources) > 0 {
		instruction += "\n\n<authorized_data_sources>\n"
		for _, source := range sources {
			instruction += fmt.Sprintf("- id=%s role=%s safety=%s\n", source.ID, source.Role, source.SafetyMode)
		}
		instruction += "</authorized_data_sources>"
	}
	if !scope.CapabilityAllowed(ToolCapabilityCode) {
		instruction += "\n\n当前任务范围未授权代码调查 Tool；Skill 内容不能扩大该能力范围。"
	} else if !scope.DependencyAvailable(ToolDependencyGitHubMCP) {
		instruction += "\n\n当前 GitHub MCP 工具暂时不可用；如果调查确实需要代码证据，保留已有证据并明确说明该限制。"
	}
	if !scope.CapabilityAllowed(ToolCapabilitySQL) {
		instruction += "\n\n当前任务范围未授权 SQL 调查 Tool；Skill 内容不能扩大该能力范围。"
	} else if !scope.DependencyAvailable(ToolDependencySQLServer) {
		instruction += "\n\n当前 SQL Server 调查工具暂时不可用；如果调查需要数据库证据，保留已有证据并明确说明该限制。"
	}
	return instruction
}

func buildBaselineAgentInstruction(baseInstruction string, entry SkillID, scope TaskScope) string {
	instruction := strings.TrimSpace(baseInstruction) +
		"\n\n<entry_task>" + string(entry) + "</entry_task>"
	if sources := scope.DataSources(); len(sources) > 0 {
		instruction += "\n\n<authorized_data_sources>\n"
		for _, source := range sources {
			instruction += fmt.Sprintf("- id=%s role=%s safety=%s\n", source.ID, source.Role, source.SafetyMode)
		}
		instruction += "</authorized_data_sources>"
	}
	return instruction
}

func (r *Runner) rewriteToolArguments(ctx context.Context, name, arguments string) (string, error) {
	if !slices.Contains(GitHubReadOnlyTools, name) {
		return arguments, nil
	}
	if r.githubArgumentRewrite == nil {
		return "", fmt.Errorf("GitHub argument policy is unavailable for tool %q", name)
	}
	return r.githubArgumentRewrite(ctx, name, arguments)
}

func rejectUnknownTool(_ context.Context, name, _ string) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
}

type executionTrace struct {
	mu                     sync.Mutex
	entries                []ToolExecution
	evidence               []EvidenceItem
	loadedSkills           []SkillID
	codeSearchIndexPending bool
	toolResultTruncated    int
}

type traceContextKey struct{}

func withExecutionTrace(ctx context.Context, trace *executionTrace) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func traceFromContext(ctx context.Context) *executionTrace {
	trace, _ := ctx.Value(traceContextKey{}).(*executionTrace)
	return trace
}

func (t *executionTrace) append(entry ToolExecution, skill SkillID, evidence *EvidenceItem) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if evidence != nil {
		entry.EvidenceID = evidence.ID
		t.evidence = append(t.evidence, *evidence)
	}
	t.entries = append(t.entries, entry)
	if skill != "" && !slices.Contains(t.loadedSkills, skill) {
		t.loadedSkills = append(t.loadedSkills, skill)
	}
}

func (t *executionTrace) snapshot() []ToolExecution {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ToolExecution(nil), t.entries...)
}

func (t *executionTrace) skillSnapshot() []SkillID {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]SkillID(nil), t.loadedSkills...)
}

func (t *executionTrace) evidenceSnapshot() []EvidenceItem {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]EvidenceItem(nil), t.evidence...)
}

func (t *executionTrace) markCodeSearchIndexPending() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.codeSearchIndexPending = true
}

func (t *executionTrace) codeSearchIndexPendingSnapshot() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.codeSearchIndexPending
}

func (t *executionTrace) markToolResultTruncated() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.toolResultTruncated++
	t.mu.Unlock()
}

func (t *executionTrace) toolResultTruncatedSnapshot() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolResultTruncated
}

func newToolTraceMiddleware(maxResultBytes int) compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := agentToolRunPolicyFromContext(ctx).reserve(input.Name); err != nil {
				return nil, err
			}
			if err := executionBudgetFromContext(ctx).reserveToolCall(); err != nil {
				return nil, err
			}
			startedAt := time.Now()
			output, err := next(ctx, input)
			codeSearchIndexPending := err == nil && output != nil &&
				isGitHubCodeSearchIndexPendingResult(input.Name, output.Result)
			if codeSearchIndexPending {
				traceFromContext(ctx).markCodeSearchIndexPending()
			}
			modelResultTruncated := false
			var evidence *EvidenceItem
			if err == nil && output != nil {
				rawResult := output.Result
				if item, ok := newToolEvidenceItem(input.Name, rawResult, false); ok {
					evidence = &item
					output.Result, modelResultTruncated = wrapToolResultWithEvidence(item, rawResult, maxResultBytes)
				} else if len(rawResult) > maxResultBytes {
					modelResultTruncated = true
					output.Result = truncateModelToolResult(rawResult, maxResultBytes)
				}
			}
			if modelResultTruncated {
				traceFromContext(ctx).markToolResultTruncated()
			}
			entry := ToolExecution{
				Name: input.Name, DurationMS: time.Since(startedAt).Milliseconds(),
				Succeeded: err == nil, Degraded: codeSearchIndexPending || modelResultTruncated,
			}
			if err != nil {
				entry.Error = "tool execution failed"
			}
			loadedSkill := SkillID("")
			if err == nil && input.Name == ToolSkill {
				var payload struct {
					Skill SkillID `json:"skill"`
				}
				if json.Unmarshal([]byte(input.Arguments), &payload) == nil {
					loadedSkill = payload.Skill
				}
			}
			traceFromContext(ctx).append(entry, loadedSkill, evidence)
			return output, err
		}
	}}
}

func truncateModelToolResult(value string, maxBytes int) string {
	const marker = "\n" + toolResultTruncationPrefix + "]"
	if len(value) <= maxBytes {
		return value
	}
	contentLimit := maxBytes - len(marker)
	if contentLimit < 0 {
		contentLimit = 0
	}
	return strings.ToValidUTF8(value[:contentLimit], "?") + marker
}

// wrapToolResultWithEvidence 给模型一个可原样引用的 sourceRef，同时保留工具原始
// JSON 作为 data。若证据引用包装本身超过工具结果预算，则保留引用和截断标记，
// 不让包装层突破 Runner 的最大结果字节限制。
func wrapToolResultWithEvidence(item EvidenceItem, snapshot string, maxBytes int) (string, bool) {
	data := json.RawMessage(snapshot)
	if !json.Valid(data) {
		encoded, _ := json.Marshal(snapshot)
		data = encoded
	}
	type envelope struct {
		EvidenceRef  string                     `json:"evidenceRef"`
		SourceType   EvidenceSourceType         `json:"sourceType"`
		CollectedAt  time.Time                  `json:"collectedAt"`
		Truncated    bool                       `json:"truncated"`
		Data         json.RawMessage            `json:"data,omitempty"`
		Preview      string                     `json:"preview,omitempty"`
		Continuation map[string]json.RawMessage `json:"continuation,omitempty"`
	}
	value := envelope{
		EvidenceRef: item.SourceRef, SourceType: item.SourceType,
		CollectedAt: item.CollectedAt, Truncated: item.Truncated, Data: data,
	}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= maxBytes {
		return string(encoded), false
	}
	value.Truncated = true
	value.Data = nil
	value.Continuation = extractEvidenceContinuation(snapshot, maxBytes/3)
	value.Preview = snapshot
	for len(value.Preview) > 0 {
		encoded, err = json.Marshal(value)
		if err == nil && len(encoded) <= maxBytes {
			return string(encoded), true
		}
		nextLength := len(value.Preview) * 3 / 4
		if nextLength >= len(value.Preview) {
			nextLength = len(value.Preview) - 1
		}
		value.Preview = strings.ToValidUTF8(value.Preview[:max(nextLength, 0)], "?")
	}
	encoded, err = json.Marshal(value)
	if err == nil && len(encoded) <= maxBytes {
		return string(encoded), true
	}
	// The configured minimum is large enough for this fallback. Keep a final
	// deterministic string in case a caller supplies an unusually small limit.
	return fmt.Sprintf(`{"evidenceRef":%q,"sourceType":%q,"truncated":true}`, item.SourceRef, item.SourceType), true
}

func extractEvidenceContinuation(snapshot string, maxBytes int) map[string]json.RawMessage {
	if maxBytes < 1 {
		return nil
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(snapshot), &payload) != nil {
		return nil
	}
	allowed := []string{
		"continuation", "continuationCursor", "continuationToken", "cursor", "hasMore",
		"nextCursor", "nextOffset", "offset", "contentOffsetRunes", "contentEndRunes",
		"contentComplete", "windowComplete", "truncated",
	}
	result := make(map[string]json.RawMessage)
	used := 0
	for _, key := range allowed {
		value, ok := payload[key]
		if !ok || !json.Valid(value) || len(value) > 2048 {
			continue
		}
		cost := len(key) + len(value) + 6
		if used+cost > maxBytes {
			continue
		}
		result[key] = append(json.RawMessage(nil), value...)
		used += cost
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func toolNames(ctx context.Context, tools []tool.BaseTool) ([]string, error) {
	names := make([]string, 0, len(tools))
	for _, current := range tools {
		info, err := current.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read authorized tool info: %w", err)
		}
		if info == nil {
			return nil, errors.New("authorized tool returned nil info")
		}
		names = append(names, info.Name)
	}
	return names, nil
}

func mergeSkillIDs(base, extra []SkillID) []SkillID {
	result := append([]SkillID(nil), base...)
	for _, skillID := range extra {
		if skillID != "" && !slices.Contains(result, skillID) {
			result = append(result, skillID)
		}
	}
	return result
}
