package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestRunWritesStrictPairedSummary(t *testing.T) {
	directory := t.TempDir()
	datasetPath := filepath.Join(directory, "dataset.jsonl")
	inputPath := filepath.Join(directory, "observations.jsonl")
	outputPath := filepath.Join(directory, "result", "summary.json")
	hit := knowledge.RetrievalEvaluationChunkRef{
		DocumentKey: "doc-a", Ordinal: 0, ContentSHA256: knowledge.SHA256Hex("hit"),
	}
	neighbor := knowledge.RetrievalEvaluationChunkRef{
		DocumentKey: "doc-a", Ordinal: 1, ContentSHA256: knowledge.SHA256Hex("neighbor"),
	}
	definition := knowledge.AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha",
		RelevantDocumentKeys: []string{"doc-a"},
		RelevantChunks:       []knowledge.RetrievalEvaluationChunkRef{hit, neighbor}, K: 1,
	}
	baseline := knowledge.AdvancedRetrievalObservation{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Variant: knowledge.AdvancedRetrievalBaseline,
		RunID: "baseline-case-a", Query: "alpha", K: 1,
		RetrieverVersion: "postgres-rrf-v1", EmbeddingProfile: "knowledge-v1",
		QueryMode: knowledge.RetrievalQueryOriginal, FTSQueryCount: 1, VectorQueryCount: 1,
		QueryRewriteStatus: knowledge.QueryRewriteDisabled, ContextMode: knowledge.RetrievalContextChild,
		ReturnedDocumentKeys: []string{"doc-a"}, ReturnedHitChunks: []knowledge.RetrievalEvaluationChunkRef{hit},
		HitContextRunes: 10, DurationMillis: 2,
	}
	experiment := baseline
	experiment.Variant = knowledge.AdvancedRetrievalExperiment
	experiment.RunID = "experiment-case-a"
	experiment.ContextMode = knowledge.RetrievalContextParent
	experiment.ContextExpansionEnabled = true
	experiment.ContextExpanded = true
	experiment.ReturnedContextChunks = []knowledge.RetrievalEvaluationChunkRef{neighbor}
	experiment.ExpandedContextRunes = 8
	experiment.DurationMillis = 3
	writeTestJSONL(t, datasetPath, []knowledge.AdvancedRetrievalEvaluationCase{definition})
	writeTestJSONL(t, inputPath, []knowledge.AdvancedRetrievalObservation{baseline, experiment})

	if err := run([]string{"-dataset", datasetPath, "-input", inputPath, "-output", outputPath}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary knowledge.AdvancedRetrievalEvaluationSummary
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.PairedCases != 1 || summary.Baseline.ContextRecall != 0.5 ||
		summary.Experiment.ContextRecall != 1 || summary.Delta.ContextRecall != 0.5 {
		t.Fatalf("summary = %+v", summary)
	}
}

func writeTestJSONL[T any](t *testing.T, path string, values []T) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
