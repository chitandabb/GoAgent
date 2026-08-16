// Package embeddingquota is the single internal source of truth for the
// conservative embedding token estimate and the smooth per-minute RPM/TPM
// budget shared by the production client, evaluation guards, and observation
// tools. The formulas were extracted verbatim from the ingestion throughput
// observation guard; no second copy may exist anywhere else in the repo.
package embeddingquota

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode"
)

// EstimateTextTokens returns the conservative per-text token estimate used for
// TPM budgeting. The estimate deliberately includes headroom: CJK and other
// non-ASCII runes are counted individually; ASCII text is approximated at four
// bytes per token. It mirrors the evaluator's estimator so production and
// evaluation budget the same way.
func EstimateTextTokens(text string) int {
	asciiRunes, nonASCIIRunes := 0, 0
	for _, value := range text {
		if value <= unicode.MaxASCII {
			asciiRunes++
		} else {
			nonASCIIRunes++
		}
	}
	base := (asciiRunes+3)/4 + nonASCIIRunes
	return max(1, (base*11+7)/8+8)
}

// SpacingDuration returns the smoothed interval between per-minute budget
// units: time.Minute * units / perMinute.
func SpacingDuration(units, perMinute int) time.Duration {
	return time.Duration(float64(time.Minute) * float64(units) / float64(perMinute))
}

// Limiter smooths per-minute request (RPM) and estimated token (TPM) budgets.
// It is process-scoped: every consumer that shares one Limiter shares the
// whole budget and no consumer gets its own full quota. It is not safe to use
// across processes; horizontal scaling requires re-allocating budgets.
type Limiter struct {
	mu          sync.Mutex
	rpm         int
	tpm         int
	nextRequest time.Time
	nextToken   time.Time
}

// NewLimiter returns a smooth limiter for the given per-minute budgets.
func NewLimiter(rpm, tpm int) (*Limiter, error) {
	if rpm < 1 || tpm < 1 {
		return nil, errors.New("provider rate limits must be positive")
	}
	return &Limiter{rpm: rpm, tpm: tpm}, nil
}

// Wait blocks until the request and estimated-token budgets admit one request
// of the given token estimate, or until ctx is done. Tokens must be positive.
func (l *Limiter) Wait(ctx context.Context, tokens int) error {
	if tokens < 1 {
		return errors.New("provider token estimate must be positive")
	}
	now := time.Now()
	l.mu.Lock()
	startAt := maxTime(now, l.nextRequest, l.nextToken)
	l.nextRequest = startAt.Add(SpacingDuration(1, l.rpm))
	l.nextToken = startAt.Add(SpacingDuration(tokens, l.tpm))
	l.mu.Unlock()
	if !startAt.After(now) {
		return nil
	}
	timer := time.NewTimer(time.Until(startAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func maxTime(values ...time.Time) time.Time {
	result := values[0]
	for _, value := range values[1:] {
		if value.After(result) {
			result = value
		}
	}
	return result
}
