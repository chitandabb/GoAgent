package main

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
)

func TestProviderTokenBudgetMatchesDefaultCostCap(t *testing.T) {
	if got := providerTokenBudget(defaultMaxProviderCostCNY, defaultEmbeddingPriceCNYPerMillion); got != 100_000 {
		t.Fatalf("providerTokenBudget() = %d, want 100000", got)
	}
}

func TestProviderEvaluationEmbeddingConfigKeepsActualAttemptsInsideOuterBudget(t *testing.T) {
	base := config.EmbeddingModelConfig{RPM: 200, TPM: 150_000, MaxAttempts: 3}
	configured := providerEvaluationEmbeddingConfig(base, 900, 600_000)
	if configured.RPM != 900 || configured.TPM != 600_000 || configured.MaxAttempts != 1 {
		t.Fatalf("provider evaluation config = %+v", configured)
	}
}

func TestGuardedEmbedderBlocksBeforeExceedingEstimatedBudget(t *testing.T) {
	inner := &providerGuardStubEmbedder{}
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newGuardedEmbedder(inner, 9, cancel)
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

func TestGuardedEmbedderAbortsOnStructuredRateLimit(t *testing.T) {
	inner := &providerGuardStubEmbedder{err: &platformembedding.ProviderError{
		Category: platformembedding.ProviderErrorRateLimited, StatusCode: 429,
		Code: "Throttling.AllocationQuota",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	guard, err := newGuardedEmbedder(inner, 1_000, cancel)
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

func TestGuardedEmbedderIgnoresRateLimitStringMatching(t *testing.T) {
	// 字符串匹配已删除：只有结构化 ProviderError 才能触发整组取消。
	inner := &providerGuardStubEmbedder{err: errors.New(
		"embedding provider rejected request: status=429 code=Throttling.AllocationQuota",
	)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newGuardedEmbedder(inner, 1_000, cancel)
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

func TestGuardedEmbedderDoesNotAbortOnOrdinaryProviderFailure(t *testing.T) {
	inner := &providerGuardStubEmbedder{err: errors.New("temporary provider failure")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newGuardedEmbedder(inner, 1_000, cancel)
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
	guard, err := newGuardedEmbedder(inner, 20, cancel)
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
