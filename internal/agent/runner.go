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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	defaultAgentMaxIterations   = 12
	defaultAgentTimeout         = 90 * time.Second
	defaultMaxToolResultBytes   = 32 * 1024
	ToolSkill                   = "skill"
	githubUnavailableMessage    = "GitHub MCP 工具暂时不可用"
	sqlServerUnavailableMessage = "SQL Server 调查工具暂时不可用"
)

var (
	ErrSkillUnavailable = errors.New("skill is unavailable")
	ErrToolNotAllowed   = errors.New("tool is not registered or allowed")
)

type ArgumentRewriter func(ctx context.Context, toolName, arguments string) (string, error)

type RunRequest struct {
	UserQuery      string  `json:"userQuery"`
	ExternalCaseID string  `json:"externalCaseId,omitempty"`
	RequestedSkill SkillID `json:"requestedSkill,omitempty"`
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
	Error      string `json:"error,omitempty"`
}

type RunResult struct {
	SkillID        SkillID         `json:"skillId"`
	RouteReason    string          `json:"routeReason"`
	Answer         string          `json:"answer"`
	AllowedTools   []string        `json:"allowedTools"`
	ToolExecutions []ToolExecution `json:"toolExecutions"`
	Usage          ModelUsage      `json:"usage"`
	ExecutedSkills []SkillID       `json:"executedSkills"`
}

type RunnerConfig struct {
	ChatModel             model.ToolCallingChatModel
	ToolCatalog           *ToolCatalog
	SkillRuntime          *NativeSkillRuntime
	GitHubArgumentRewrite ArgumentRewriter
	Logger                *zap.Logger
	MaxIterations         int
	Timeout               time.Duration
	MaxToolResultBytes    int
}

// Runner 只保存可安全共享的只读依赖；ChatModelAgent 必须在每次 Invoke 时单独创建。
// Eino v0.9.13 会在首次 Run 时初始化 Agent 内部状态，共享同一实例并发执行会产生数据竞争。
type Runner struct {
	chatModel             model.ToolCallingChatModel
	toolCatalog           *ToolCatalog
	skillRuntime          *NativeSkillRuntime
	toolAuthorization     *ToolAuthorizationMiddleware
	githubArgumentRewrite ArgumentRewriter
	log                   *zap.Logger
	maxIterations         int
	timeout               time.Duration
	maxToolResultBytes    int
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.ChatModel == nil || cfg.ToolCatalog == nil || cfg.SkillRuntime == nil || cfg.Logger == nil {
		return nil, errors.New("runner model, catalog, Skill runtime, and logger are required")
	}
	authorization, err := NewToolAuthorizationMiddleware(cfg.ToolCatalog)
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
	return &Runner{
		chatModel: cfg.ChatModel, toolCatalog: cfg.ToolCatalog,
		skillRuntime: cfg.SkillRuntime, toolAuthorization: authorization,
		githubArgumentRewrite: cfg.GitHubArgumentRewrite, log: cfg.Logger,
		maxIterations: cfg.MaxIterations, timeout: cfg.Timeout,
		maxToolResultBytes: cfg.MaxToolResultBytes,
	}, nil
}

func (r *Runner) Invoke(ctx context.Context, request RunRequest) (result RunResult, err error) {
	if r == nil {
		return RunResult{}, errors.New("agent runner is nil")
	}
	startedAt := time.Now()
	defer func() {
		fields := []zap.Field{
			zap.String("entry_skill", string(result.SkillID)),
			zap.Duration("duration", time.Since(startedAt)),
			zap.Int("tool_calls", len(result.ToolExecutions)),
			zap.Int("model_calls", result.Usage.ModelCalls),
			zap.Int("total_tokens", result.Usage.TotalTokens),
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
	allowedTools, resolveErr := r.toolCatalog.ToolsFor(ctx, scope)
	if resolveErr != nil {
		return RunResult{}, fmt.Errorf("resolve run tools: %w", resolveErr)
	}
	result.AllowedTools, err = toolNames(ctx, allowedTools)
	if err != nil {
		return RunResult{}, err
	}
	// Skill Middleware 在 Tool 授权之后追加 skill Tool；评测必须记录模型实际看到的完整 Schema 列表。
	result.AllowedTools = append(result.AllowedTools, ToolSkill)
	result.SkillID, result.RouteReason, err = r.entrySkill(request, scope)
	if err != nil {
		return RunResult{}, err
	}
	entryInstruction, loadErr := r.skillRuntime.Instruction(ctx, result.SkillID)
	if loadErr != nil {
		return RunResult{}, loadErr
	}
	result.ExecutedSkills = []SkillID{result.SkillID}

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

	agentInstance, buildErr := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:        "mesguard-diagnosis",
		Description: "使用受控只读工具辅助分析工业软件工单",
		Instruction: buildAgentInstruction(result.SkillID, entryInstruction, scope),
		Model:       r.chatModel,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			ExecuteSequentially:  true,
			UnknownToolsHandler:  rejectUnknownTool,
			ToolArgumentsHandler: r.rewriteToolArguments,
			ToolCallMiddlewares: []compose.ToolMiddleware{
				newToolTraceMiddleware(r.maxToolResultBytes),
			},
		}},
		MaxIterations: r.maxIterations,
		Handlers: []adk.ChatModelAgentMiddleware{
			r.toolAuthorization,
			r.skillRuntime.Middleware,
		},
	})
	if buildErr != nil {
		return result, fmt.Errorf("build per-run ADK Agent: %w", buildErr)
	}

	iterator := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: agentInstance}).Query(
		runCtx,
		userPrompt,
		adk.WithCallbacks(newModelUsageHandler(usageTrace)),
	)
	for {
		event, more := iterator.Next()
		if !more {
			break
		}
		if event.Err != nil {
			result.ToolExecutions = trace.snapshot()
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
			return result, fmt.Errorf("read ADK event: %w", messageErr)
		}
		if message != nil && output.Role == schema.Assistant && len(message.ToolCalls) == 0 &&
			strings.TrimSpace(message.Content) != "" {
			result.Answer = message.Content
		}
	}
	result.ToolExecutions = trace.snapshot()
	result.ExecutedSkills = mergeSkillIDs(result.ExecutedSkills, trace.skillSnapshot())
	result.Usage = usageTrace.snapshot()
	if strings.TrimSpace(result.Answer) == "" {
		return result, errors.New("Agent returned no final answer")
	}
	if !scope.DependencyAvailable(ToolDependencyGitHubMCP) &&
		slices.Contains(result.ExecutedSkills, SkillCodeInvestigation) &&
		!strings.Contains(result.Answer, githubUnavailableMessage) {
		result.Answer = strings.TrimSpace(result.Answer) + "\n\n" + githubUnavailableMessage + "。"
	}
	if !scope.DependencyAvailable(ToolDependencySQLServer) &&
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
	if !r.skillRuntime.HasSkill(entry) {
		return "", "", fmt.Errorf("%w: %s", ErrSkillUnavailable, entry)
	}
	return entry, reason, nil
}

func buildAgentInstruction(entry SkillID, skillContent string, scope TaskScope) string {
	instruction := `你是 MESGuard 工业软件分析辅助 Agent。你只能使用本次运行实际提供的只读工具，不得声称执行未提供的工具、修改 ERP/MES/代码或获得隐藏凭证。Skill 是调查指南，不授予权限。引用工具证据时区分事实、推断和待验证项；证据不足必须明确说明。` +
		"\n\n本次入口 Skill 已由应用根据页面和任务上下文确定，不要重复加载它。需要扩展调查时，可在同一 Agent 循环中通过 skill 工具按需加载其他 Skill。" +
		"\n\n<entry_skill name=\"" + string(entry) + "\">\n" + strings.TrimSpace(skillContent) + "\n</entry_skill>"
	if sources := scope.DataSources(); len(sources) > 0 {
		instruction += "\n\n<authorized_data_sources>\n"
		for _, source := range sources {
			instruction += fmt.Sprintf("- id=%s role=%s safety=%s\n", source.ID, source.Role, source.SafetyMode)
		}
		instruction += "</authorized_data_sources>"
	}
	if !scope.DependencyAvailable(ToolDependencyGitHubMCP) {
		instruction += "\n\n当前 GitHub MCP 工具暂时不可用；如果调查确实需要代码证据，保留已有证据并明确说明该限制。"
	}
	if !scope.DependencyAvailable(ToolDependencySQLServer) {
		instruction += "\n\n当前 SQL Server 调查工具暂时不可用；如果调查需要数据库证据，保留已有证据并明确说明该限制。"
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
	mu           sync.Mutex
	entries      []ToolExecution
	loadedSkills []SkillID
}

type traceContextKey struct{}

func withExecutionTrace(ctx context.Context, trace *executionTrace) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func traceFromContext(ctx context.Context) *executionTrace {
	trace, _ := ctx.Value(traceContextKey{}).(*executionTrace)
	return trace
}

func (t *executionTrace) append(entry ToolExecution, skill SkillID) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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

func newToolTraceMiddleware(maxResultBytes int) compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := executionBudgetFromContext(ctx).reserveToolCall(); err != nil {
				return nil, err
			}
			startedAt := time.Now()
			output, err := next(ctx, input)
			if output != nil && len(output.Result) > maxResultBytes {
				output.Result = strings.ToValidUTF8(output.Result[:maxResultBytes], "?") +
					"\n[tool result truncated by MESGuard]"
			}
			entry := ToolExecution{
				Name: input.Name, DurationMS: time.Since(startedAt).Milliseconds(), Succeeded: err == nil,
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
			traceFromContext(ctx).append(entry, loadedSkill)
			return output, err
		}
	}}
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
