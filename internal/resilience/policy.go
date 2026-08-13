package resilience

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type OperationError struct {
	Operation  string
	Policy     Policy
	ReasonCode string
	Err        error
}

func (e *OperationError) Error() string {
	if e == nil {
		return "resilience operation failed"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s failed: %s", e.Operation, e.ReasonCode)
	}
	return fmt.Sprintf("%s failed (%s): %v", e.Operation, e.ReasonCode, e.Err)
}

func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RunIdentity struct {
	RunID   string
	TraceID string
}

type runIdentityContextKey struct{}

func WithRunIdentity(ctx context.Context, identity RunIdentity) context.Context {
	return context.WithValue(ctx, runIdentityContextKey{}, identity)
}

func RunIdentityFromContext(ctx context.Context) (RunIdentity, bool) {
	if ctx == nil {
		return RunIdentity{}, false
	}
	identity, ok := ctx.Value(runIdentityContextKey{}).(RunIdentity)
	if !ok || strings.TrimSpace(identity.RunID) == "" || identity.RunID != strings.TrimSpace(identity.RunID) ||
		len(identity.RunID) > 128 || identity.TraceID != strings.TrimSpace(identity.TraceID) || len(identity.TraceID) > 128 {
		return RunIdentity{}, false
	}
	return identity, true
}

type Policy string

type ExhaustedOutcome string

const (
	PolicyStrict         Policy = "strict"
	PolicyRepairThenFail Policy = "repair_then_fail"
	PolicyBestEffort     Policy = "best_effort"

	ExhaustedFail     ExhaustedOutcome = "fail"
	ExhaustedFallback ExhaustedOutcome = "fallback"
)

type Contract struct {
	MaxAttempts    int
	AllowsFallback bool
	OnExhausted    ExhaustedOutcome
}

func (p Policy) Contract() (Contract, error) {
	switch p {
	case PolicyStrict:
		return Contract{MaxAttempts: 1, OnExhausted: ExhaustedFail}, nil
	case PolicyRepairThenFail:
		return Contract{MaxAttempts: 2, OnExhausted: ExhaustedFail}, nil
	case PolicyBestEffort:
		return Contract{MaxAttempts: 1, AllowsFallback: true, OnExhausted: ExhaustedFallback}, nil
	default:
		return Contract{}, errors.New("resilience policy is invalid")
	}
}

var machineLabel = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type DegradationEvent struct {
	Operation      string `json:"operation"`
	Policy         Policy `json:"policy"`
	Fallback       string `json:"fallback"`
	ReasonCode     string `json:"reasonCode"`
	RunID          string `json:"runId"`
	TraceID        string `json:"traceId,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	DurationMillis int64  `json:"durationMillis"`
}

func (e DegradationEvent) Validate() error {
	contract, err := e.Policy.Contract()
	if err != nil {
		return err
	}
	if !contract.AllowsFallback || !machineLabel.MatchString(e.Operation) ||
		!machineLabel.MatchString(e.Fallback) || !machineLabel.MatchString(e.ReasonCode) {
		return errors.New("degradation event fallback contract is invalid")
	}
	if strings.TrimSpace(e.RunID) == "" || e.RunID != strings.TrimSpace(e.RunID) || len(e.RunID) > 128 ||
		e.TraceID != strings.TrimSpace(e.TraceID) || len(e.TraceID) > 128 || e.DurationMillis < 0 {
		return errors.New("degradation event runtime identity is invalid")
	}
	providerPresent := strings.TrimSpace(e.Provider) != ""
	modelPresent := strings.TrimSpace(e.Model) != ""
	if providerPresent != modelPresent || e.Provider != strings.TrimSpace(e.Provider) ||
		e.Model != strings.TrimSpace(e.Model) || len(e.Provider) > 128 || len(e.Model) > 128 {
		return errors.New("degradation event model identity is invalid")
	}
	return nil
}

type Observer interface {
	ObserveDegradation(DegradationEvent)
}

type ObserverFunc func(DegradationEvent)

func (f ObserverFunc) ObserveDegradation(event DegradationEvent) {
	if f != nil {
		f(event)
	}
}
