package conversationmemory

import (
	"context"
	"errors"
	"strings"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
)

type sourceTokenCounter struct {
	estimator contextgovernance.TokenEstimator
	profile   string
}

func NewSourceTokenCounter(
	estimator contextgovernance.TokenEstimator,
	profile string,
) (SourceTokenCounter, error) {
	profile = strings.TrimSpace(profile)
	if estimator == nil || profile == "" || len(profile) > 128 {
		return nil, errors.New("conversation memory source TokenEstimator and profile are required")
	}
	return &sourceTokenCounter{estimator: estimator, profile: profile}, nil
}

func (c *sourceTokenCounter) Count(ctx context.Context, content string) (int, error) {
	if c == nil || c.estimator == nil || c.profile == "" {
		return 0, errors.New("conversation memory source token counter is unavailable")
	}
	estimate, err := c.estimator.Estimate(ctx, contextgovernance.PromptInput{
		Profile: c.profile,
		Segments: []contextgovernance.PromptSegment{{
			Kind: contextgovernance.PromptSegmentDynamicReferences, Content: content,
		}},
	})
	if err != nil {
		return 0, err
	}
	if estimate.UpperBoundTokens < 0 || estimate.UpperBoundTokens < estimate.EstimatedTokens {
		return 0, errors.New("conversation memory source token estimate is invalid")
	}
	return estimate.UpperBoundTokens, nil
}
