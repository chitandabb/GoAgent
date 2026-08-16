package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

type recordingSmokeEmbedder struct {
	calls int
	texts []string
}

func (e *recordingSmokeEmbedder) Embed(
	_ context.Context,
	request knowledge.EmbeddingRequest,
) (knowledge.EmbeddingResult, error) {
	e.calls++
	e.texts = append(e.texts, request.Texts...)
	vector := make([]float32, 1024)
	vector[0] = 0.5
	return knowledge.EmbeddingResult{
		Vectors: [][]float32{vector}, Usage: knowledge.EmbeddingUsage{TotalTokens: 9},
	}, nil
}

func smokeProfileForTest() knowledge.EmbeddingProfile {
	return knowledge.EmbeddingProfile{Model: "text-embedding-v4", Dimensions: 1024}
}

func TestRunSmokeRefusesProviderCallsByDefault(t *testing.T) {
	embedder := &recordingSmokeEmbedder{}
	var out bytes.Buffer
	err := runSmoke(context.Background(), embedder, smokeProfileForTest(), smokeOptions{}, &out)
	if err == nil {
		t.Fatal("smoke must refuse provider calls without -allow-provider-calls")
	}
	if embedder.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 by default", embedder.calls)
	}
}

func TestRunRefusesBeforeLoadingConfigOrCredentials(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "")
	var out bytes.Buffer
	err := run(nil, &out)
	if err == nil || err.Error() != "embedding smoke provider calls are disabled; add -allow-provider-calls to run" {
		t.Fatalf("run() error = %v, want provider gate before config and credentials", err)
	}
}

func TestRunSmokeIssuesExactlyOneShortRequestWhenAllowed(t *testing.T) {
	embedder := &recordingSmokeEmbedder{}
	var out bytes.Buffer
	err := runSmoke(context.Background(), embedder, smokeProfileForTest(),
		smokeOptions{allowProviderCalls: true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", embedder.calls)
	}
	if len(embedder.texts) != 1 || len([]rune(embedder.texts[0])) > 64 ||
		embedder.texts[0] != smokeText {
		t.Fatalf("smoke texts = %v, want one short fixed text", embedder.texts)
	}
}

func TestRunSmokeOutputDoesNotLeakTextOrVectors(t *testing.T) {
	embedder := &recordingSmokeEmbedder{}
	var out bytes.Buffer
	if err := runSmoke(context.Background(), embedder, smokeProfileForTest(),
		smokeOptions{allowProviderCalls: true}, &out); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if strings.Contains(output, smokeText) {
		t.Fatalf("smoke output leaked the input text: %q", output)
	}
	if strings.Contains(output, "0.5") {
		t.Fatalf("smoke output leaked vector content: %q", output)
	}
	if !strings.Contains(output, "dimensions=1024") || !strings.Contains(output, "vectors=1") ||
		!strings.Contains(output, "total_tokens=9") {
		t.Fatalf("smoke output lost bounded summary fields: %q", output)
	}
}

func TestRunSmokeRejectsUnavailableEmbedder(t *testing.T) {
	var out bytes.Buffer
	if err := runSmoke(context.Background(), nil, smokeProfileForTest(),
		smokeOptions{allowProviderCalls: true}, &out); err == nil {
		t.Fatal("runSmoke accepted a nil embedder")
	}
}
