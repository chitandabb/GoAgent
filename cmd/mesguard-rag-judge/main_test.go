package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/ragjudge"
)

func TestParseOptionsRequiresOneExplicitExecutionMode(t *testing.T) {
	valid, err := parseOptions([]string{"-input", "cases.jsonl", "-validate-only"})
	if err != nil || !valid.validateOnly || valid.maxCases != 1 {
		t.Fatalf("options=%+v err=%v", valid, err)
	}
	for _, args := range [][]string{
		{"-input", "cases.jsonl"},
		{"-input", "cases.jsonl", "-validate-only", "-execute-provider"},
		{"-input", "cases.jsonl", "-estimate-only", "-execute-provider"},
		{"-validate-only"},
		{"-input", "cases.jsonl", "-validate-only", "-max-cases", "21"},
		{"-input", "cases.jsonl", "-validate-only", "-max-provider-cost-cny", "0"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) accepted an unsafe option set", args)
		}
	}
}

func TestRunValidateOnlyUsesStrictBoundedJSONLWithoutProvider(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "judge-input.jsonl")
	encoded, err := json.Marshal(validJudgeInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"-input", inputPath, "-validate-only"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "provider_calls=0") || !strings.Contains(got, ragjudge.SchemaVersion) {
		t.Fatalf("stdout = %q", got)
	}

	invalidPath := filepath.Join(t.TempDir(), "invalid.jsonl")
	if err := os.WriteFile(invalidPath, append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-input", invalidPath, "-validate-only"}, &stdout); err == nil {
		t.Fatal("run accepted an unknown Judge input field")
	}
}

func TestValidateJudgeRuntimeConfigRejectsSchemaDriftBeforeExecution(t *testing.T) {
	cfg := config.JudgeModelConfig{
		Provider: "dashscope", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "qwen3-max",
		PromptFile: "config/prompts/rag-judge.md", PromptVersion: ragjudge.SchemaVersion,
		TimeoutMillis: 120_000, MaxOutputTokens: 2_048,
	}
	if err := validateJudgeRuntimeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.PromptVersion = "rag-judge-v1"
	if err := validateJudgeRuntimeConfig(cfg); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("validateJudgeRuntimeConfig error = %v", err)
	}
}

func validJudgeInput() ragjudge.Input {
	content := "New operations wait for an existing connection and the application can deadlock."
	evidence := ragjudge.Evidence{
		CitationID: "source-1", SourceRef: "knowledge:version/chunk",
		ContentSHA256: knowledge.SHA256Hex(content), ContentText: content,
	}
	return ragjudge.Input{
		DatasetVersion: "conversation-quality-v1", CaseID: "pool-wait",
		AnswerProvider: "stepfun", AnswerModel: "step-3.7-flash",
		Question: "Why does the query wait?", Answerable: true,
		GoldFacts:      []string{"New operations wait.", "The application can deadlock."},
		AllowedSources: []ragjudge.Evidence{evidence}, CandidateAnswer: "It waits and can deadlock [source-1].",
		CitedEvidence: []ragjudge.Evidence{evidence},
	}
}
