package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
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
	ToolCatalog           *ToolCatalog
	SystemInstruction     string
	AvailableDependencies []ToolDependency
	Logger                *zap.Logger
	MaxIterations         int
	MaxToolCalls          int
	MaxTotalTokens        int
	MaxContextRunes       int
	Timeout               time.Duration
	MaxToolResultBytes    int
}

// ConversationRunner executes one lightweight workbench turn. It is separate
// from the diagnosis Runner so chat responses are not forced through the report
// schema or Evidence Gate and cannot synchronously perform a full diagnosis.
type ConversationRunner struct {
	chatModel             model.ToolCallingChatModel
	toolCatalog           *ToolCatalog
	systemInstruction     string
	availableDependencies []ToolDependency
	log                   *zap.Logger
	maxIterations         int
	maxToolCalls          int
	maxTotalTokens        int
	maxContextRunes       int
	timeout               time.Duration
	maxToolResultBytes    int
}

func NewConversationRunner(cfg ConversationRunnerConfig) (*ConversationRunner, error) {
	if cfg.ChatModel == nil || cfg.ToolCatalog == nil || cfg.Logger == nil {
		return nil, errors.New("conversation runner model, catalog, and logger are required")
	}
	cfg.SystemInstruction = strings.TrimSpace(cfg.SystemInstruction)
	if cfg.SystemInstruction == "" {
		return nil, errors.New("conversation runner system instruction is required")
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
	return &ConversationRunner{
		chatModel: cfg.ChatModel, toolCatalog: cfg.ToolCatalog,
		systemInstruction: cfg.SystemInstruction, availableDependencies: dependencies,
		log: cfg.Logger, maxIterations: cfg.MaxIterations, maxToolCalls: cfg.MaxToolCalls,
		maxTotalTokens: cfg.MaxTotalTokens, maxContextRunes: cfg.MaxContextRunes,
		timeout: cfg.Timeout, maxToolResultBytes: cfg.MaxToolResultBytes,
	}, nil
}

func (r *ConversationRunner) Respond(ctx context.Context, request conversation.AgentRequest) (result conversation.AgentResponse, err error) {
	if r == nil {
		return conversation.AgentResponse{}, conversation.ErrAgentUnavailable
	}
	startedAt := time.Now()
	defer func() {
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
	runCtx, cancel := context.WithTimeout(WithTaskScope(ctx, scope), r.timeout)
	defer cancel()
	budget := newExecutionBudget(r.maxToolCalls, r.maxTotalTokens)
	runCtx = withExecutionBudget(runCtx, budget)
	runCtx = withAgentToolRunPolicy(runCtx, newAgentToolRunPolicy(nil, map[string]int{
		ToolCreateDiagnosisTask: 1,
	}))
	trace := &executionTrace{}
	runCtx = withExecutionTrace(runCtx, trace)
	usageTrace := &modelUsageTrace{onUsage: budget.recordUsage}
	tools, err := r.toolCatalog.ToolsFor(runCtx, scope)
	if err != nil {
		return conversation.AgentResponse{}, fmt.Errorf("resolve conversation tools: %w", err)
	}
	messages := conversationModelMessages(request.History, request.UserMessage, r.maxContextRunes)
	if len(messages) == 0 {
		return conversation.AgentResponse{}, conversation.ErrInvalidMessage
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
	return conversation.AgentResponse{Content: answer}, nil
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
	return NewTaskScope(TaskScopeConfig{
		UserID: actor.UserID, Role: role, TaskType: TaskTypeConversation,
		AllowedCapabilities: capabilities, AvailableDependencies: r.availableDependencies,
	})
}

func conversationModelMessages(
	history []conversation.Message,
	current conversation.Message,
	maxRunes int,
) []*schema.Message {
	ordered := append([]conversation.Message(nil), history...)
	if len(ordered) == 0 || ordered[len(ordered)-1].ID != current.ID {
		ordered = append(ordered, current)
	}
	selected := make([]*schema.Message, 0, len(ordered))
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
		usedRunes += contentRunes
	}
	slices.Reverse(selected)
	return selected
}

func conversationMessagePrompt(message conversation.Message) string {
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	if len(message.CaseReferences) == 0 && len(message.TaskReferences) == 0 {
		return content
	}
	var contextBlock strings.Builder
	contextBlock.WriteString("<message_references>\n")
	for _, reference := range message.CaseReferences {
		fmt.Fprintf(&contextBlock, "case id=%s kind=%s\n", reference.ExternalCaseID, reference.Kind)
	}
	for _, reference := range message.TaskReferences {
		fmt.Fprintf(&contextBlock, "task id=%s kind=%s\n", reference.TaskID, reference.Kind)
	}
	contextBlock.WriteString("</message_references>\n")
	contextBlock.WriteString(content)
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
