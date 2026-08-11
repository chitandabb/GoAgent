package contextgovernance

import (
	"encoding/json"
	"testing"
)

func TestCanonicalToolContractIgnoresRegistrationAndJSONPropertyOrder(t *testing.T) {
	first, err := NewCanonicalToolContract([]ToolDefinition{
		{Name: "zeta", Description: "second", Parameters: json.RawMessage(`{"type":"object","required":["b","a"],"properties":{"b":{"type":"string"},"a":{"type":"integer"}}}`)},
		{Name: "alpha", Description: "first", Parameters: json.RawMessage(`{"required":["query"],"type":"object","properties":{"query":{"type":"string"}}}`)},
	})
	if err != nil {
		t.Fatalf("NewCanonicalToolContract(first): %v", err)
	}
	second, err := NewCanonicalToolContract([]ToolDefinition{
		{Name: "alpha", Description: "first", Parameters: json.RawMessage(`{"properties":{"query":{"type":"string"}},"type":"object","required":["query"]}`)},
		{Name: "zeta", Description: "second", Parameters: json.RawMessage(`{"properties":{"a":{"type":"integer"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("NewCanonicalToolContract(second): %v", err)
	}
	if first.Fingerprint != second.Fingerprint || first.ModelVisibleJSON != second.ModelVisibleJSON {
		t.Fatalf("canonical contracts differ:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first.ToolNames) != 2 || first.ToolNames[0] != "alpha" || first.ToolNames[1] != "zeta" {
		t.Fatalf("tool order = %v", first.ToolNames)
	}
}

func TestCanonicalToolContractRejectsTrimmedDuplicatesAndTrailingJSON(t *testing.T) {
	if _, err := NewCanonicalToolContract([]ToolDefinition{
		{Name: "search"}, {Name: " search "},
	}); err == nil {
		t.Fatal("NewCanonicalToolContract accepted duplicate trimmed names")
	}
	if _, err := NewCanonicalToolContract([]ToolDefinition{{
		Name: "search", Parameters: json.RawMessage(`{} {}`),
	}}); err == nil {
		t.Fatal("NewCanonicalToolContract accepted multiple JSON values")
	}
}

func TestPromptEpochChangesForEveryStablePromptDimension(t *testing.T) {
	base := PromptIdentityInput{
		ModelProfile:          "chat-main",
		ModelProvider:         "stepfun",
		ModelID:               "step-3.7-flash",
		SystemPromptVersion:   "conversation-v6",
		SystemPrompt:          "stable system",
		ToolSchemaFingerprint: SHA256Hex("tool-contract-v1"),
		PreloadedSkill:        "knowledge-qa-v1",
		SummaryFingerprint:    SHA256Hex("summary-v1"),
	}
	identity, err := BuildPromptIdentity(base)
	if err != nil {
		t.Fatalf("BuildPromptIdentity(base): %v", err)
	}
	mutations := []func(*PromptIdentityInput){
		func(input *PromptIdentityInput) { input.ModelID = "step-3.7" },
		func(input *PromptIdentityInput) { input.SystemPrompt = "changed system" },
		func(input *PromptIdentityInput) { input.ToolSchemaFingerprint = SHA256Hex("tool-contract-v2") },
		func(input *PromptIdentityInput) { input.PreloadedSkill = "ticket-diagnosis-v1" },
		func(input *PromptIdentityInput) { input.SummaryFingerprint = SHA256Hex("summary-v2") },
	}
	for index, mutate := range mutations {
		changed := base
		mutate(&changed)
		candidate, buildErr := BuildPromptIdentity(changed)
		if buildErr != nil {
			t.Fatalf("BuildPromptIdentity(mutation %d): %v", index, buildErr)
		}
		if candidate.PromptEpochID == identity.PromptEpochID {
			t.Fatalf("mutation %d did not change epoch", index)
		}
	}
}
