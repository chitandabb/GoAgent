package contextgovernance

import (
	"context"
	"errors"
	"math"
	"strings"
)

const (
	maxContinuousTailMessages = 10_000
	maxContinuousTailBytes    = 8 * 1024 * 1024
	MaxTailWindowRatio        = 0.20
)

func ValidTailWindowRatio(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= MaxTailWindowRatio
}

// TailMessage is the provider-neutral, model-visible projection of one
// Conversation message. Content already includes bounded reference metadata.
type TailMessage struct {
	Sequence int64
	Content  string
	Current  bool
}

type ContinuousTailRequest struct {
	Profile   string
	MaxTokens int
	Messages  []TailMessage
}

type ContinuousTailSelection struct {
	Messages                    []TailMessage
	EstimatedTokens             int
	EstimatedUpperBoundTokens   int
	CurrentMessageExceedsBudget bool
	EstimationMethod            EstimationMethod
	EstimatorDegradedReasons    []string
}

type ContinuousTailSelector struct {
	estimator TokenEstimator
}

func NewContinuousTailSelector(estimator TokenEstimator) (*ContinuousTailSelector, error) {
	if estimator == nil {
		return nil, errors.New("continuous Tail TokenEstimator is required")
	}
	return &ContinuousTailSelector{estimator: estimator}, nil
}

func (s *ContinuousTailSelector) Select(
	ctx context.Context,
	request ContinuousTailRequest,
) (ContinuousTailSelection, error) {
	if s == nil || s.estimator == nil {
		return ContinuousTailSelection{}, errors.New("continuous Tail selector is unavailable")
	}
	if err := validateContinuousTailRequest(request); err != nil {
		return ContinuousTailSelection{}, err
	}
	last := len(request.Messages) - 1
	contiguousFrom := last
	for contiguousFrom > 0 && request.Messages[contiguousFrom-1].Sequence+1 == request.Messages[contiguousFrom].Sequence {
		contiguousFrom--
	}
	selectedFrom := last
	estimate, err := s.estimate(ctx, request.Profile, request.Messages[last:])
	if err != nil {
		return ContinuousTailSelection{}, err
	}
	currentExceedsBudget := estimate.UpperBoundTokens > request.MaxTokens
	low, high := contiguousFrom, last
	for low <= high {
		index := low + (high-low)/2
		candidate, estimateErr := s.estimate(ctx, request.Profile, request.Messages[index:])
		if estimateErr != nil {
			return ContinuousTailSelection{}, estimateErr
		}
		if candidate.UpperBoundTokens <= request.MaxTokens {
			selectedFrom, estimate = index, candidate
			high = index - 1
		} else {
			low = index + 1
		}
	}
	return ContinuousTailSelection{
		Messages:                    append([]TailMessage(nil), request.Messages[selectedFrom:]...),
		EstimatedTokens:             estimate.EstimatedTokens,
		EstimatedUpperBoundTokens:   estimate.UpperBoundTokens,
		CurrentMessageExceedsBudget: currentExceedsBudget,
		EstimationMethod:            estimate.Method,
		EstimatorDegradedReasons:    append([]string(nil), estimate.DegradedReasons...),
	}, nil
}

func (s *ContinuousTailSelector) estimate(
	ctx context.Context,
	profile string,
	messages []TailMessage,
) (TokenEstimate, error) {
	var combined strings.Builder
	reservedTokens := 0
	for _, message := range messages {
		if combined.Len() > 0 {
			combined.WriteByte('\n')
		}
		combined.WriteString(message.Content)
		reservedTokens += 4
	}
	estimate, err := s.estimator.Estimate(ctx, PromptInput{Profile: profile, Segments: []PromptSegment{
		{Kind: PromptSegmentHistory, Content: combined.String()},
		{Kind: PromptSegmentToolGrowthReserve, ReservedTokens: reservedTokens},
	}})
	if err != nil {
		return TokenEstimate{}, err
	}
	if err := estimate.validate(); err != nil {
		return TokenEstimate{}, err
	}
	return estimate, nil
}

func validateContinuousTailRequest(request ContinuousTailRequest) error {
	if strings.TrimSpace(request.Profile) == "" || len(request.Profile) > 128 ||
		request.MaxTokens < 1 || len(request.Messages) == 0 || len(request.Messages) > maxContinuousTailMessages {
		return errors.New("continuous Tail request is invalid")
	}
	totalBytes := 0
	for index, message := range request.Messages {
		totalBytes += len(message.Content)
		if message.Sequence < 1 || strings.TrimSpace(message.Content) == "" ||
			len(message.Content) > 8*1024*1024 ||
			totalBytes > maxContinuousTailBytes ||
			(index > 0 && message.Sequence <= request.Messages[index-1].Sequence) ||
			message.Current != (index == len(request.Messages)-1) {
			return errors.New("continuous Tail messages are invalid")
		}
	}
	return nil
}
