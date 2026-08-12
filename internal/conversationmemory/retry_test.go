package conversationmemory

import (
	"testing"
	"time"
)

func TestServiceRetryDelayAppliesBoundedJitterToExponentialBackoff(t *testing.T) {
	service := &Service{retryBaseDelay: 100 * time.Millisecond, retryJitterRatio: 0.10}
	tests := []struct {
		retry  int
		random float64
		want   time.Duration
	}{
		{retry: 1, random: 0, want: 90 * time.Millisecond},
		{retry: 1, random: 1, want: 110 * time.Millisecond},
		{retry: 2, random: 0.5, want: 200 * time.Millisecond},
	}
	for _, tt := range tests {
		service.randomFloat = func() float64 { return tt.random }
		if got := service.retryDelay(tt.retry); got != tt.want {
			t.Fatalf("retryDelay(%d, %.1f) = %s, want %s", tt.retry, tt.random, got, tt.want)
		}
	}
}
