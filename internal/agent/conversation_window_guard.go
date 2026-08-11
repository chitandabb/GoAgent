package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// conversationWindowGuardModel is a per-turn safety wrapper around the
// external model boundary. The Runner has already checked and manifested the
// first call, so this wrapper skips that call and guards every subsequent ReAct
// request after Tool results have been appended.
type conversationWindowGuardModel struct {
	inner       model.ToolCallingChatModel
	preflight   ConversationContextPreflightConfig
	boundTools  []*schema.ToolInfo
	state       *conversationWindowGuardState
	observation *conversationRuntimePromptObservation
}

type conversationWindowGuardState struct {
	skipInitial atomic.Uint32
}

type conversationRuntimePromptObservation struct {
	mu             sync.Mutex
	blockedPlan    *contextgovernance.TokenBudgetPlan
	failureStage   string
	durationMicros int64
}

func newConversationWindowGuardModel(
	inner model.ToolCallingChatModel,
	preflight ConversationContextPreflightConfig,
) (model.ToolCallingChatModel, *conversationRuntimePromptObservation) {
	state := &conversationWindowGuardState{}
	state.skipInitial.Store(1)
	observation := &conversationRuntimePromptObservation{}
	return &conversationWindowGuardModel{
		inner: inner, preflight: preflight, state: state, observation: observation,
	}, observation
}

func (m *conversationWindowGuardModel) WithTools(
	tools []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	if m == nil || m.inner == nil {
		return nil, errors.New("conversation window guard model is unavailable")
	}
	bound, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &conversationWindowGuardModel{
		inner: bound, preflight: m.preflight,
		boundTools: append([]*schema.ToolInfo(nil), tools...), state: m.state,
		observation: m.observation,
	}, nil
}

func (m *conversationWindowGuardModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	if err := m.guard(ctx, input, opts...); err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, input, opts...)
}

func (m *conversationWindowGuardModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.guard(ctx, input, opts...); err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, input, opts...)
}

func (m *conversationWindowGuardModel) guard(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) error {
	if m == nil || m.inner == nil || m.state == nil {
		return errors.New("conversation window guard model is unavailable")
	}
	if m.state.skipInitial.CompareAndSwap(1, 0) {
		return nil
	}
	startedAt := time.Now()
	prompt, err := encodeConversationRuntimePrompt(input)
	if err != nil {
		m.observation.recordFailure("runtime_prompt_encode_failed", time.Since(startedAt))
		return fmt.Errorf("%w: encode runtime prompt: %v", ErrConversationContextPreparationFailed, err)
	}
	tools, err := conversationRuntimeToolContract(m.boundTools, opts...)
	if err != nil {
		m.observation.recordFailure("runtime_tool_schema_failed", time.Since(startedAt))
		return fmt.Errorf("%w: encode runtime Tool contract: %v", ErrConversationContextPreparationFailed, err)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, m.preflight.effectiveTimeout())
	defer cancel()
	plan, err := m.preflight.plan(preflightCtx, []contextgovernance.PromptSegment{
		{Kind: contextgovernance.PromptSegmentToolSchema, Content: tools},
		{Kind: contextgovernance.PromptSegmentHistory, Content: prompt},
		{
			Kind:           contextgovernance.PromptSegmentToolGrowthReserve,
			ReservedTokens: m.preflight.ToolGrowthReserveTokens,
		},
	})
	if err != nil {
		m.observation.recordFailure("runtime_token_estimation_failed", time.Since(startedAt))
		return fmt.Errorf("%w: runtime Token preflight: %v", ErrConversationContextPreparationFailed, err)
	}
	if plan.ExceedsHardWindow {
		m.observation.recordBlocked(plan, time.Since(startedAt))
		return ErrConversationPromptWindowExceeded
	}
	return nil
}

func (o *conversationRuntimePromptObservation) recordBlocked(
	plan contextgovernance.TokenBudgetPlan,
	duration time.Duration,
) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	copyPlan := plan
	o.blockedPlan = &copyPlan
	o.failureStage = ""
	o.durationMicros = duration.Microseconds()
}

func (o *conversationRuntimePromptObservation) recordFailure(stage string, duration time.Duration) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.blockedPlan = nil
	o.failureStage = stage
	o.durationMicros = duration.Microseconds()
}

func (o *conversationRuntimePromptObservation) apply(
	base *contextgovernance.PromptManifest,
) *contextgovernance.PromptManifest {
	if o == nil || base == nil {
		return base
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.blockedPlan == nil && o.failureStage == "" {
		return base
	}
	manifest := *base
	manifest.ActualUsageAvailable = false
	manifest.ActualPromptTokens = 0
	manifest.CacheHitTokens = 0
	manifest.CacheMissTokens = 0
	manifest.CompletionTokens = 0
	manifest.EstimationErrorRatio = 0
	manifest.PreflightDurationMicros = o.durationMicros
	manifest.ContextDegraded = true
	if o.blockedPlan != nil {
		plan := *o.blockedPlan
		manifest.PreflightStatus = contextgovernance.PreflightStatusSucceeded
		manifest.FailureStage = ""
		manifest.EstimateAvailable = true
		manifest.AvailableInputTokens = plan.AvailableInputTokens
		manifest.EstimatedPromptTokens = plan.EstimatedPromptTokens
		manifest.EstimatedUpperBoundTokens = plan.EstimatedUpperBoundTokens
		manifest.ToolGrowthReserveTokens = plan.ReservedTokens
		manifest.EstimationMethod = plan.EstimationMethod
		manifest.SoftThresholdReached = plan.SoftThresholdReached
		manifest.HardThresholdReached = plan.HardThresholdReached
		manifest.ExceedsHardWindow = plan.ExceedsHardWindow
		manifest.DegradedReasons = mergeConversationManifestReasons(
			manifest.DegradedReasons, plan.EstimatorDegradedReasons, "react_prompt_blocked",
		)
	} else {
		manifest.PreflightStatus = contextgovernance.PreflightStatusFailed
		manifest.FailureStage = o.failureStage
		manifest.EstimateAvailable = false
		manifest.EstimatedPromptTokens = 0
		manifest.EstimatedUpperBoundTokens = 0
		manifest.EstimationMethod = ""
		manifest.SoftThresholdReached = false
		manifest.HardThresholdReached = false
		manifest.ExceedsHardWindow = false
		manifest.DegradedReasons = mergeConversationManifestReasons(
			manifest.DegradedReasons, nil, "runtime_preflight_failed", o.failureStage,
		)
	}
	if manifest.Validate() != nil {
		return base
	}
	return &manifest
}

func mergeConversationManifestReasons(existing, additional []string, extras ...string) []string {
	merged := make([]string, 0, 16)
	for _, values := range [][]string{extras, existing, additional} {
		for _, value := range values {
			if value != "" && !slices.Contains(merged, value) && len(merged) < 16 {
				merged = append(merged, value)
			}
		}
	}
	return merged
}

type conversationRuntimeMessage struct {
	Role                    schema.RoleType            `json:"role"`
	Content                 string                     `json:"content,omitempty"`
	MultiContent            []schema.ChatMessagePart   `json:"multiContent,omitempty"`
	UserInputMultiContent   []schema.MessageInputPart  `json:"userInputMultiContent,omitempty"`
	AssistantMultiContent   []schema.MessageOutputPart `json:"assistantMultiContent,omitempty"`
	Name                    string                     `json:"name,omitempty"`
	ToolCalls               []schema.ToolCall          `json:"toolCalls,omitempty"`
	ToolCallID              string                     `json:"toolCallId,omitempty"`
	ToolName                string                     `json:"toolName,omitempty"`
	ProviderReasoningPrompt string                     `json:"providerReasoningPrompt,omitempty"`
}

func encodeConversationRuntimePrompt(input []*schema.Message) (string, error) {
	if len(input) == 0 {
		return "", errors.New("runtime prompt is empty")
	}
	projection := make([]conversationRuntimeMessage, 0, len(input))
	for _, message := range input {
		if message == nil {
			return "", errors.New("runtime prompt contains a nil message")
		}
		projection = append(projection, conversationRuntimeMessage{
			Role: message.Role, Content: message.Content,
			MultiContent: message.MultiContent, UserInputMultiContent: message.UserInputMultiContent,
			AssistantMultiContent: message.AssistantGenMultiContent, Name: message.Name,
			ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID, ToolName: message.ToolName,
			ProviderReasoningPrompt: message.ReasoningContent,
		})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func conversationRuntimeToolContract(
	bound []*schema.ToolInfo,
	opts ...model.Option,
) (string, error) {
	common := model.GetCommonOptions(nil, opts...)
	active := common.Tools
	if active == nil {
		active = bound
	}
	activeContract, err := canonicalConversationToolInfoContract(active)
	if err != nil {
		return "", err
	}
	deferredContract, err := canonicalConversationToolInfoContract(common.DeferredTools)
	if err != nil {
		return "", err
	}
	var searchContract contextgovernance.CanonicalToolContract
	if common.ToolSearchTool != nil {
		searchContract, err = canonicalConversationToolInfoContract([]*schema.ToolInfo{common.ToolSearchTool})
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(struct {
		Active     json.RawMessage `json:"active"`
		Deferred   json.RawMessage `json:"deferred,omitempty"`
		ToolSearch json.RawMessage `json:"toolSearch,omitempty"`
	}{
		Active:     json.RawMessage(activeContract.ModelVisibleJSON),
		Deferred:   json.RawMessage(deferredContract.ModelVisibleJSON),
		ToolSearch: json.RawMessage(searchContract.ModelVisibleJSON),
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
