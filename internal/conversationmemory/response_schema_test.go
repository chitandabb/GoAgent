package conversationmemory_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
)

func TestPayloadJSONSchemaPinsTheFixedTopLevelContract(t *testing.T) {
	schema := conversationmemory.PayloadJSONSchema()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, field := range []string{
		"conversationGoal", "facts", "decisions", "corrections", "evidenceReferences",
		"openQuestions", "todos", "taskReferences", "reportReferences",
	} {
		if !strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("schema does not contain %q: %s", field, text)
		}
	}
	if !strings.Contains(text, `"additionalProperties":false`) {
		t.Fatalf("schema does not reject additional properties: %s", text)
	}
	if len(schema.Required) != 9 {
		t.Fatalf("schema required fields = %#v, want all nine top-level fields", schema.Required)
	}
	for _, value := range []string{
		`"enum":["active","superseded","open","completed","cancelled"]`,
		`"enum":["knowledge_chunk","attachment","web","diagnosis_task","diagnosis_report"]`,
	} {
		if !strings.Contains(text, value) {
			t.Fatalf("schema does not contain %s: %s", value, text)
		}
	}
	for _, unsupported := range []string{`"pattern":`, `"uniqueItems":`, `"minItems":`, `"maxItems":`} {
		if strings.Contains(text, unsupported) {
			t.Fatalf("provider schema contains unverified StepFun keyword %s: %s", unsupported, text)
		}
	}
}
