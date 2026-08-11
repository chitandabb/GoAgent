package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"

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

type ConversationRunnerConfig struct {
	ChatModel             model.ToolCallingChatModel
	CitationRepairer      ConversationCitationRepairer
	ToolCatalog           *ToolCatalog
	SystemInstruction     string
	ModelProvider         string
	ModelID               string
	PromptVersion         string
	AvailableDependencies []ToolDependency
	Logger                *zap.Logger
	MaxIterations         int
	MaxToolCalls          int
	MaxTotalTokens        int
	MaxContextRunes       int
	Timeout               time.Duration
	MaxToolResultBytes    int
	ContextPreflight      ConversationContextPreflightConfig
}

// ConversationRunner executes one lightweight workbench turn. It is separate
// from the diagnosis Runner so chat responses are not forced through the report
// schema or Evidence Gate and cannot synchronously perform a full diagnosis.
type ConversationRunner struct {
	chatModel             model.ToolCallingChatModel
	citationRepairer      ConversationCitationRepairer
	toolCatalog           *ToolCatalog
	systemInstruction     string
	modelProvider         string
	modelID               string
	promptVersion         string
	availableDependencies []ToolDependency
	log                   *zap.Logger
	maxIterations         int
	maxToolCalls          int
	maxTotalTokens        int
	maxContextRunes       int
	timeout               time.Duration
	maxToolResultBytes    int
	contextPreflight      ConversationContextPreflightConfig
}

func NewConversationRunner(cfg ConversationRunnerConfig) (*ConversationRunner, error) {
	if cfg.ChatModel == nil || cfg.ToolCatalog == nil || cfg.Logger == nil {
		return nil, errors.New("conversation runner model, catalog, and logger are required")
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
	dependencies := append([]ToolDependency(nil), cfg.AvailableDependencies...)
	for _, dependency := range dependencies {
		if !dependency.Valid() {
			return nil, fmt.Errorf("conversation runner dependency %q is invalid", dependency)
		}
	}
	if hasDuplicate(dependencies) {
		return nil, errors.New("conversation runner dependencies contain duplicates")
	}
	if err := cfg.ContextPreflight.validate(cfg.ModelProvider, cfg.ModelID); err != nil {
		return nil, err
	}
	return &ConversationRunner{
		chatModel: cfg.ChatModel, citationRepairer: cfg.CitationRepairer, toolCatalog: cfg.ToolCatalog,
		systemInstruction: cfg.SystemInstruction, modelProvider: cfg.ModelProvider,
		modelID: cfg.ModelID, promptVersion: cfg.PromptVersion, availableDependencies: dependencies,
		log: cfg.Logger, maxIterations: cfg.MaxIterations, maxToolCalls: cfg.MaxToolCalls,
		maxTotalTokens: cfg.MaxTotalTokens, maxContextRunes: cfg.MaxContextRunes,
		timeout: cfg.Timeout, maxToolResultBytes: cfg.MaxToolResultBytes,
		contextPreflight: cfg.ContextPreflight,
	}, nil
}

func (r *ConversationRunner) Respond(ctx context.Context, request conversation.AgentRequest) (result conversation.AgentResponse, err error) {
	if r == nil {
		return conversation.AgentResponse{}, conversation.ErrAgentUnavailable
	}
	startedAt := time.Now()
	var citationTrace *conversationCitationTrace
	var usageTrace *modelUsageTrace
	var promptManifest *contextgovernance.PromptManifest
	observeFailure := false
	defer func() {
		if err != nil && observeFailure {
			if _, alreadyWrapped := conversation.AgentRunFailureRecordFrom(err); !alreadyWrapped {
				observation := r.buildRunObservation(
					startedAt, citationTrace, usageTrace, promptManifest, conversation.AgentRunFailed,
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
		fields := []zap.Field{zap.Duration("duration", time.Since(startedAt))}
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
	scope, err := r.conversationScope(commandContext.Actor, request.UserMessage)
	if err != nil {
		return conversation.AgentResponse{}, err
	}
	runCtx := WithTaskScope(ctx, scope)
	if !r.contextPreflight.Enabled {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, r.timeout)
		defer cancel()
	}
	budget := newExecutionBudget(r.maxToolCalls, r.maxTotalTokens)
	runCtx = withExecutionBudget(runCtx, budget)
	runCtx = withAgentToolRunPolicy(runCtx, newAgentToolRunPolicy(nil, map[string]int{
		ToolCreateDiagnosisTask: 1,
	}))
	trace := &executionTrace{}
	runCtx = withExecutionTrace(runCtx, trace)
	citationTrace = &conversationCitationTrace{}
	runCtx = withConversationCitationTrace(runCtx, citationTrace)
	usageTrace = &modelUsageTrace{onUsage: budget.recordUsage}
	observeFailure = true
	tools, err := r.toolCatalog.ToolsFor(runCtx, scope)
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("resolve conversation tools: %w", err)
	}
	projection := buildConversationPromptProjection(request.History, request.UserMessage, r.maxContextRunes)
	messages := projection.messages
	if len(messages) == 0 {
		return conversation.AgentResponse{}, conversation.ErrInvalidMessage
	}
	promptManifest, err = r.shadowPromptManifest(runCtx, tools, projection)
	if err != nil {
		// Shadow observation must never make a previously valid model request fail.
		r.log.Warn("conversation shadow prompt preflight failed",
			zap.String("service_role", "conversation_agent"),
			zap.String("user_id", commandContext.Actor.UserID.String()),
			zap.String("conversation_id", request.Conversation.ID.String()),
			zap.String("message_id", request.UserMessage.ID.String()),
			zap.String("model_profile", r.contextPreflight.ModelProfile.Name),
			zap.Error(err),
		)
	}
	if r.contextPreflight.Enabled {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, r.timeout)
		defer cancel()
	}
	agentInstance, err := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:        "mesguard-conversation",
		Description: "回答企业知识问题并将明确诊断请求转换为异步任务",
		Instruction: r.systemInstruction,
		Model:       r.chatModel,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ExecuteSequentially: true,
			UnknownToolsHandler: rejectUnknownTool,
			ToolCallMiddlewares: []compose.ToolMiddleware{newConversationToolTraceMiddleware(r.maxToolResultBytes)},
		}},
		MaxIterations: r.maxIterations,
	})
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("build per-turn conversation Agent: %w", err)
	}
	iterator := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: agentInstance}).Run(
		runCtx,
		messages,
		adk.WithCallbacks(newModelUsageHandler(usageTrace)),
	)
	answer := ""
	for {
		event, more := iterator.Next()
		if !more {
			break
		}
		if event.Err != nil {
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
	observation := r.buildRunObservation(startedAt, citationTrace, usageTrace, promptManifest, outcome)
	if observation.Validate() != nil {
		return conversation.AgentResponse{}, conversation.ErrAgentResponseInvalid
	}
	return conversation.AgentResponse{Content: answer, Citations: citations, RunObservation: &observation}, nil
}

func (r *ConversationRunner) buildRunObservation(
	startedAt time.Time,
	citationTrace *conversationCitationTrace,
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
		initialUsage, available := usageTrace.initialSnapshot()
		manifest.FinalizeUsage(contextgovernance.PromptActualUsage{
			Available: available, PromptTokens: initialUsage.PromptTokens,
			CachedTokens: initialUsage.CachedTokens, CompletionTokens: initialUsage.CompletionTokens,
		}, durationMillis)
		promptManifest = &manifest
	}
	return conversation.AgentRunObservation{
		ModelProvider: r.modelProvider, ModelID: r.modelID, PromptVersion: r.promptVersion,
		Outcome: outcome, RetrievedSources: retrievedSources, DegradedChannels: degradedChannels,
		Usage: conversation.AgentRunUsage{
			ModelCalls: usage.ModelCalls, PromptTokens: usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
			CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens,
		},
		DurationMillis: durationMillis, SourcesTruncated: sourcesTruncated,
		PromptManifest: promptManifest,
	}
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
	case errors.Is(err, conversation.ErrAgentResponseInvalid):
		return "agent_response_invalid"
	default:
		return "agent_execution_failed"
	}
}

func (r *ConversationRunner) conversationScope(actor conversation.Actor, message conversation.Message) (TaskScope, error) {
	role := auth.RoleAnalyst
	if actor.IsAdmin {
		role = auth.RoleAdmin
	}
	capabilities := []ToolCapability{ToolCapabilityKnowledge, ToolCapabilityWebSearch}
	selected := 0
	for _, reference := range message.CaseReferences {
		if reference.Kind == conversation.ReferenceKindSelected {
			selected++
		}
	}
	if selected == 1 {
		capabilities = append(capabilities, ToolCapabilityCase)
	}
	if len(message.TaskReferences) > 0 {
		capabilities = append(capabilities, ToolCapabilityTask)
	}
	if len(message.Attachments) > 0 {
		capabilities = append(capabilities, ToolCapabilityAttachment)
	}
	return NewTaskScope(TaskScopeConfig{
		UserID: actor.UserID, Role: role, TaskType: TaskTypeConversation,
		AllowedCapabilities: capabilities, AvailableDependencies: r.availableDependencies,
	})
}

type conversationPromptProjection struct {
	messages         []*schema.Message
	selected         []conversation.Message
	currentMessageID uuid.UUID
	tailFromSeq      int64
	tailThroughSeq   int64
	tailContinuous   bool
}

func buildConversationPromptProjection(
	history []conversation.Message,
	current conversation.Message,
	maxRunes int,
) conversationPromptProjection {
	ordered := append([]conversation.Message(nil), history...)
	if len(ordered) == 0 || ordered[len(ordered)-1].ID != current.ID {
		ordered = append(ordered, current)
	}
	selected := make([]*schema.Message, 0, len(ordered))
	selectedDomain := make([]conversation.Message, 0, len(ordered))
	usedRunes := 0
	for index := len(ordered) - 1; index >= 0; index-- {
		item := ordered[index]
		content := conversationMessagePrompt(item)
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
		tailContinuous: true,
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

func conversationMessagePrompt(message conversation.Message) string {
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	references := conversationMessageReferencePrompt(message)
	if references == "" {
		return content
	}
	return references + content
}

func conversationMessageReferencePrompt(message conversation.Message) string {
	if len(message.CaseReferences) == 0 && len(message.TaskReferences) == 0 && len(message.Attachments) == 0 {
		return ""
	}
	var contextBlock strings.Builder
	contextBlock.WriteString("<message_references>\n")
	for _, reference := range message.CaseReferences {
		fmt.Fprintf(&contextBlock, "case id=%s kind=%s\n", reference.ExternalCaseID, reference.Kind)
	}
	for _, reference := range message.TaskReferences {
		fmt.Fprintf(&contextBlock, "task id=%s kind=%s\n", reference.TaskID, reference.Kind)
	}
	for _, reference := range message.Attachments {
		fmt.Fprintf(
			&contextBlock,
			"attachment id=%s name=%q media_type=%s purpose=%s size_bytes=%d status=%s\n",
			reference.AttachmentID, reference.OriginalName, reference.MediaType,
			reference.Purpose, reference.SizeBytes, reference.Status,
		)
	}
	contextBlock.WriteString("</message_references>\n")
	return contextBlock.String()
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
				output.Result = strings.ToValidUTF8(output.Result[:maxResultBytes], "?") +
					"\n[tool result truncated by MESGuard]"
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

var _ conversation.AgentResponder = (*ConversationRunner)(nil)
