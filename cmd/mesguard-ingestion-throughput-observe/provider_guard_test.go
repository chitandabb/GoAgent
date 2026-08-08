package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestEstimateEmbeddingTextTokensKeepsSafetyHeadroom(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{text: "abcd", want: 10},
		{text: "中文", want: 11},
		{text: "", want: 8},
		{text: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: 22},
	}
	for _, test := range tests {
		if got := estimateEmbeddingTextTokens(test.text); got != test.want {
			t.Fatalf("estimateEmbeddingTextTokens(%q) = %d, want %d", test.text, got, test.want)
		}
	}
}

func TestProviderTokenBudgetMatchesDefaultCostCap(t *testing.T) {
	if got := providerTokenBudget(defaultMaxProviderCostCNY, defaultEmbeddingPriceCNYPerMillion); got != 100_000 {
		t.Fatalf("providerTokenBudget() = %d, want 100000", got)
	}
}

func TestGuardedEmbedderBlocksBeforeExceedingEstimatedBudget(t *testing.T) {
	inner := &providerGuardStubEmbedder{}
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newGuardedEmbedder(inner, 9, 100_000, 100_000_000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.Embed(ctx, knowledge.EmbeddingRequest{
		Texts: []string{"a"}, InputType: knowledge.EmbeddingInputDocument,
	})
	if err == nil || inner.calls != 0 || guard.Err() == nil || ctx.Err() == nil {
		t.Fatalf("err=%v calls=%d guardErr=%v contextErr=%v", err, inner.calls, guard.Err(), ctx.Err())
	}
}

func TestGuardedEmbedderAbortsOnProviderRateLimit(t *testing.T) {
	inner := &providerGuardStubEmbedder{err: errors.New(
		"embedding provider rejected request: status=429 code=Throttling.AllocationQuota",
	)}
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newGuardedEmbedder(inner, 1_000, 100_000, 100_000_000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.Embed(ctx, knowledge.EmbeddingRequest{
		Texts: []string{"bounded input"}, InputType: knowledge.EmbeddingInputDocument,
	})
	if err == nil || inner.calls != 1 || guard.Err() == nil || ctx.Err() == nil {
		t.Fatalf("err=%v calls=%d guardErr=%v contextErr=%v", err, inner.calls, guard.Err(), ctx.Err())
	}
}

func TestGuardedEmbedderDoesNotAbortOnOrdinaryProviderFailure(t *testing.T) {
	inner := &providerGuardStubEmbedder{err: errors.New("temporary provider failure")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newGuardedEmbedder(inner, 1_000, 100_000, 100_000_000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.Embed(ctx, knowledge.EmbeddingRequest{
		Texts: []string{"bounded input"}, InputType: knowledge.EmbeddingInputDocument,
	})
	if err == nil || guard.Err() != nil || ctx.Err() != nil {
		t.Fatalf("err=%v guardErr=%v contextErr=%v", err, guard.Err(), ctx.Err())
	}
}

func TestGuardedEmbedderStopsWhenActualUsageCrossesBudget(t *testing.T) {
	inner := &providerGuardStubEmbedder{result: knowledge.EmbeddingResult{
		Usage: knowledge.EmbeddingUsage{TotalTokens: 21},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newGuardedEmbedder(inner, 20, 100_000, 100_000_000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.Embed(ctx, knowledge.EmbeddingRequest{
		Texts: []string{"a"}, InputType: knowledge.EmbeddingInputDocument,
	})
	if err == nil || inner.calls != 1 || guard.Err() == nil || ctx.Err() == nil {
		t.Fatalf("err=%v calls=%d guardErr=%v contextErr=%v", err, inner.calls, guard.Err(), ctx.Err())
	}
}

func TestSpacingDurationRepresentsPerMinuteLimits(t *testing.T) {
	if got := spacingDuration(600_000, 600_000); got != time.Minute {
		t.Fatalf("token spacing = %s, want %s", got, time.Minute)
	}
	if got := spacingDuration(1, 60); got != time.Second {
		t.Fatalf("request spacing = %s, want %s", got, time.Second)
	}
}

type providerGuardStubEmbedder struct {
	calls  int
	result knowledge.EmbeddingResult
	err    error
}

func (e *providerGuardStubEmbedder) Embed(
	context.Context,
	knowledge.EmbeddingRequest,
) (knowledge.EmbeddingResult, error) {
	e.calls++
	return e.result, e.err
}
