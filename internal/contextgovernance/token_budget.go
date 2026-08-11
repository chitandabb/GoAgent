// Package contextgovernance owns provider-independent prompt budgeting and
// bounded prompt metadata. It deliberately has no database, model, or Tool
// authorization dependencies.
package contextgovernance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

type EstimationMethod string

const (
	EstimationMethodLocalExact            EstimationMethod = "local_exact"
	EstimationMethodLocalCalibrated       EstimationMethod = "local_calibrated"
	EstimationMethodConservativeHeuristic EstimationMethod = "conservative_heuristic"
)

func (m EstimationMethod) Valid() bool {
	switch EstimationMethod(strings.ToLower(strings.TrimSpace(string(m)))) {
	case EstimationMethodLocalExact, EstimationMethodLocalCalibrated,
		EstimationMethodConservativeHeuristic:
		return true
	default:
		return false
	}
}

type PromptSegmentKind string

const (
	PromptSegmentSystem            PromptSegmentKind = "system_prompt"
	PromptSegmentToolSchema        PromptSegmentKind = "tool_schema"
	PromptSegmentPreloadedSkill    PromptSegmentKind = "preloaded_skill"
	PromptSegmentSummary           PromptSegmentKind = "summary"
	PromptSegmentHistory           PromptSegmentKind = "history"
	PromptSegmentDynamicReferences PromptSegmentKind = "dynamic_references"
	PromptSegmentCurrentUser       PromptSegmentKind = "current_user_message"
	PromptSegmentCaseSnapshot      PromptSegmentKind = "case_snapshot"
	PromptSegmentEvidenceIndex     PromptSegmentKind = "evidence_index"
	PromptSegmentToolResult        PromptSegmentKind = "tool_result"
	PromptSegmentToolGrowthReserve PromptSegmentKind = "tool_growth_reserve"
)

func (k PromptSegmentKind) Valid() bool {
	switch k {
	case PromptSegmentSystem, PromptSegmentToolSchema, PromptSegmentPreloadedSkill,
		PromptSegmentSummary, PromptSegmentHistory, PromptSegmentDynamicReferences,
		PromptSegmentCurrentUser, PromptSegmentCaseSnapshot, PromptSegmentEvidenceIndex,
		PromptSegmentToolResult, PromptSegmentToolGrowthReserve:
		return true
	default:
		return false
	}
}

type PromptSegment struct {
	Kind           PromptSegmentKind
	Content        string
	ReservedTokens int
}

type PromptInput struct {
	Profile  string
	Segments []PromptSegment
}

func (p PromptInput) validate() error {
	if strings.TrimSpace(p.Profile) == "" || len(p.Profile) > 128 || len(p.Segments) > 64 {
		return errors.New("prompt profile and at most 64 segments are required")
	}
	for _, segment := range p.Segments {
		if !segment.Kind.Valid() || segment.ReservedTokens < 0 ||
			len(segment.Content) > 8*1024*1024 ||
			(segment.ReservedTokens > 0 && strings.TrimSpace(segment.Content) != "") {
			return errors.New("prompt segment is invalid")
		}
	}
	return nil
}

type TokenEstimate struct {
	EstimatedTokens  int
	UpperBoundTokens int
	ReservedTokens   int
	Method           EstimationMethod
	Profile          string
	DegradedReasons  []string
}

func (e TokenEstimate) validate() error {
	if e.EstimatedTokens < 0 || e.ReservedTokens < 0 ||
		e.UpperBoundTokens < e.EstimatedTokens+e.ReservedTokens ||
		!e.Method.Valid() || strings.TrimSpace(e.Profile) == "" || len(e.DegradedReasons) > 16 {
		return errors.New("token estimate is invalid")
	}
	for _, reason := range e.DegradedReasons {
		if !validMachineLabel(reason, 64) {
			return errors.New("token estimate degraded reason is invalid")
		}
	}
	return nil
}

type TokenEstimator interface {
	Estimate(ctx context.Context, input PromptInput) (TokenEstimate, error)
}

type ExactTokenCounter interface {
	CountTokens(ctx context.Context, input PromptInput) (int, error)
}

type localTokenEstimator struct {
	preferred EstimationMethod
	exact     ExactTokenCounter
}

func NewLocalTokenEstimator(preferred EstimationMethod, exact ExactTokenCounter) (TokenEstimator, error) {
	preferred = EstimationMethod(strings.ToLower(strings.TrimSpace(string(preferred))))
	if !preferred.Valid() {
		return nil, errors.New("local token estimator strategy is invalid")
	}
	return &localTokenEstimator{preferred: preferred, exact: exact}, nil
}

func (e *localTokenEstimator) Estimate(ctx context.Context, input PromptInput) (TokenEstimate, error) {
	if e == nil {
		return TokenEstimate{}, errors.New("local token estimator is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return TokenEstimate{}, err
	}
	if err := input.validate(); err != nil {
		return TokenEstimate{}, err
	}
	if e.preferred == EstimationMethodLocalExact && e.exact != nil {
		reservedTokens := 0
		visibleInput := PromptInput{Profile: input.Profile}
		for _, segment := range input.Segments {
			if segment.ReservedTokens > 0 {
				reservedTokens += segment.ReservedTokens
				continue
			}
			visibleInput.Segments = append(visibleInput.Segments, segment)
		}
		tokens, err := e.exact.CountTokens(ctx, visibleInput)
		if err != nil {
			if ctx.Err() != nil {
				return TokenEstimate{}, ctx.Err()
			}
			return e.estimateHeuristically(input, "exact_tokenizer_failed"), nil
		}
		if tokens < 0 {
			return TokenEstimate{}, errors.New("exact tokenizer returned a negative count")
		}
		return TokenEstimate{
			EstimatedTokens: tokens, UpperBoundTokens: tokens + reservedTokens,
			ReservedTokens: reservedTokens,
			Method:         EstimationMethodLocalExact, Profile: strings.TrimSpace(input.Profile),
		}, nil
	}
	if e.preferred == EstimationMethodLocalExact {
		return e.estimateHeuristically(input, "exact_tokenizer_unavailable"), nil
	}
	return e.estimateHeuristically(input, ""), nil
}

func (e *localTokenEstimator) estimateHeuristically(input PromptInput, degradedReason string) TokenEstimate {
	baseTokens, reservedTokens := 0, 0
	for _, segment := range input.Segments {
		if segment.ReservedTokens > 0 {
			reservedTokens += segment.ReservedTokens
			continue
		}
		baseTokens += heuristicContentTokens(segment.Content) + 4
	}
	method := e.preferred
	if method == EstimationMethodLocalExact {
		method = EstimationMethodLocalCalibrated
	}
	estimatedFactor, upperFactor := 1.08, 1.22
	if method == EstimationMethodConservativeHeuristic {
		estimatedFactor, upperFactor = 1.20, 1.45
	}
	estimate := TokenEstimate{
		EstimatedTokens:  int(math.Ceil(float64(baseTokens) * estimatedFactor)),
		UpperBoundTokens: int(math.Ceil(float64(baseTokens)*upperFactor)) + reservedTokens,
		ReservedTokens:   reservedTokens,
		Method:           method,
		Profile:          strings.TrimSpace(input.Profile),
	}
	if degradedReason != "" {
		estimate.DegradedReasons = []string{degradedReason}
	}
	return estimate
}

// heuristicContentTokens is deliberately local and conservative. CJK runes
// count as one token, other non-ASCII runes as half a token, and ASCII bytes as
// a quarter token before calibration compensation is applied.
func heuristicContentTokens(content string) int {
	quarterUnits := 0
	for _, current := range content {
		switch {
		case current <= unicode.MaxASCII:
			quarterUnits++
		case unicode.Is(unicode.Han, current) || unicode.Is(unicode.Hiragana, current) ||
			unicode.Is(unicode.Katakana, current) || unicode.Is(unicode.Hangul, current):
			quarterUnits += 4
		default:
			quarterUnits += 2
		}
	}
	if !utf8.ValidString(content) {
		quarterUnits += len(content)
	}
	return (quarterUnits + 3) / 4
}

type TokenBudgetRequest struct {
	ContextWindowTokens int
	MaxOutputTokens     int
	SafetyMarginTokens  int
	SoftThresholdRatio  float64
	HardThresholdRatio  float64
	Prompt              PromptInput
}

// ModelProfile is the provider-neutral window contract consumed by every
// caller of TokenBudgetPlanner. Provider credentials and endpoint details do
// not belong in prompt governance.
type ModelProfile struct {
	Name                string
	Provider            string
	ModelID             string
	ContextWindowTokens int
	MaxOutputTokens     int
	SafetyMarginTokens  int
}

func (p ModelProfile) Validate() error {
	if !validMachineLabel(p.Name, 128) || !validMachineLabel(p.Provider, 64) ||
		!validLabel(p.ModelID, 256) || p.ContextWindowTokens < 1 ||
		p.MaxOutputTokens < 0 || p.SafetyMarginTokens < 0 ||
		p.MaxOutputTokens+p.SafetyMarginTokens >= p.ContextWindowTokens {
		return errors.New("context-governance model profile is invalid")
	}
	return nil
}

type TokenBudgetPlan struct {
	AvailableInputTokens      int
	EstimatedPromptTokens     int
	EstimatedUpperBoundTokens int
	ReservedTokens            int
	SoftThresholdReached      bool
	HardThresholdReached      bool
	ExceedsHardWindow         bool
	EstimationMethod          EstimationMethod
	EstimatorDegradedReasons  []string
}

type TokenBudgetPlanner interface {
	Plan(ctx context.Context, request TokenBudgetRequest) (TokenBudgetPlan, error)
}

type tokenBudgetPlanner struct {
	estimator TokenEstimator
}

func NewTokenBudgetPlanner(estimator TokenEstimator) (TokenBudgetPlanner, error) {
	if estimator == nil {
		return nil, errors.New("token estimator is required")
	}
	return &tokenBudgetPlanner{estimator: estimator}, nil
}

func (p *tokenBudgetPlanner) Plan(ctx context.Context, request TokenBudgetRequest) (TokenBudgetPlan, error) {
	if p == nil || p.estimator == nil {
		return TokenBudgetPlan{}, errors.New("token budget planner is unavailable")
	}
	if request.ContextWindowTokens < 1 || request.MaxOutputTokens < 0 || request.SafetyMarginTokens < 0 ||
		request.MaxOutputTokens+request.SafetyMarginTokens >= request.ContextWindowTokens ||
		math.IsNaN(request.SoftThresholdRatio) || math.IsNaN(request.HardThresholdRatio) ||
		request.SoftThresholdRatio <= 0 || request.HardThresholdRatio <= request.SoftThresholdRatio ||
		request.HardThresholdRatio >= 1 {
		return TokenBudgetPlan{}, errors.New("token budget request is invalid")
	}
	estimate, err := p.estimator.Estimate(ctx, request.Prompt)
	if err != nil {
		return TokenBudgetPlan{}, fmt.Errorf("estimate prompt tokens: %w", err)
	}
	if err := estimate.validate(); err != nil {
		return TokenBudgetPlan{}, err
	}
	available := request.ContextWindowTokens - request.MaxOutputTokens - request.SafetyMarginTokens
	upper := estimate.UpperBoundTokens
	return TokenBudgetPlan{
		AvailableInputTokens:      available,
		EstimatedPromptTokens:     estimate.EstimatedTokens,
		EstimatedUpperBoundTokens: upper,
		ReservedTokens:            estimate.ReservedTokens,
		SoftThresholdReached:      float64(upper) >= float64(available)*request.SoftThresholdRatio,
		HardThresholdReached:      float64(upper) >= float64(available)*request.HardThresholdRatio,
		ExceedsHardWindow:         upper > available,
		EstimationMethod:          estimate.Method,
		EstimatorDegradedReasons:  append([]string(nil), estimate.DegradedReasons...),
	}, nil
}
