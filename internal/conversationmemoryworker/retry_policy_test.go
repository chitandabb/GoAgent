package conversationmemoryworker

import (
	"testing"
	"time"
)

func TestExponentialRetryDelayUsesBoundedJitter(t *testing.T) {
	withoutJitter, err := newExponentialRetryDelay(0, func() float64 { return 0.5 })
	if err != nil {
		t.Fatalf("newExponentialRetryDelay(): %v", err)
	}
	if got := withoutJitter(1); got != 30*time.Second {
		t.Fatalf("attempt 1 delay = %s", got)
	}
	if got := withoutJitter(3); got != 2*time.Minute {
		t.Fatalf("attempt 3 delay = %s", got)
	}

	minimum, err := newExponentialRetryDelay(0.10, func() float64 { return 0 })
	if err != nil {
		t.Fatalf("minimum jitter policy: %v", err)
	}
	maximum, err := newExponentialRetryDelay(0.10, func() float64 { return 1 })
	if err != nil {
		t.Fatalf("maximum jitter policy: %v", err)
	}
	if got := minimum(2); got != 54*time.Second {
		t.Fatalf("minimum attempt 2 delay = %s", got)
	}
	if got := maximum(2); got != 66*time.Second {
		t.Fatalf("maximum attempt 2 delay = %s", got)
	}
	if _, err := NewExponentialRetryDelay(0.51); err == nil {
		t.Fatal("NewExponentialRetryDelay() accepted jitter above 50 percent")
	}
}
