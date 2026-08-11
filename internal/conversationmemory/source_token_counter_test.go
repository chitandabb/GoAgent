package conversationmemory

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
)

type recordingSourceEstimator struct {
	input    contextgovernance.PromptInput
	estimate contextgovernance.TokenEstimate
	err      error
}

func (e *recordingSourceEstimator) Estimate(
	_ context.Context,
	input contextgovernance.PromptInput,
) (contextgovernance.TokenEstimate, error) {
	e.input = input
	return e.estimate, e.err
}

func TestSourceTokenCounterUsesTheActiveProfileUpperBound(t *testing.T) {
	estimator := &recordingSourceEstimator{estimate: contextgovernance.TokenEstimate{
		EstimatedTokens: 80, UpperBoundTokens: 123, Method: contextgovernance.EstimationMethodLocalCalibrated,
		Profile: "chat-main",
	}}
	counter, err := NewSourceTokenCounter(estimator, "chat-main")
	if err != nil {
		t.Fatalf("NewSourceTokenCounter(): %v", err)
	}
	tokens, err := counter.Count(context.Background(), `{"messages":[{"content":"恢复原文"}]}`)
	if err != nil {
		t.Fatalf("Count(): %v", err)
	}
	if tokens != 123 {
		t.Fatalf("Count() = %d, want upper bound 123", tokens)
	}
	if estimator.input.Profile != "chat-main" || len(estimator.input.Segments) != 1 ||
		estimator.input.Segments[0].Kind != contextgovernance.PromptSegmentDynamicReferences ||
		estimator.input.Segments[0].Content == "" {
		t.Fatalf("PromptInput = %+v", estimator.input)
	}
}

func TestSourceTokenCounterPropagatesEstimatorFailure(t *testing.T) {
	want := errors.New("tokenizer unavailable")
	counter, err := NewSourceTokenCounter(&recordingSourceEstimator{err: want}, "chat-main")
	if err != nil {
		t.Fatalf("NewSourceTokenCounter(): %v", err)
	}
	if _, err := counter.Count(context.Background(), "result"); !errors.Is(err, want) {
		t.Fatalf("Count() error = %v, want %v", err, want)
	}
}
