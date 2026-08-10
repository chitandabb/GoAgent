package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunConversationQualityEvaluation(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	caseJSON := strings.Join([]string{
		`{"datasetVersion":"conversation-quality-v1","caseId":"answer","userQuery":"查询制度","relevantSources":[{"sourceType":"knowledge_chunk","sourceRef":"knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222","contentSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","previewRequired":true}],"requiredCitationRefs":["knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222"],"requiredAnswerTerms":["制度"],"expectedOutcome":"answered"}`,
	}, "\n")
	observationJSON := strings.Join([]string{
		`{"datasetVersion":"conversation-quality-v1","caseId":"answer","runId":"seed-answer","observationKind":"seeded_contract","model":"fixture","modelVersion":"v1","promptVersion":"contract-v1","answer":"制度见附件。","outcome":"answered","retrievedSources":[{"sourceType":"knowledge_chunk","sourceRef":"knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222","contentSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","previewRequired":true}],"citations":[{"sourceType":"knowledge_chunk","sourceRef":"knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222","contentSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","previewContentSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"usage":{"modelCalls":1,"promptTokens":1,"completionTokens":1,"totalTokens":2},"durationMillis":10,"estimatedCostCny":0.001}`,
	}, "\n")
	casePath := writeQualityTempFile(t, caseJSON)
	observationPath := writeQualityTempFile(t, observationJSON)
	judgePath := writeQualityTempFile(t, `{"dataset_version":"conversation-quality-v1","case_id":"answer","provider":"dashscope","request_model":"qwen3-max","prompt_version":"rag-judge-v2","prompt_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","duration_millis":120,"usage":{"prompt_tokens":100,"completion_tokens":40,"total_tokens":140},"estimated_cost_cny":0.001,"scores":{"schema_version":"rag-judge-v2","verdict":"partial","answer_correctness":{"score":3,"reason":"core fact present"},"faithfulness":{"score":3,"reason":"mostly supported"},"answer_relevance":{"score":2,"reason":"extra detail"},"citation_correctness":{"score":4,"reason":"aligned"},"refusal_correctness":{"score":4,"reason":"answered"},"unsupported_claims":[],"missing_key_facts":[],"citation_issues":[]}}`)
	if exitCode := run([]string{"-dataset", casePath, "-input", observationPath, "-judge", judgePath}, stdout, stderr); exitCode != 0 {
		t.Fatalf("run()=%d stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"passRate": 1`) ||
		!strings.Contains(stdout.String(), `"judgedRuns": 1`) ||
		!strings.Contains(stdout.String(), `"averageFaithfulness": 0.75`) ||
		!strings.Contains(stdout.String(), `"averageAnswerRelevance": 0.5`) ||
		!strings.Contains(stdout.String(), `"averageCitationAlignment": 1`) {
		t.Fatalf("summary = %s", stdout.String())
	}
}

func TestRunConversationQualityEvaluationRejectsUnknownJSONField(t *testing.T) {
	casePath := writeQualityTempFile(t, `{"datasetVersion":"conversation-quality-v1","caseId":"answer","userQuery":"查询","expectedOutcome":"answered","unknown":true}`)
	observationPath := writeQualityTempFile(t, `{}`)
	stderr := &bytes.Buffer{}
	if exitCode := run([]string{"-dataset", casePath, "-input", observationPath}, &bytes.Buffer{}, stderr); exitCode != 1 {
		t.Fatalf("run()=%d stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func writeQualityTempFile(t *testing.T, contents string) string {
	t.Helper()
	file := t.TempDir() + "\\input.jsonl"
	if err := writeFileForQualityTest(file, contents); err != nil {
		t.Fatal(err)
	}
	return file
}

func writeFileForQualityTest(path, contents string) error {
	return os.WriteFile(path, []byte(contents+"\n"), 0o600)
}
