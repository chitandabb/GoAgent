package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/observability"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultConversationMaxIterations   = 8
	defaultConversationTimeout         = 60 * time.Second
	defaultConversationMaxContextRunes = 32_000
)

var (
	ErrConversationPromptWindowExceeded     = errors.New("conversation prompt exceeds the model hard window")
	ErrConversationContextPreparationFailed = errors.New("conversation context preparation failed")
)

type ConversationRunnerConfig struct {
	ChatModel                    model.ToolCallingChatModel
	CitationRepairer             ConversationCitationRepairer
	CitationRepairPolicy         resilience.Policy
	ToolCatalog                  *ToolCatalog
	SystemInstruction            string
	ModelProvider                string
	ModelID                      string
	PromptVersion                string
	Logger                       *zap.Logger
	MaxIterations                int
	MaxToolCalls                 int
	MaxTotalTokens               int
	MaxContextRunes              int
	Timeout                      time.Duration
	MaxToolResultBytes           int
	MemorySourceRecoveryEnabled  bool
	MemorySourceRecoveryMaxCalls int
	EnableStreaming              bool
	ContextPreflight             ConversationContextPreflightConfig
	// SQLDataSourceID 是启动期解析的只读数据源 UUID；非零时 Conversation
	// RunAccess 获得 sql.read 并把该 ID 作为唯一数据源 Grant。零值表示
	// 部署未配置 Conversation 只读数据源，不授予任何 sql.read。
	SQLDataSourceID uuid.UUID
}

// ConversationRunner executes one lightweight workbench turn. It is separate
// from the diagnosis Runner so chat responses are not forced through the report
// schema or Evidence Gate and cannot synchronously perform a full diagnosis.
type ConversationRunner struct {
	chatModel                    model.ToolCallingChatModel
	citationRepairer             ConversationCitationRepairer
	toolCatalog                  *ToolCatalog
	toolProfileID                agentruntime.ToolProfileID
	systemInstruction            string
	modelProvider                string
	modelID                      string
	promptVersion                string
	log                          *zap.Logger
	maxIterations                int
	maxToolCalls                 int
	maxTotalTokens               int
	maxContextRunes              int
	timeout                      time.Duration
	maxToolResultBytes           int
	memorySourceRecoveryEnabled  bool
	memorySourceRecoveryMaxCalls int
	enableStreaming              bool
	contextPreflight             ConversationContextPreflightConfig
	sqlDataSourceID              uuid.UUID
}

func NewConversationRunner(cfg ConversationRunnerConfig) (*ConversationRunner, error) {
	if cfg.ChatModel == nil || cfg.ToolCatalog == nil || cfg.Logger == nil {
		return nil, errors.New("conversation runner model, catalog, and logger are required")
	}
	if cfg.CitationRepairer != nil && cfg.CitationRepairPolicy != resilience.PolicyRepairThenFail {
		return nil, errors.New("conversation citation repair policy must be repair_then_fail")
	}
	if cfg.CitationRepairer == nil && cfg.CitationRepairPolicy != "" {
		return nil, errors.New("conversation citation repair policy requires a repairer")
	}
	cfg.SystemInstruction = strings.TrimSpace(cfg.SystemInstruction)
	cfg.ModelProvider = strings.TrimSpace(cfg.ModelProvider)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.PromptVersion = strings.TrimSpace(cfg.PromptVersion)
	if cfg.SystemInstruction == "" || cfg.ModelProvider == "" || cfg.ModelID == "" || cfg.PromptVersion == "" {
		return nil, errors.New("conversation runner instruction and model/prompt identity are required")
	}
	if (conversation.AgentRunObservation{
		ModelProvider: cfg.ModelProvider, ModelID: cfg.ModelID, PromptVersion: cfg.PromptVersion,
		Outcome: conversation.AgentRunAnswered,
	}).Validate() != nil {
		return nil, errors.New("conversation runner model or prompt identity is invalid")
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = defaultConversationMaxIterations
	}
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = 8
	}
	if cfg.MaxTotalTokens == 0 {
		cfg.MaxTotalTokens = 16_000
	}
	if cfg.MaxContextRunes == 0 {
		cfg.MaxContextRunes = defaultConversationMaxContextRunes
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultConversationTimeout
	}
	if cfg.MaxToolResultBytes == 0 {
		cfg.MaxToolResultBytes = defaultMaxToolResultBytes
	}
	if cfg.MemorySourceRecoveryEnabled && cfg.MemorySourceRecoveryMaxCalls == 0 {
		cfg.MemorySourceRecoveryMaxCalls = 2
	}
	if cfg.MaxIterations < 1 || cfg.MaxIterations > 16 {
		return nil, errors.New("conversation runner max iterations must be between 1 and 16")
	}
	if cfg.MaxToolCalls < 1 || cfg.MaxToolCalls > 32 {
		return nil, errors.New("conversation runner max tool calls must be between 1 and 32")
	}
	if cfg.MaxTotalTokens < 1_000 || cfg.MaxTotalTokens > 200_000 {
		return nil, errors.New("conversation runner max total tokens must be between 1000 and 200000")
	}
	if cfg.MaxContextRunes < conversation.MaxContentRunes || cfg.MaxContextRunes > 200_000 {
		return nil, fmt.Errorf("conversation runner max context runes must be between %d and 200000", conversation.MaxContentRunes)
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 5*time.Minute {
		return nil, errors.New("conversation runner timeout must be between 1 second and 5 minutes")
	}
	if cfg.MaxToolResultBytes < 1024 || cfg.MaxToolResultBytes > 1024*1024 {
		return nil, errors.New("conversation runner max tool result bytes must be between 1024 and 1048576")
	}
	if cfg.MemorySourceRecoveryEnabled &&
		(cfg.MemorySourceRecoveryMaxCalls < 1 || cfg.MemorySourceRecoveryMaxCalls > 2) {
		return nil, errors.New("conversation memory source recovery max calls must be between 1 and 2")
	}
	// Conversation Runner 只能使用 conversation-default Profile；误配的
	// Catalog 在构造期失败，而不是在每次请求时失败。
	if boundProfileID := cfg.ToolCatalog.BoundProfileID(); boundProfileID != agentruntime.ToolProfileConversation {
		return nil, fmt.Errorf(
			"conversation runner requires a conversation-default catalog, got profile %q", boundProfileID,
		)
	}
	if err := cfg.ContextPreflight.validate(cfg.ModelProvider, cfg.ModelID); err != nil {
		return nil, err
	}
	return &ConversationRunner{
		chatModel: cfg.ChatModel, citationRepairer: cfg.CitationRepairer, toolCatalog: cfg.ToolCatalog,
		toolProfileID: agentruntime.ToolProfileConversation, systemInstruction: cfg.SystemInstruction,
		modelProvider: cfg.ModelProvider, modelID: cfg.ModelID, promptVersion: cfg.PromptVersion,
		log: cfg.Logger, maxIterations: cfg.MaxIterations, maxToolCalls: cfg.MaxToolCalls,
		maxTotalTokens: cfg.MaxTotalTokens, maxContextRunes: cfg.MaxContextRunes,
		timeout: cfg.Timeout, maxToolResultBytes: cfg.MaxToolResultBytes,
		memorySourceRecoveryEnabled:  cfg.MemorySourceRecoveryEnabled,
		memorySourceRecoveryMaxCalls: cfg.MemorySourceRecoveryMaxCalls,
		enableStreaming:              cfg.EnableStreaming,
		contextPreflight:             cfg.ContextPreflight,
		sqlDataSourceID:              cfg.SQLDataSourceID,
	}, nil
}

func (r *ConversationRunner) Respond(ctx context.Context, request conversation.AgentRequest) (result conversation.AgentResponse, err error) {
	if r == nil {
		return conversation.AgentResponse{}, conversation.ErrAgentUnavailable
	}
	startedAt := time.Now()
	traceID := ""
	runID := ""
	var citationTrace *conversationCitationTrace
	var toolTrace *executionTrace
	var reportReferenceTrace *conversationReportReferenceTrace
	var usageTrace *modelUsageTrace
	var promptManifest *contextgovernance.PromptManifest
	var runtimePromptObservation *conversationRuntimePromptObservation
	observeFailure := false
	defer func() {
		if err != nil && observeFailure {
			if _, alreadyWrapped := conversation.AgentRunFailureRecordFrom(err); !alreadyWrapped {
				observation := r.buildRunObservation(
					startedAt, citationTrace, toolTrace, usageTrace, promptManifest, conversation.AgentRunFailed,
				)
				record := conversation.AgentRunFailureRecord{
					Observation: observation,
					ErrorType:   conversationAgentErrorType(err),
				}
				if record.Validate() == nil {
					result.RunObservation = &observation
					err = conversation.NewAgentRunFailure(err, record)
				}
			}
		}
		fields := []zap.Field{
			zap.Duration("duration", time.Since(startedAt)),
			zap.String("trace_id", traceID),
			zap.String("run_id", runID),
		}
		if err != nil {
			r.log.Warn("conversation Agent run failed", append(fields, zap.Error(err))...)
			return
		}
		r.log.Info("conversation Agent run completed", fields...)
	}()
	commandContext, ok := conversation.CommandContextFromContext(ctx)
	if !ok || commandContext.ConversationID != request.Conversation.ID ||
		commandContext.UserMessageID != request.UserMessage.ID || commandContext.Actor.UserID == uuid.Nil {
		return conversation.AgentResponse{}, conversation.ErrCommandContextRequired
	}
	if request.Conversation.ID == uuid.Nil || request.UserMessage.ID == uuid.Nil ||
		request.UserMessage.ConversationID != request.Conversation.ID ||
		request.UserMessage.Role != conversation.MessageRoleUser {
		return conversation.AgentResponse{}, conversation.ErrInvalidMessage
	}
	// 每轮只解析一次固定 Conversation Profile：用同一份 resolved.ModelVisibleNames
	// 推导 RunAccess，并复用同一份 resolved.Tools 执行，禁止再次解析或读取内部
	// Profile。Tool Schema 因此不随消息引用或 RunAccess 收窄而变化。
	resolved, err := r.toolCatalog.ResolveProfile(ctx, r.toolProfileID)
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("resolve conversation tool profile %q: %w", r.toolProfileID, err)
	}
	runContext, err := buildConversationRunContext(
		commandContext.Actor, request.UserMessage, resolved.ModelVisibleNames, r.sqlDataSourceID,
	)
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("build conversation run context: %w", err)
	}
	// 权威 v2 RunAccess 直接由固定 Profile + 当前消息引用派生并绑定；
	// 旧 TaskScope/WithTaskScope 双写已硬切删除。
	runCtx := agentruntime.WithRunAccess(ctx, runContext.Access())
	runCtx = resilience.WithRunIdentity(runCtx, resilience.RunIdentity{
		RunID: request.UserMessage.ID.String(), ConversationID: request.Conversation.ID.String(),
	})
	runCtx, agentSpan := observability.StartAgentRun(runCtx, "conversation")
	runCtx = observability.BindTraceIdentity(runCtx)
	traceID = observability.TraceID(runCtx)
	runID = request.UserMessage.ID.String()
	defer func() { observability.End(agentSpan, err) }()
	if r.memorySourceRecoveryEnabled {
		runCtx = conversationmemory.WithSourceRecoveryRun(runCtx)
	}
	if !r.contextPreflight.Enabled || r.contextPreflight.SummaryTailEnabled {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, r.timeout)
		defer cancel()
	}
	budget := newExecutionBudget(r.maxToolCalls, r.maxTotalTokens)
	runCtx = withExecutionBudget(runCtx, budget)
	runCtx = withConversationToolResultStore(
		runCtx, newConversationToolResultStore(r.maxToolCalls, r.maxToolResultBytes),
	)
	runCtx = withAgentToolRunPolicy(runCtx, newAgentToolRunPolicy(nil, map[string]int{
		ToolCreateDiagnosisTask:           1,
		ToolReadConversationMemorySources: r.memorySourceRecoveryMaxCalls,
	}))
	trace := &executionTrace{}
	toolTrace = trace
	runCtx = withExecutionTrace(runCtx, trace)
	citationTrace = &conversationCitationTrace{}
	runCtx = withConversationCitationTrace(runCtx, citationTrace)
	reportReferenceTrace = &conversationReportReferenceTrace{}
	runCtx = withConversationReportReferenceTrace(runCtx, reportReferenceTrace)
	usageTrace = &modelUsageTrace{onUsage: budget.recordUsage}
	observeFailure = true
	tools := resolved.Tools
	promptContext := runContext.PromptContext()
	var projection conversationPromptProjection
	if r.contextPreflight.SummaryTailEnabled {
		projection, promptManifest, err = r.prepareSummaryTailPrompt(runCtx, tools, request, promptContext)
	} else {
		projection, err = r.buildConversationPromptProjection(runCtx, request.History, request.UserMessage, promptContext)
		if err == nil {
			promptManifest, err = r.buildConversationPromptManifest(runCtx, tools, projection)
		}
	}
	messages := projection.messages
	if err == nil && len(messages) == 0 {
		return conversation.AgentResponse{}, conversation.ErrInvalidMessage
	}
	if err != nil {
		// Observation-only rollout remains fail-open. Once Continuous Tail is
		// active, the same preflight is a safety gate and must fail closed.
		r.log.Warn("conversation prompt preflight failed",
			zap.String("service_role", "conversation_agent"),
			zap.String("user_id", commandContext.Actor.UserID.String()),
			zap.String("conversation_id", request.Conversation.ID.String()),
			zap.String("message_id", request.UserMessage.ID.String()),
			zap.String("model_profile", r.contextPreflight.ModelProfile.Name),
			zap.Error(err),
		)
		if r.contextPreflight.ContinuousTailEnabled || r.contextPreflight.HardWindowEnforced {
			return conversation.AgentResponse{}, fmt.Errorf("%w: %w", ErrConversationContextPreparationFailed, err)
		}
	}
	if (r.contextPreflight.ContinuousTailEnabled || r.contextPreflight.HardWindowEnforced) && promptManifest != nil &&
		promptManifest.EstimateAvailable && promptManifest.ExceedsHardWindow {
		return conversation.AgentResponse{}, ErrConversationPromptWindowExceeded
	}
	if r.contextPreflight.Enabled && !r.contextPreflight.SummaryTailEnabled {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, r.timeout)
		defer cancel()
	}
	chatModel := r.chatModel
	if r.contextPreflight.ContinuousTailEnabled || r.contextPreflight.HardWindowEnforced {
		chatModel, runtimePromptObservation = newConversationWindowGuardModel(chatModel, r.contextPreflight)
	}
	agentInstance, err := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:        "mesguard-conversation",
		Description: "回答企业知识问题并将明确诊断请求转换为异步任务",
		Instruction: r.systemInstruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ExecuteSequentially: true,
			UnknownToolsHandler: rejectUnknownTool,
			ToolCallMiddlewares: []compose.ToolMiddleware{
				newToolObservabilityMiddleware(),
				newConversationToolTraceMiddleware(r.maxToolResultBytes),
			},
		}},
		MaxIterations: r.maxIterations,
	})
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("build per-turn conversation Agent: %w", err)
	}
	iterator := adk.NewRunner(runCtx, adk.RunnerConfig{
		Agent: agentInstance, EnableStreaming: r.enableStreaming,
	}).Run(
		runCtx,
		messages,
		adk.WithCallbacks(
			newModelUsageHandler(usageTrace),
			newModelTracingHandler(r.modelProvider, r.modelID),
		),
	)
	answer := ""
	for {
		event, more := iterator.Next()
		if !more {
			break
		}
		if event.Err != nil {
			promptManifest = runtimePromptObservation.apply(promptManifest)
			return conversation.AgentResponse{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		output := event.Output.MessageOutput
		message, messageErr := output.GetMessage()
		if messageErr != nil {
			return conversation.AgentResponse{}, fmt.Errorf("read conversation Agent event: %w", messageErr)
		}
		if message != nil && output.Role == schema.Assistant && len(message.ToolCalls) == 0 &&
			strings.TrimSpace(message.Content) != "" {
			answer = strings.TrimSpace(message.Content)
		}
	}
	if answer == "" {
		return conversation.AgentResponse{}, errors.New("conversation Agent returned no final answer")
	}
	citations, err := conversation.ResolveAnswerCitations(answer, citationTrace.snapshot())
	if err != nil {
		return conversation.AgentResponse{}, conversation.ErrAgentResponseInvalid
	}
	_, _, sourceToolAttempted, _ := citationTrace.observationSnapshot()
	if sourceToolAttempted && len(citations) == 0 && r.citationRepairer != nil {
		if evidence, repairable := citationTrace.repairSnapshot(); repairable {
			repaired, repairErr := r.citationRepairer.Repair(runCtx, ConversationCitationRepairRequest{
				UserQuery: request.UserMessage.Content, Draft: answer,
				Evidence: evidence, Sources: citationTrace.snapshot(),
			})
			usageTrace.appendUsage(repaired.Usage)
			if budget.exhausted() {
				return conversation.AgentResponse{}, ErrTokenBudgetExhausted
			}
			if repairErr == nil {
				repairedCitations, resolveErr := conversation.ResolveAnswerCitations(
					repaired.Answer, citationTrace.snapshot(),
				)
				if resolveErr == nil && len(repairedCitations) > 0 {
					answer, citations = repaired.Answer, repairedCitations
				}
			}
		}
	}
	_, degradedChannels, sourceToolAttempted, _ := citationTrace.observationSnapshot()
	outcome := conversation.AgentRunAnswered
	if len(degradedChannels) > 0 {
		outcome = conversation.AgentRunDegraded
	} else if sourceToolAttempted && len(citations) == 0 {
		outcome = conversation.AgentRunInsufficientEvidence
	}
	observation := r.buildRunObservation(startedAt, citationTrace, toolTrace, usageTrace, promptManifest, outcome)
	if observation.Validate() != nil {
		return conversation.AgentResponse{}, conversation.ErrAgentResponseInvalid
	}
	return conversation.AgentResponse{
		Content: answer, Citations: citations, ReportReferences: reportReferenceTrace.snapshot(),
		RunObservation: &observation,
	}, nil
}

func (r *ConversationRunner) buildRunObservation(
	startedAt time.Time,
	citationTrace *conversationCitationTrace,
	toolTrace *executionTrace,
	usageTrace *modelUsageTrace,
	promptManifest *contextgovernance.PromptManifest,
	outcome conversation.AgentRunOutcome,
) conversation.AgentRunObservation {
	var retrievedSources []conversation.AgentRunSource
	var degradedChannels []string
	var sourcesTruncated bool
	if citationTrace != nil {
		retrievedSources, degradedChannels, _, sourcesTruncated = citationTrace.observationSnapshot()
	}
	var usage ModelUsage
	if usageTrace != nil {
		usage = usageTrace.snapshot()
	}
	toolCalls := 0
	answerCacheEligible := false
	if toolTrace != nil {
		toolExecutions := toolTrace.snapshot()
		toolCalls = len(toolExecutions)
		answerCacheEligible = conversationAnswerCacheEligible(toolExecutions)
	}
	if minimumTotal := usage.PromptTokens + usage.CompletionTokens; usage.TotalTokens < minimumTotal {
		usage.TotalTokens = minimumTotal
	}
	durationMillis := time.Since(startedAt).Milliseconds()
	maxDurationMillis := int64((5 * time.Minute) / time.Millisecond)
	if durationMillis > maxDurationMillis {
		durationMillis = maxDurationMillis
	}
	if promptManifest != nil {
		manifest := *promptManifest
		if slices.Contains(manifest.DegradedReasons, "react_prompt_blocked") ||
			slices.Contains(manifest.DegradedReasons, "runtime_preflight_failed") {
			manifest.RunDurationMillis = durationMillis
		} else {
			initialUsage, available := usageTrace.initialSnapshot()
			manifest.FinalizeUsage(contextgovernance.PromptActualUsage{
				Available: available, PromptTokens: initialUsage.PromptTokens,
				CachedTokens: initialUsage.CachedTokens, CompletionTokens: initialUsage.CompletionTokens,
			}, durationMillis)
		}
		promptManifest = &manifest
	}
	return conversation.AgentRunObservation{
		ModelProvider: r.modelProvider, ModelID: r.modelID, PromptVersion: r.promptVersion,
		ExecutionPath: conversation.AgentRunExecutionAgent, ToolCalls: toolCalls,
		AnswerCacheEligible: answerCacheEligible && outcome == conversation.AgentRunAnswered,
		Outcome:             outcome, RetrievedSources: retrievedSources, DegradedChannels: degradedChannels,
		Usage: conversation.AgentRunUsage{
			ModelCalls: usage.ModelCalls, PromptTokens: usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
			CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens,
		},
		DurationMillis: durationMillis, SourcesTruncated: sourcesTruncated,
		PromptManifest: promptManifest,
	}
}

func conversationAnswerCacheEligible(executions []ToolExecution) bool {
	knowledgeSearchSucceeded := false
	for _, execution := range executions {
		if !execution.Succeeded || execution.Degraded {
			return false
		}
		switch execution.Name {
		case ToolSearchKnowledge:
			knowledgeSearchSucceeded = true
		case ToolSkill, ToolReadSkillReference, ToolReadConversationToolResult:
		default:
			return false
		}
	}
	return knowledgeSearchSucceeded
}

func conversationAgentErrorType(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "agent_timeout"
	case errors.Is(err, context.Canceled):
		return "agent_cancelled"
	case errors.Is(err, ErrAgentToolRunLimitExhausted):
		return "agent_tool_run_limit_exhausted"
	case errors.Is(err, ErrToolCallBudgetExhausted):
		return "tool_call_budget_exhausted"
	case errors.Is(err, ErrTokenBudgetExhausted):
		return "token_budget_exhausted"
	case errors.Is(err, ErrConversationPromptWindowExceeded):
		return "prompt_window_exceeded"
	case errors.Is(err, ErrConversationContextPreparationFailed):
		return conversation.AgentRunErrorTypeContextPreparationFailed
	case errors.Is(err, conversation.ErrAgentResponseInvalid):
		return "agent_response_invalid"
	default:
		return "agent_execution_failed"
	}
}

type conversationPromptProjection struct {
	messages                []*schema.Message
	selected                []conversation.Message
	currentMessageID        uuid.UUID
	currentUserContent      string
	tailFromSeq             int64
	tailThroughSeq          int64
	tailContinuous          bool
	summaryContent          string
	summaryFingerprint      string
	summarySnapshotID       string
	hardCompactionTriggered bool
	degradedReasons         []string
}

func (r *ConversationRunner) buildConversationPromptProjection(
	ctx context.Context,
	history []conversation.Message,
	current conversation.Message,
	promptContext conversationPromptContext,
) (conversationPromptProjection, error) {
	if r.contextPreflight.FullHistoryEnabled {
		return buildFullConversationPromptProjection(history, current, promptContext)
	}
	if !r.contextPreflight.ContinuousTailEnabled {
		return buildRuneConversationPromptProjection(history, current, r.maxContextRunes, promptContext), nil
	}
	tailBudget := int(math.Floor(float64(r.contextPreflight.ModelProfile.ContextWindowTokens) *
		r.contextPreflight.TailMaxRatio))
	return r.buildConversationPromptProjectionWithTailBudget(ctx, history, current, tailBudget, promptContext)
}

func (r *ConversationRunner) buildConversationPromptProjectionWithTailBudget(
	ctx context.Context,
	history []conversation.Message,
	current conversation.Message,
	tailBudget int,
	promptContext conversationPromptContext,
) (conversationPromptProjection, error) {
	if tailBudget < 1 {
		return conversationPromptProjection{}, ErrConversationContextPreparationFailed
	}
	candidates, err := continuousConversationCandidates(history, current)
	if err != nil {
		return conversationPromptProjection{}, err
	}
	tailMessages := make([]contextgovernance.TailMessage, 0, len(candidates))
	for _, item := range candidates {
		tailMessages = append(tailMessages, contextgovernance.TailMessage{
			Sequence: item.Seq, Content: conversationMessagePrompt(item, turnContextForMessage(item, promptContext)),
			Current: item.ID == current.ID,
		})
	}
	selectionCtx, cancel := context.WithTimeout(ctx, r.contextPreflight.effectiveTimeout())
	defer cancel()
	selection, err := r.contextPreflight.TailSelector.Select(selectionCtx, contextgovernance.ContinuousTailRequest{
		Profile: r.contextPreflight.ModelProfile.Name, MaxTokens: tailBudget, Messages: tailMessages,
	})
	if err != nil {
		return conversationPromptProjection{}, fmt.Errorf("select continuous conversation Tail: %w", err)
	}
	selectedDomain := candidates[len(candidates)-len(selection.Messages):]
	selected := make([]*schema.Message, 0, len(selectedDomain))
	for _, item := range selectedDomain {
		content := conversationMessagePrompt(item, turnContextForMessage(item, promptContext))
		if item.Role == conversation.MessageRoleUser {
			selected = append(selected, schema.UserMessage(content))
		} else {
			selected = append(selected, schema.AssistantMessage(content, nil))
		}
	}
	return conversationPromptProjection{
		messages: selected, selected: selectedDomain, currentMessageID: current.ID,
		currentUserContent: conversationCurrentUserContent(selectedDomain, current, promptContext),
		tailFromSeq:        selectedDomain[0].Seq, tailThroughSeq: selectedDomain[len(selectedDomain)-1].Seq,
		tailContinuous: true,
	}, nil
}

// turnContextForMessage gives every user message one stable representation.
// Persisted references are rendered from that message, while the SQL data
// source is a deployment-level fact. This model-visible block never grants
// authority; the current RunAccess remains the only execution authority.
func turnContextForMessage(
	item conversation.Message,
	promptContext conversationPromptContext,
) string {
	if item.Role == conversation.MessageRoleUser {
		return renderConversationTurnContext(
			item, promptContext.sqlDataSourceID, promptContext.sqlAuthorized,
		)
	}
	return ""
}

// conversationCurrentUserContent 返回当前 user message 渲染后的完整内容
// （含尾部 turn_context），供 Token 预算与 PromptManifest 统计追加内容。
func conversationCurrentUserContent(
	selected []conversation.Message,
	current conversation.Message,
	promptContext conversationPromptContext,
) string {
	for _, item := range selected {
		if item.ID == current.ID {
			return strings.TrimSpace(conversationMessagePrompt(item, turnContextForMessage(item, promptContext)))
		}
	}
	return ""
}

func continuousConversationCandidates(
	history []conversation.Message,
	current conversation.Message,
) ([]conversation.Message, error) {
	ordered := conversationHistoryThroughCurrent(history, current)
	candidates := make([]conversation.Message, 0, len(ordered))
	nextSequence := current.Seq + 1
	for index := len(ordered) - 1; index >= 0; index-- {
		item := ordered[index]
		if item.Seq != nextSequence-1 ||
			(item.Role != conversation.MessageRoleUser && item.Role != conversation.MessageRoleAssistant) ||
			conversationMessagePrompt(item, "") == "" {
			break
		}
		candidates = append(candidates, item)
		nextSequence = item.Seq
	}
	slices.Reverse(candidates)
	if len(candidates) == 0 || candidates[len(candidates)-1].ID != current.ID {
		return nil, conversation.ErrInvalidMessage
	}
	return candidates, nil
}

func buildRuneConversationPromptProjection(
	history []conversation.Message,
	current conversation.Message,
	maxRunes int,
	promptContext conversationPromptContext,
) conversationPromptProjection {
	ordered := conversationHistoryThroughCurrent(history, current)
	selected := make([]*schema.Message, 0, len(ordered))
	selectedDomain := make([]conversation.Message, 0, len(ordered))
	usedRunes := 0
	for index := len(ordered) - 1; index >= 0; index-- {
		item := ordered[index]
		content := conversationMessagePrompt(item, turnContextForMessage(item, promptContext))
		if content == "" {
			continue
		}
		contentRunes := len([]rune(content))
		if len(selected) > 0 && usedRunes+contentRunes > maxRunes {
			continue
		}
		var message *schema.Message
		switch item.Role {
		case conversation.MessageRoleUser:
			message = schema.UserMessage(content)
		case conversation.MessageRoleAssistant:
			message = schema.AssistantMessage(content, nil)
		default:
			continue
		}
		selected = append(selected, message)
		selectedDomain = append(selectedDomain, item)
		usedRunes += contentRunes
	}
	slices.Reverse(selected)
	slices.Reverse(selectedDomain)
	projection := conversationPromptProjection{
		messages: selected, selected: selectedDomain, currentMessageID: current.ID,
		currentUserContent: conversationCurrentUserContent(selectedDomain, current, promptContext),
		tailContinuous:     true,
	}
	if len(selectedDomain) > 0 {
		projection.tailFromSeq = selectedDomain[0].Seq
		projection.tailThroughSeq = selectedDomain[len(selectedDomain)-1].Seq
		for index := 1; index < len(selectedDomain); index++ {
			if selectedDomain[index].Seq != selectedDomain[index-1].Seq+1 {
				projection.tailContinuous = false
				break
			}
		}
	}
	return projection
}

func conversationHistoryThroughCurrent(history []conversation.Message, current conversation.Message) []conversation.Message {
	ordered := make([]conversation.Message, 0, len(history)+1)
	for _, item := range history {
		if item.ID == current.ID || item.Seq > current.Seq {
			continue
		}
		ordered = append(ordered, item)
	}
	ordered = append(ordered, current)
	slices.SortFunc(ordered, func(left, right conversation.Message) int {
		switch {
		case left.Seq < right.Seq:
			return -1
		case left.Seq > right.Seq:
			return 1
		default:
			return 0
		}
	})
	return ordered
}

// conversationMessagePrompt 渲染一条消息的模型可见正文。结构化上下文一律
// 追加在正文尾部（用户原文 + 换行 + 上下文块），绝不放在原文前面：
//   - user messages carry a stable turn_context built from their own persisted
//     references plus deployment-level SQL context;
//   - non-user historical messages keep their persisted message_references.
func conversationMessagePrompt(message conversation.Message, turnContext string) string {
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	if turnContext != "" {
		return content + "\n" + turnContext
	}
	references := conversationMessageReferencePrompt(message)
	if references == "" {
		return content
	}
	return content + "\n" + references
}

// conversationMessageReferencePrompt 渲染历史消息各自已持久化引用的安全 JSON
// 投影（<message_references> 块），委托共享渲染器（conversation_context_projection.go）；
// 永不携带本轮数据源授权。
func conversationMessageReferencePrompt(message conversation.Message) string {
	return renderConversationMessageReferences(message)
}

func newConversationToolTraceMiddleware(maxResultBytes int) compose.ToolMiddleware {
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
			if trace := conversationCitationTraceFromContext(ctx); trace != nil {
				snapshot := ""
				if output != nil {
					snapshot = output.Result
				}
				trace.observeTool(input.Name, snapshot, err)
			}
			if err == nil && output != nil && len(output.Result) <= maxResultBytes {
				if sources, ok := conversationCitationSourcesFromTool(input.Name, output.Result); ok {
					trace := conversationCitationTraceFromContext(ctx)
					if augmented, augmentedOK := augmentConversationToolResultWithCitationSources(
						output.Result, sources, maxResultBytes,
					); augmentedOK && trace.append(sources) {
						output.Result = augmented
						trace.appendRepairEvidence(augmented)
					} else {
						trace.markDegraded("citation_sources_omitted")
					}
				} else if conversationCitationProducingTool(input.Name) {
					conversationCitationTraceFromContext(ctx).markDegraded("citation_source_validation_failed")
				}
			}
			if output != nil && len(output.Result) > maxResultBytes {
				bounded, boundedErr := boundedConversationToolResult(ctx, output.Result, maxResultBytes)
				if boundedErr != nil {
					err = boundedErr
					output = nil
				} else {
					output.Result = bounded
				}
			}
			entry := ToolExecution{
				Name: input.Name, DurationMS: time.Since(startedAt).Milliseconds(), Succeeded: err == nil,
			}
			if err != nil {
				entry.Error = "tool execution failed"
			}
			traceFromContext(ctx).append(entry, "", nil)
			return output, err
		}
	}}
}

func boundedConversationToolResult(
	ctx context.Context,
	result string,
	maxResultBytes int,
) (string, error) {
	if len(result) <= maxResultBytes {
		return result, nil
	}
	store := conversationToolResultStoreFromContext(ctx)
	if store == nil {
		return "", errors.New("conversation Tool result store is unavailable")
	}
	ref, err := store.put(result)
	if err != nil {
		return "", err
	}
	suffix := fmt.Sprintf(
		"\n[tool result truncated by MESGuard; ref=%s; original_bytes=%d; "+
			"read with %s using this ref and offsetBytes]",
		ref, len(result), ToolReadConversationToolResult,
	)
	previewBytes := maxResultBytes - len(suffix)
	if previewBytes < 1 {
		return strings.ToValidUTF8(suffix[len(suffix)-maxResultBytes:], "?"), nil
	}
	return strings.ToValidUTF8(result[:previewBytes], "?") + suffix, nil
}

var _ conversation.AgentResponder = (*ConversationRunner)(nil)
