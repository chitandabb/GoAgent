package embeddingquota

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestEstimateTextTokensKeepsSafetyHeadroom(t *testing.T) {
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
		if got := EstimateTextTokens(test.text); got != test.want {
			t.Fatalf("EstimateTextTokens(%q) = %d, want %d", test.text, got, test.want)
		}
	}
}

func TestSpacingDurationRepresentsPerMinuteLimits(t *testing.T) {
	if got := SpacingDuration(600_000, 600_000); got != time.Minute {
		t.Fatalf("token spacing = %s, want %s", got, time.Minute)
	}
	if got := SpacingDuration(1, 60); got != time.Second {
		t.Fatalf("request spacing = %s, want %s", got, time.Second)
	}
}

func TestNewLimiterRejectsInvalidBudgets(t *testing.T) {
	for _, test := range []struct {
		rpm, tpm int
	}{
		{rpm: 0, tpm: 600_000},
		{rpm: 600, tpm: 0},
		{rpm: -1, tpm: 600_000},
	} {
		if _, err := NewLimiter(test.rpm, test.tpm); err == nil {
			t.Fatalf("NewLimiter(%d, %d) accepted an invalid budget", test.rpm, test.tpm)
		}
	}
}

func TestLimiterWaitsForRequestSpacing(t *testing.T) {
	limiter, err := NewLimiter(600, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := limiter.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed < 90*time.Millisecond {
		t.Fatalf("two requests completed after %s, want at least 90ms at 600 RPM", elapsed)
	}
}

func TestLimiterWaitsForTokenSpacing(t *testing.T) {
	limiter, err := NewLimiter(600_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 6_000); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := limiter.Wait(context.Background(), 6_000); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed < 590*time.Millisecond {
		t.Fatalf("6000 estimated tokens completed after %s, want at least 590ms at 600000 TPM", elapsed)
	}
}

func TestLimiterWaitCombinesRequestAndTokenSpacing(t *testing.T) {
	limiter, err := NewLimiter(120, 6_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := limiter.Wait(context.Background(), 30_000); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed < 480*time.Millisecond {
		t.Fatalf("combined spacing completed after %s, want at least 480ms (max of RPM and TPM spacing)", elapsed)
	}
}

func TestLimiterWaitHonorsContextCancellation(t *testing.T) {
	limiter, err := NewLimiter(60, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	defer cancel()
	startedAt := time.Now()
	err = limiter.Wait(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("canceled wait returned after %s", elapsed)
	}
}

func TestLimiterWaitRejectsNonPositiveTokens(t *testing.T) {
	limiter, err := NewLimiter(600, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background(), 0); err == nil {
		t.Fatal("Wait accepted a non-positive token estimate")
	}
}

func TestLimiterSharesBudgetAcrossConsumers(t *testing.T) {
	limiter, err := NewLimiter(600, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	var first sync.WaitGroup
	var gate sync.WaitGroup
	first.Add(1)
	gate.Add(2)
	for range 2 {
		go func() {
			defer gate.Done()
			first.Wait()
			if err := limiter.Wait(context.Background(), 1); err != nil {
				t.Error(err)
			}
		}()
	}
	first.Done()
	if err := limiter.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	gate.Wait()
	if elapsed := time.Since(startedAt); elapsed < 180*time.Millisecond {
		t.Fatalf("three consumers completed after %s, want at least 180ms for a shared 600 RPM budget", elapsed)
	}
}
