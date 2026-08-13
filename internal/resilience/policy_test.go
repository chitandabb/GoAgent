package resilience_test

import (
	"context"
	"testing"

	"github.com/chitandabb/GoAgent/internal/resilience"
)

func TestStablePolicyContracts(t *testing.T) {
	tests := []struct {
		policy         resilience.Policy
		attempts       int
		allowsFallback bool
		exhausted      resilience.ExhaustedOutcome
	}{
		{policy: resilience.PolicyStrict, attempts: 1, exhausted: resilience.ExhaustedFail},
		{policy: resilience.PolicyRepairThenFail, attempts: 2, exhausted: resilience.ExhaustedFail},
		{policy: resilience.PolicyBestEffort, attempts: 1, allowsFallback: true, exhausted: resilience.ExhaustedFallback},
	}
	for _, test := range tests {
		t.Run(string(test.policy), func(t *testing.T) {
			contract, err := test.policy.Contract()
			if err != nil {
				t.Fatal(err)
			}
			if contract.MaxAttempts != test.attempts || contract.AllowsFallback != test.allowsFallback ||
				contract.OnExhausted != test.exhausted {
				t.Fatalf("contract = %+v", contract)
			}
		})
	}
	if _, err := resilience.Policy("retry_forever").Contract(); err == nil {
		t.Fatal("unknown policy should be rejected")
	}
}

func TestRunIdentityRoundTripsThroughContext(t *testing.T) {
	identity := resilience.RunIdentity{RunID: "turn-42", TraceID: "trace-7"}
	ctx := resilience.WithRunIdentity(context.Background(), identity)
	got, ok := resilience.RunIdentityFromContext(ctx)
	if !ok || got != identity {
		t.Fatalf("identity = %+v, ok = %v", got, ok)
	}
	if _, ok := resilience.RunIdentityFromContext(context.Background()); ok {
		t.Fatal("empty context should not invent a correlation identity")
	}
}

func TestDegradationEventRequiresStableFallbackIdentity(t *testing.T) {
	event := resilience.DegradationEvent{
		Operation: "query_rewrite", Policy: resilience.PolicyBestEffort,
		Fallback: "original_query", ReasonCode: "provider_error",
		RunID: "turn-42", Provider: "dashscope", Model: "qwen-flash",
		DurationMillis: 12,
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := event
	invalid.RunID = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("event without run identity should be rejected")
	}
	invalid = event
	invalid.Policy = resilience.PolicyStrict
	if err := invalid.Validate(); err == nil {
		t.Fatal("strict policy cannot describe a fallback")
	}
}
