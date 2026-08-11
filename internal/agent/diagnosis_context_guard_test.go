package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type diagnosisGuardPlanner struct {
	mu       sync.Mutex
	plans    []contextgovernance.TokenBudgetPlan
	requests []contextgovernance.TokenBudgetRequest
}

func (p *diagnosisGuardPlanner) Plan(
	_ context.Context,
	request contextgovernance.TokenBudgetRequest,
) (contextgovernance.TokenBudgetPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if len(p.plans) == 0 {
		return contextgovernance.TokenBudgetPlan{}, errors.New("unexpected preflight call")
	}
	plan := p.plans[0]
	p.plans = p.plans[1:]
	return plan, nil
}

func (p *diagnosisGuardPlanner) requestSnapshot() []contextgovernance.TokenBudgetRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]contextgovernance.TokenBudgetRequest(nil), p.requests...)
}

type diagnosisGuardModel struct {
	mu    sync.Mutex
	calls int
	tools []*schema.ToolInfo
}

func (m *diagnosisGuardModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &diagnosisGuardBoundModel{root: m, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *diagnosisGuardModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return schema.AssistantMessage("ok", nil), nil
}

func (m *diagnosisGuardModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *diagnosisGuardModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type diagnosisGuardBoundModel struct {
	root  *diagnosisGuardModel
	tools []*schema.ToolInfo
}

func (m *diagnosisGuardBoundModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &diagnosisGuardBoundModel{root: m.root, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *diagnosisGuardBoundModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	return m.root.Generate(ctx, input, opts...)
}

func (m *diagnosisGuardBoundModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.root.Stream(ctx, input, opts...)
}

func TestDiagnosisContextGuardPreflightsEveryCallAndBlocksBeforeProvider(t *testing.T) {
	planner := &diagnosisGuardPlanner{plans: []contextgovernance.TokenBudgetPlan{
		{
			AvailableInputTokens: 176, EstimatedPromptTokens: 80, EstimatedUpperBoundTokens: 100,
			ReservedTokens: 16, EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
		},
		{
			AvailableInputTokens: 176, EstimatedPromptTokens: 170, EstimatedUpperBoundTokens: 190,
			ReservedTokens: 16, ExceedsHardWindow: true,
			EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated,
		},
	}}
	inner := &diagnosisGuardModel{}
	guarded, observation := newDiagnosisContextGuardModel(inner, diagnosisContextPreflightForTest(planner), diagnosisPromptSeed{
		SystemInstruction: "system and entry skill",
		CaseSnapshot:      `{"id":"case-1","title":"failure"}`,
	})
	bound, err := guarded.WithTools([]*schema.ToolInfo{{
		Name: "read_case", Desc: "read case",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	input := []*schema.Message{schema.SystemMessage("system and entry skill"), schema.UserMessage("diagnose")}
	if _, err = bound.Generate(context.Background(), input); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	input = append(input, schema.ToolMessage(`{"evidenceRef":"evidence:1"}`, "call-1"))
	if _, err = bound.Generate(context.Background(), input); !errors.Is(err, ErrDiagnosisPromptWindowExceeded) {
		t.Fatalf("second Generate error = %v", err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	got := observation.snapshot(0)
	if got.PreflightCalls != 2 || got.HardWindowBlockedCount != 1 ||
		got.HighWaterTokens != 190 || got.LastEstimatedUpperBoundTokens != 190 {
		t.Fatalf("observation = %+v", got)
	}
}

func TestDiagnosisContextGuardBudgetsCaseToolsEvidenceAndReportOutput(t *testing.T) {
	planner := &diagnosisGuardPlanner{plans: []contextgovernance.TokenBudgetPlan{
		{AvailableInputTokens: 176, EstimatedUpperBoundTokens: 80, ReservedTokens: 16,
			EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated},
		{AvailableInputTokens: 176, EstimatedUpperBoundTokens: 120, ReservedTokens: 16,
			EstimationMethod: contextgovernance.EstimationMethodLocalCalibrated},
	}}
	inner := &diagnosisGuardModel{}
	guarded, _ := newDiagnosisContextGuardModel(inner, diagnosisContextPreflightForTest(planner), diagnosisPromptSeed{
		SystemInstruction: "system", PreloadedSkill: "ticket diagnosis skill",
		CaseSnapshot: `{"id":"case-1"}`,
	})
	bound, err := guarded.WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	trace := &executionTrace{}
	ctx := withExecutionTrace(context.Background(), trace)
	if _, err = bound.Generate(ctx, []*schema.Message{schema.UserMessage("diagnose")}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	trace.append(ToolExecution{Name: ToolReadExternalCase, Succeeded: true}, "", &EvidenceItem{
		ID: "evidence:1", SourceRef: "evidence:1", SourceTool: ToolReadExternalCase,
		SourceType: EvidenceSourceCaseSnapshot, Location: "tool-output",
	})
	if _, err = bound.Generate(ctx, []*schema.Message{
		schema.UserMessage("diagnose"), schema.ToolMessage(`{"evidenceRef":"evidence:1"}`, "call-1"),
	}); err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	requests := planner.requestSnapshot()
	if len(requests) != 2 {
		t.Fatalf("preflight requests = %d, want 2", len(requests))
	}
	if requests[0].MaxOutputTokens != 64 || requests[0].SafetyMarginTokens != 16 {
		t.Fatalf("report reserve contract = %+v", requests[0])
	}
	firstKinds := diagnosisPromptSegmentKinds(requests[0].Prompt.Segments)
	for _, kind := range []contextgovernance.PromptSegmentKind{
		contextgovernance.PromptSegmentSystem,
		contextgovernance.PromptSegmentPreloadedSkill,
		contextgovernance.PromptSegmentCaseSnapshot,
		contextgovernance.PromptSegmentToolSchema,
		contextgovernance.PromptSegmentToolGrowthReserve,
	} {
		if !slices.Contains(firstKinds, kind) {
			t.Fatalf("initial prompt kinds = %v, missing %q", firstKinds, kind)
		}
	}
	secondKinds := diagnosisPromptSegmentKinds(requests[1].Prompt.Segments)
	if slices.Contains(secondKinds, contextgovernance.PromptSegmentCaseSnapshot) ||
		!slices.Contains(secondKinds, contextgovernance.PromptSegmentToolResult) ||
		!slices.Contains(secondKinds, contextgovernance.PromptSegmentEvidenceIndex) {
		t.Fatalf("second prompt kinds = %v", secondKinds)
	}
}

func diagnosisContextPreflightForTest(planner contextgovernance.TokenBudgetPlanner) DiagnosisContextPreflightConfig {
	return DiagnosisContextPreflightConfig{
		Enabled: true, Planner: planner,
		ModelProfile: contextgovernance.ModelProfile{
			Name: "test-main", Provider: "test", ModelID: "test-model",
			ContextWindowTokens: 256, MaxOutputTokens: 64, SafetyMarginTokens: 16,
		},
		SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: 16,
	}
}

func diagnosisPromptSegmentKinds(segments []contextgovernance.PromptSegment) []contextgovernance.PromptSegmentKind {
	result := make([]contextgovernance.PromptSegmentKind, 0, len(segments))
	for _, segment := range segments {
		result = append(result, segment.Kind)
	}
	return result
}
