package agent

import (
	"errors"
	"testing"
)

func TestRegistryProtectsSkillDefinitions(t *testing.T) {
	definitions := testSkills()
	registry, err := NewRegistry(definitions...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	definition, err := registry.Get(SkillTicketDiagnosis)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	definition.AllowedTools[0] = "mutated_tool"
	again, err := registry.Get(SkillTicketDiagnosis)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.AllowedTools[0] != ToolReadExternalCase {
		t.Fatalf("registry definition was mutated: %v", again.AllowedTools)
	}
	if _, err = registry.Get("missing-skill"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
}

func TestSkillDefinitionRejectsDuplicateTools(t *testing.T) {
	definition := testSkills()[0]
	definition.AllowedTools = []string{ToolReadExternalCase, ToolReadExternalCase}
	if err := definition.Validate(); err == nil {
		t.Fatal("Validate accepted duplicate tools")
	}
}

func TestSkillDefinitionRejectsEmptyVersion(t *testing.T) {
	definition := testSkills()[0]
	definition.Version = "  "
	if err := definition.Validate(); err == nil {
		t.Fatal("Validate accepted empty version")
	}
}
