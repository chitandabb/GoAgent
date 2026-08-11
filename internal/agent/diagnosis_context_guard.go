package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const defaultDiagnosisPreflightTimeout = 250 * time.Millisecond

var (
	ErrDiagnosisContextPreparationFailed = errors.New("diagnosis context preparation failed")
	ErrDiagnosisPromptWindowExceeded     = errors.New("diagnosis prompt exceeds the model context window")
)

// DiagnosisContextPreflightConfig shares the provider-neutral planner and
// model window contract with Conversation without enabling conversation memory.
type DiagnosisContextPreflightConfig struct {
	Enabled                 bool
	Planner                 contextgovernance.TokenBudgetPlanner
	ModelProfile            contextgovernance.ModelProfile
	SoftThresholdRatio      float64
	HardThresholdRatio      float64
	ToolGrowthReserveTokens int
	PreflightTimeout        time.Duration
}

func (c DiagnosisContextPreflightConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	available := c.ModelProfile.ContextWindowTokens - c.ModelProfile.MaxOutputTokens -
		c.ModelProfile.SafetyMarginTokens
	if c.Planner == nil || c.ModelProfile.Validate() != nil ||
		math.IsNaN(c.SoftThresholdRatio) || math.IsInf(c.SoftThresholdRatio, 0) ||
		math.IsNaN(c.HardThresholdRatio) || math.IsInf(c.HardThresholdRatio, 0) ||
		c.SoftThresholdRatio <= 0 || c.HardThresholdRatio <= c.SoftThresholdRatio ||
		c.HardThresholdRatio >= 1 || c.ToolGrowthReserveTokens < 1 ||
		c.ToolGrowthReserveTokens >= available ||
		(c.PreflightTimeout != 0 && (c.PreflightTimeout < 5*time.Millisecond || c.PreflightTimeout > 5*time.Second)) {
		return errors.New("diagnosis context preflight configuration is invalid")
	}
	return nil
}

func (c DiagnosisContextPreflightConfig) effectiveTimeout() time.Duration {
	if c.PreflightTimeout > 0 {
		return c.PreflightTimeout
	}
	return defaultDiagnosisPreflightTimeout
}

func (c DiagnosisContextPreflightConfig) plan(
	ctx context.Context,
	segments []contextgovernance.PromptSegment,
) (contextgovernance.TokenBudgetPlan, error) {
	profile := c.ModelProfile
	return c.Planner.Plan(ctx, contextgovernance.TokenBudgetRequest{
		ContextWindowTokens: profile.ContextWindowTokens,
		MaxOutputTokens:     profile.MaxOutputTokens,
		SafetyMarginTokens:  profile.SafetyMarginTokens,
		SoftThresholdRatio:  c.SoftThresholdRatio,
		HardThresholdRatio:  c.HardThresholdRatio,
		Prompt: contextgovernance.PromptInput{
			Profile: profile.Name, Segments: segments,
		},
	})
}

// DiagnosisContextObservation is the bounded, report-safe runtime view of
// preflight decisions. HighWaterTokens is the largest conservative upper bound.
type DiagnosisContextObservation struct {
	PreflightCalls                int                                `json:"preflightCalls"`
	PreflightFailureCount         int                                `json:"preflightFailureCount"`
	HighWaterTokens               int                                `json:"highWaterTokens"`
	AvailableInputTokens          int                                `json:"availableInputTokens"`
	HighWaterRatio                float64                            `json:"highWaterRatio"`
	ToolResultTruncatedCount      int                                `json:"toolResultTruncatedCount"`
	HardWindowBlockedCount        int                                `json:"hardWindowBlockedCount"`
	LastEstimatedUpperBoundTokens int                                `json:"lastEstimatedUpperBoundTokens"`
	ReportOutputReserveTokens     int                                `json:"reportOutputReserveTokens"`
	ToolGrowthReserveTokens       int                                `json:"toolGrowthReserveTokens"`
	EstimationMethod              contextgovernance.EstimationMethod `json:"estimationMethod,omitempty"`
}

type diagnosisContextObservationRecorder struct {
	mu    sync.Mutex
	value DiagnosisContextObservation
}

func newDiagnosisContextObservation(
	config DiagnosisContextPreflightConfig,
) *diagnosisContextObservationRecorder {
	return &diagnosisContextObservationRecorder{value: DiagnosisContextObservation{
		ReportOutputReserveTokens: config.ModelProfile.MaxOutputTokens,
		ToolGrowthReserveTokens:   config.ToolGrowthReserveTokens,
	}}
}

func (o *diagnosisContextObservationRecorder) recordPlan(plan contextgovernance.TokenBudgetPlan) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.value.PreflightCalls++
	o.value.AvailableInputTokens = plan.AvailableInputTokens
	o.value.LastEstimatedUpperBoundTokens = plan.EstimatedUpperBoundTokens
	o.value.EstimationMethod = plan.EstimationMethod
	if plan.ExceedsHardWindow {
		o.value.HardWindowBlockedCount++
	}
	if plan.EstimatedUpperBoundTokens >= o.value.HighWaterTokens {
		o.value.HighWaterTokens = plan.EstimatedUpperBoundTokens
		if plan.AvailableInputTokens > 0 {
			o.value.HighWaterRatio = float64(plan.EstimatedUpperBoundTokens) /
				float64(plan.AvailableInputTokens)
		}
	}
}

func (o *diagnosisContextObservationRecorder) recordFailure() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.value.PreflightCalls++
	o.value.PreflightFailureCount++
	o.mu.Unlock()
}

func (o *diagnosisContextObservationRecorder) snapshot(
	toolResultTruncatedCount int,
) DiagnosisContextObservation {
	if o == nil {
		return DiagnosisContextObservation{ToolResultTruncatedCount: toolResultTruncatedCount}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	result := o.value
	result.ToolResultTruncatedCount = toolResultTruncatedCount
	return result
}

type diagnosisPromptSeed struct {
	SystemInstruction string
	PreloadedSkill    string
	CaseSnapshot      string
}

type diagnosisContextGuardState struct {
	calls atomic.Uint32
}

type diagnosisContextGuardModel struct {
	inner       model.ToolCallingChatModel
	preflight   DiagnosisContextPreflightConfig
	seed        diagnosisPromptSeed
	boundTools  []*schema.ToolInfo
	state       *diagnosisContextGuardState
	observation *diagnosisContextObservationRecorder
}

func newDiagnosisContextGuardModel(
	inner model.ToolCallingChatModel,
	preflight DiagnosisContextPreflightConfig,
	seed diagnosisPromptSeed,
) (model.ToolCallingChatModel, *diagnosisContextObservationRecorder) {
	observation := newDiagnosisContextObservation(preflight)
	return &diagnosisContextGuardModel{
		inner: inner, preflight: preflight, seed: seed,
		state: &diagnosisContextGuardState{}, observation: observation,
	}, observation
}

func (m *diagnosisContextGuardModel) WithTools(
	tools []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	if m == nil || m.inner == nil {
		return nil, errors.New("diagnosis context guard model is unavailable")
	}
	bound, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &diagnosisContextGuardModel{
		inner: bound, preflight: m.preflight, seed: m.seed,
		boundTools: append([]*schema.ToolInfo(nil), tools...), state: m.state,
		observation: m.observation,
	}, nil
}

func (m *diagnosisContextGuardModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	if err := m.guard(ctx, input, opts...); err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, input, opts...)
}

func (m *diagnosisContextGuardModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.guard(ctx, input, opts...); err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, input, opts...)
}

func (m *diagnosisContextGuardModel) guard(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) error {
	if m == nil || m.inner == nil || m.state == nil || m.observation == nil {
		return errors.New("diagnosis context guard model is unavailable")
	}
	call := m.state.calls.Add(1)
	tools, err := conversationRuntimeToolContract(m.boundTools, opts...)
	if err != nil {
		m.observation.recordFailure()
		return fmt.Errorf("%w: encode Tool contract: %v", ErrDiagnosisContextPreparationFailed, err)
	}
	history, toolResults, err := encodeDiagnosisRuntimeMessages(input)
	if err != nil {
		m.observation.recordFailure()
		return fmt.Errorf("%w: encode runtime prompt: %v", ErrDiagnosisContextPreparationFailed, err)
	}
	segments := make([]contextgovernance.PromptSegment, 0, 8)
	appendDiagnosisPromptSegment(&segments, contextgovernance.PromptSegmentSystem, m.seed.SystemInstruction)
	appendDiagnosisPromptSegment(&segments, contextgovernance.PromptSegmentPreloadedSkill, m.seed.PreloadedSkill)
	segments = append(segments, contextgovernance.PromptSegment{
		Kind: contextgovernance.PromptSegmentToolSchema, Content: tools,
	})
	if call == 1 {
		appendDiagnosisPromptSegment(&segments, contextgovernance.PromptSegmentCaseSnapshot, m.seed.CaseSnapshot)
	}
	appendDiagnosisPromptSegment(&segments, contextgovernance.PromptSegmentHistory, history)
	appendDiagnosisPromptSegment(&segments, contextgovernance.PromptSegmentToolResult, toolResults)
	if trace := traceFromContext(ctx); trace != nil {
		appendDiagnosisPromptSegment(
			&segments, contextgovernance.PromptSegmentEvidenceIndex, trace.evidenceIndexPrompt(),
		)
	}
	segments = append(segments, contextgovernance.PromptSegment{
		Kind:           contextgovernance.PromptSegmentToolGrowthReserve,
		ReservedTokens: m.preflight.ToolGrowthReserveTokens,
	})
	preflightCtx, cancel := context.WithTimeout(ctx, m.preflight.effectiveTimeout())
	defer cancel()
	plan, err := m.preflight.plan(preflightCtx, segments)
	if err != nil {
		m.observation.recordFailure()
		return fmt.Errorf("%w: Token preflight: %v", ErrDiagnosisContextPreparationFailed, err)
	}
	m.observation.recordPlan(plan)
	if plan.ExceedsHardWindow {
		return ErrDiagnosisPromptWindowExceeded
	}
	return nil
}

func appendDiagnosisPromptSegment(
	segments *[]contextgovernance.PromptSegment,
	kind contextgovernance.PromptSegmentKind,
	content string,
) {
	if content == "" {
		return
	}
	*segments = append(*segments, contextgovernance.PromptSegment{Kind: kind, Content: content})
}

func encodeDiagnosisRuntimeMessages(input []*schema.Message) (history, toolResults string, err error) {
	if len(input) == 0 {
		return "", "", errors.New("runtime prompt is empty")
	}
	historyMessages := make([]*schema.Message, 0, len(input))
	toolMessages := make([]*schema.Message, 0, len(input))
	for _, message := range input {
		if message == nil {
			return "", "", errors.New("runtime prompt contains a nil message")
		}
		switch message.Role {
		case schema.System:
			// The exact Runner instruction is budgeted as a stable seed.
		case schema.Tool:
			toolMessages = append(toolMessages, message)
		default:
			historyMessages = append(historyMessages, message)
		}
	}
	if len(historyMessages) > 0 {
		history, err = encodeConversationRuntimePrompt(historyMessages)
		if err != nil {
			return "", "", err
		}
	}
	if len(toolMessages) > 0 {
		toolResults, err = encodeConversationRuntimePrompt(toolMessages)
		if err != nil {
			return "", "", err
		}
	}
	return history, toolResults, nil
}

func (t *executionTrace) evidenceIndexPrompt() string {
	items := t.evidenceSnapshot()
	if len(items) == 0 {
		return ""
	}
	type evidenceIndexItem struct {
		EvidenceRef string             `json:"evidenceRef"`
		SourceType  EvidenceSourceType `json:"sourceType"`
		SourceTool  string             `json:"sourceTool"`
		Location    string             `json:"location,omitempty"`
		Truncated   bool               `json:"truncated"`
	}
	index := make([]evidenceIndexItem, 0, len(items))
	for _, item := range items {
		index = append(index, evidenceIndexItem{
			EvidenceRef: item.SourceRef, SourceType: item.SourceType,
			SourceTool: item.SourceTool, Location: item.Location, Truncated: item.Truncated,
		})
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return ""
	}
	return string(encoded)
}
