package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
)

func TestRunWritesPilotSummary(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "observations.jsonl")
	outputPath := filepath.Join(directory, "summary.json")
	observations := []knowledgeingestion.ThroughputObservation{
		observationForCommandTest(knowledgeingestion.ThroughputBaseline, 1000),
		observationForCommandTest(knowledgeingestion.ThroughputExperiment, 500),
	}
	file, err := os.OpenFile(inputPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-input", inputPath, "-output", outputPath}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary knowledgeingestion.ThroughputEvaluationSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Pairs != 1 || summary.AcceptanceEligible || summary.MeetsTarget || !summary.IntegrityPreserved {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRunRejectsUnknownObservationField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	if err := os.WriteFile(path, []byte(`{"unexpected":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-input", path}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("run error = %v", err)
	}
}

func observationForCommandTest(variant knowledgeingestion.ThroughputVariant, duration int64) knowledgeingestion.ThroughputObservation {
	return knowledgeingestion.ThroughputObservation{
		DatasetVersion: "rag-ingestion-v1", RunID: "run-" + string(variant), Repetition: 1, Variant: variant,
		CorpusFingerprint: strings.Repeat("a", 64), EnvironmentFingerprint: strings.Repeat("b", 64),
		Documents: 1, FormatCount: 1, SucceededDocuments: 1,
		SourceBytes: 1024 * 1024, Pages: 10, Elements: 20, Chunks: 10,
		DurationMillis: duration, QueueDurationMillis: duration / 4, ProcessDurationMillis: duration * 3 / 4,
		DocumentConcurrency: 1, EmbeddingBatchSize: 1, EmbeddingMaxConcurrent: 1,
		ChunkWriteBatchSize: 1, EmbeddingRequests: 10, EmbeddingTokens: 100,
		ChunkInsertBatches: 10, EmbeddingInsertBatches: 10,
	}
}
