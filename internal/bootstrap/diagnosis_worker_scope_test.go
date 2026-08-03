package bootstrap

import (
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
)

func TestTaskCapabilitiesFromScopeSeparatesAuthorizationFromDependencyHealth(t *testing.T) {
	capabilities, err := taskCapabilitiesFromScope(map[string]any{
		diagnosis.RequestScopeKeyAllowedCapabilities: []any{"case", "code"},
	})
	if err != nil {
		t.Fatalf("taskCapabilitiesFromScope: %v", err)
	}
	if len(capabilities) != 2 || capabilities[0] != agent.ToolCapabilityCase || capabilities[1] != agent.ToolCapabilityCode {
		t.Fatalf("capabilities = %v", capabilities)
	}
}

func TestTaskCapabilitiesFromScopeRejectsInvalidPersistedScope(t *testing.T) {
	_, err := taskCapabilitiesFromScope(map[string]any{
		diagnosis.RequestScopeKeyAllowedCapabilities: []any{"case", "unknown"},
	})
	if !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("taskCapabilitiesFromScope error = %v, want ErrInvalidTask", err)
	}
}

func TestRequestedSkillFromScopeUsesValidatedDefault(t *testing.T) {
	got, err := requestedSkillFromScope(map[string]any{})
	if err != nil || got != agent.SkillTicketDiagnosis {
		t.Fatalf("requestedSkillFromScope() = %q, %v", got, err)
	}

	got, err = requestedSkillFromScope(map[string]any{
		diagnosis.RequestScopeKeyRequestedSkill: diagnosis.RequestedSkillSQLInvestigation,
	})
	if err != nil || got != agent.SkillSQLInvestigation {
		t.Fatalf("requestedSkillFromScope(sql) = %q, %v", got, err)
	}
}
