package contextgovernance

import (
	"context"
	"testing"
)

type fixedTokenEstimator struct {
	estimate TokenEstimate
}

type exactTokenCounterStub struct {
	got PromptInput
}

func (s *exactTokenCounterStub) CountTokens(_ context.Context, input PromptInput) (int, error) {
	s.got = input
	return 42, nil
}

func (f fixedTokenEstimator) Estimate(context.Context, PromptInput) (TokenEstimate, error) {
	return f.estimate, nil
}

func TestTokenBudgetPlannerReportsAvailableInputAndThresholds(t *testing.T) {
	planner, err := NewTokenBudgetPlanner(fixedTokenEstimator{estimate: TokenEstimate{
		EstimatedTokens:  700,
		UpperBoundTokens: 850,
		Method:           EstimationMethodLocalCalibrated,
		Profile:          "chat-main",
	}})
	if err != nil {
		t.Fatalf("NewTokenBudgetPlanner(): %v", err)
	}

	plan, err := planner.Plan(context.Background(), TokenBudgetRequest{
		ContextWindowTokens: 2000,
		MaxOutputTokens:     200,
		SafetyMarginTokens:  200,
		SoftThresholdRatio:  0.50,
		HardThresholdRatio:  0.75,
		Prompt: PromptInput{Profile: "chat-main", Segments: []PromptSegment{{
			Kind: PromptSegmentHistory, Content: "bounded history",
		}}},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if plan.AvailableInputTokens != 1600 || plan.EstimatedPromptTokens != 700 ||
		plan.EstimatedUpperBoundTokens != 850 {
		t.Fatalf("plan capacity/estimate = %+v", plan)
	}
	if !plan.SoftThresholdReached || plan.HardThresholdReached || plan.ExceedsHardWindow {
		t.Fatalf("plan thresholds = %+v", plan)
	}
}

func TestTokenBudgetPlannerKeepsHardWindowIndependentFromCache(t *testing.T) {
	planner, err := NewTokenBudgetPlanner(fixedTokenEstimator{estimate: TokenEstimate{
		EstimatedTokens:  1500,
		UpperBoundTokens: 1700,
		Method:           EstimationMethodConservativeHeuristic,
		Profile:          "chat-main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(context.Background(), TokenBudgetRequest{
		ContextWindowTokens: 2000,
		MaxOutputTokens:     200,
		SafetyMarginTokens:  200,
		SoftThresholdRatio:  0.50,
		HardThresholdRatio:  0.75,
		Prompt:              PromptInput{Profile: "chat-main"},
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if !plan.SoftThresholdReached || !plan.HardThresholdReached || !plan.ExceedsHardWindow {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestLocalTokenEstimatorFallsBackWithoutRemoteTokenizer(t *testing.T) {
	estimator, err := NewLocalTokenEstimator(EstimationMethodLocalExact, nil)
	if err != nil {
		t.Fatalf("NewLocalTokenEstimator(): %v", err)
	}
	estimate, err := estimator.Estimate(context.Background(), PromptInput{
		Profile: "chat-main",
		Segments: []PromptSegment{
			{Kind: PromptSegmentSystem, Content: "You are a safe assistant."},
			{Kind: PromptSegmentCurrentUser, Content: "检查连接池超时"},
			{Kind: PromptSegmentToolGrowthReserve, ReservedTokens: 100},
		},
	})
	if err != nil {
		t.Fatalf("Estimate(): %v", err)
	}
	if estimate.EstimatedTokens < 1 || estimate.ReservedTokens != 100 ||
		estimate.UpperBoundTokens < estimate.EstimatedTokens+100 {
		t.Fatalf("estimate = %+v", estimate)
	}
	if estimate.Method != EstimationMethodLocalCalibrated ||
		len(estimate.DegradedReasons) != 1 || estimate.DegradedReasons[0] != "exact_tokenizer_unavailable" {
		t.Fatalf("fallback = %+v", estimate)
	}
}

func TestLocalExactEstimatorSeparatesVisiblePromptFromGrowthReserve(t *testing.T) {
	counter := &exactTokenCounterStub{}
	estimator, err := NewLocalTokenEstimator(EstimationMethodLocalExact, counter)
	if err != nil {
		t.Fatal(err)
	}
	estimate, err := estimator.Estimate(context.Background(), PromptInput{
		Profile: "chat-main",
		Segments: []PromptSegment{
			{Kind: PromptSegmentSystem, Content: "visible"},
			{Kind: PromptSegmentToolGrowthReserve, ReservedTokens: 100},
		},
	})
	if err != nil {
		t.Fatalf("Estimate(): %v", err)
	}
	if estimate.EstimatedTokens != 42 || estimate.UpperBoundTokens != 142 || estimate.ReservedTokens != 100 ||
		len(counter.got.Segments) != 1 || counter.got.Segments[0].Content != "visible" {
		t.Fatalf("estimate=%+v exact input=%+v", estimate, counter.got)
	}
}
