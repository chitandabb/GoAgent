package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadObservationsRejectsInvalidJSONL(t *testing.T) {
	_, err := readObservations(strings.NewReader("{invalid}\n"))
	if err == nil {
		t.Fatal("readObservations accepted invalid JSONL")
	}
}

func TestReadObservationsRequiresActualAllowedTools(t *testing.T) {
	_, err := readObservations(strings.NewReader(`{"datasetVersion":"dev-v1","caseId":"sample","variant":"experiment","runId":"run-1","observationSchemaVersion":"evaluation-observation-v3","model":"stepfun","modelVersion":"step-3.7-flash","reasoningEffort":"medium","promptVersion":"v1","toolProfileId":"diagnosis-default","toolSchemaFingerprint":"` + strings.Repeat("2", 64) + `","modelProfileFingerprint":"` + strings.Repeat("a", 64) + `","implementationRevision":"git:test","comparisonFingerprint":"sha256:` + strings.Repeat("c", 64) + `","sharedToolNames":["read_external_case","skill"],"baselineOnlyToolNames":["search_code"],"selectedSkill":"ticket-diagnosis"}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "allowedTools") {
		t.Fatalf("readObservations error = %v", err)
	}
}

func TestRunRequiresInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run code = %d", code)
	}
}

func TestReadJSONLinesRejectsUnknownFields(t *testing.T) {
	_, err := readCases(strings.NewReader(`{"datasetVersion":"dev-v1","caseId":"case-1","taskType":"diagnosis","userQuery":"q","expectedSkill":"ticket-diagnosis","acceptableConclusionStatuses":["probable"],"unknown":true}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("readCases error = %v", err)
	}
}
