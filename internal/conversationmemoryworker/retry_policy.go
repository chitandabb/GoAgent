package conversationmemoryworker

import (
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

const maxRetryJitterRatio = 0.50

// NewExponentialRetryDelay returns the durable application-retry policy used
// between Memory Job attempts. RabbitMQ retry copies remain limited to
// transient Claim/Lease infrastructure failures.
func NewExponentialRetryDelay(jitterRatio float64) (func(int) time.Duration, error) {
	return newExponentialRetryDelay(jitterRatio, rand.Float64)
}

func newExponentialRetryDelay(
	jitterRatio float64,
	randomFloat func() float64,
) (func(int) time.Duration, error) {
	if math.IsNaN(jitterRatio) || math.IsInf(jitterRatio, 0) ||
		jitterRatio < 0 || jitterRatio > maxRetryJitterRatio || randomFloat == nil {
		return nil, errors.New("conversation memory retry jitter ratio is invalid")
	}
	return func(attempt int) time.Duration {
		base := defaultRetryDelay(attempt)
		if jitterRatio == 0 {
			return base
		}
		random := randomFloat()
		if random < 0 {
			random = 0
		} else if random > 1 {
			random = 1
		}
		multiplier := 1 + (random*2-1)*jitterRatio
		return time.Duration(float64(base) * multiplier)
	}, nil
}
