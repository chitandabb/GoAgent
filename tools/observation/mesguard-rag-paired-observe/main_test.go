package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestRunValidateOnlyChecksFixtureWithoutProvider(t *testing.T) {
	corpusPath, datasetPath := writeCommandFixture(t)
	if err := run([]string{
		"-validate-only", "-corpus", corpusPath, "-dataset", datasetPath, "-max-cases", "1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRequiresExplicitProviderExecution(t *testing.T) {
	corpusPath, datasetPath := writeCommandFixture(t)
	err := run([]string{"-corpus", corpusPath, "-dataset", datasetPath})
	if err == nil || err.Error() != "provider execution is disabled; review the budget and add -execute-provider" {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckedInCompressionPressureFixtureCanExceedProductionChunkLimit(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	corpus, err := readStrictJSON[knowledge.AdvancedRetrievalEvaluationCorpus](
		filepath.Join(root, "testdata", "rag-compression-pressure-v1.corpus.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := readStrictJSONL(
		filepath.Join(root, "testdata", "rag-compression-pressure-v1.jsonl"),
		func(value knowledge.AdvancedRetrievalEvaluationCase) error { return value.Validate() },
	)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := knowledge.ValidateAdvancedRetrievalFixture(corpus, cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].K < 4 || len(chunks["postgresql-advisory-locks"]) < 10 {
		t.Fatalf("pressure fixture cannot exceed six parent neighbors: case=%+v chunks=%d", cases, len(chunks["postgresql-advisory-locks"]))
	}
}

func TestParseOptionsRejectsConfoundedOrOverlappingOutputs(t *testing.T) {
	if _, err := parseOptions([]string{"-axis", "both"}); err == nil {
		t.Fatal("parseOptions accepted a confounded axis")
	}
	if _, err := parseOptions([]string{"-output", "result.json", "-summary", ".\\result.json"}); err == nil {
		t.Fatal("parseOptions accepted overlapping output paths")
	}
	if _, err := parseOptions([]string{"-axis", axisContext, "-require-compression-acceptance"}); err == nil {
		t.Fatal("parseOptions accepted compression acceptance on the context axis")
	}
}

func TestValidateCompressionAcceptanceRequiresTriggerAndPreservedGoldRecall(t *testing.T) {
	summary := knowledge.AdvancedRetrievalEvaluationSummary{
		Experiment: knowledge.AdvancedRetrievalVariantSummary{
			CompressionTriggeredRuns: 1,
			CompressionOmittedChunks: 2,
		},
	}
	if err := validateCompressionAcceptance(summary); err != nil {
		t.Fatal(err)
	}
	summary.Experiment.CompressionTriggeredRuns = 0
	if err := validateCompressionAcceptance(summary); err == nil {
		t.Fatal("validateCompressionAcceptance accepted a no-op compression run")
	}
	summary.Experiment.CompressionTriggeredRuns = 1
	summary.Delta.ContextRecall = -0.01
	if err := validateCompressionAcceptance(summary); err == nil {
		t.Fatal("validateCompressionAcceptance accepted a gold context recall regression")
	}
}

func TestPairedConfigsChangeOnlySelectedAxis(t *testing.T) {
	cfg := configForPairTest()
	baseline, experiment := pairedConfigs(cfg, axisContext)
	if baseline.Knowledge.Retrieval.ContextExpansionEnabled ||
		!experiment.Knowledge.Retrieval.ContextExpansionEnabled ||
		baseline.Knowledge.Retrieval.QueryRewrite.Enabled || experiment.Knowledge.Retrieval.QueryRewrite.Enabled ||
		baseline.Knowledge.Retrieval.ContextCompression.Enabled || experiment.Knowledge.Retrieval.ContextCompression.Enabled {
		t.Fatalf("context pair baseline=%+v experiment=%+v", baseline.Knowledge.Retrieval, experiment.Knowledge.Retrieval)
	}
	baseline, experiment = pairedConfigs(cfg, axisRewrite)
	if !baseline.Knowledge.Retrieval.ContextExpansionEnabled ||
		!experiment.Knowledge.Retrieval.ContextExpansionEnabled ||
		baseline.Knowledge.Retrieval.QueryRewrite.Enabled || !experiment.Knowledge.Retrieval.QueryRewrite.Enabled ||
		baseline.Knowledge.Retrieval.ContextCompression.Enabled || experiment.Knowledge.Retrieval.ContextCompression.Enabled {
		t.Fatalf("rewrite pair baseline=%+v experiment=%+v", baseline.Knowledge.Retrieval, experiment.Knowledge.Retrieval)
	}
	baseline, experiment = pairedConfigs(cfg, axisCompression)
	if !baseline.Knowledge.Retrieval.ContextExpansionEnabled ||
		!experiment.Knowledge.Retrieval.ContextExpansionEnabled ||
		baseline.Knowledge.Retrieval.ContextCompression.Enabled ||
		!experiment.Knowledge.Retrieval.ContextCompression.Enabled ||
		baseline.Knowledge.Retrieval.QueryRewrite.Enabled || experiment.Knowledge.Retrieval.QueryRewrite.Enabled {
		t.Fatalf("compression pair baseline=%+v experiment=%+v", baseline.Knowledge.Retrieval, experiment.Knowledge.Retrieval)
	}
}

func writeCommandFixture(t *testing.T) (string, string) {
	t.Helper()
	documents := []knowledge.AdvancedRetrievalEvaluationDocument{
		{
			DocumentKey: "go-cancel", Title: "Canceling database operations", MediaType: "text/markdown",
			SourceURL: "https://go.dev/doc/database/cancel-operations", SourceRetrievedAt: "2026-08-06",
			Content: "# Cancel operations\n\nUse a context to stop an operation that is no longer needed.",
		},
		{
			DocumentKey: "go-connections", Title: "Managing database connections", MediaType: "text/markdown",
			SourceURL: "https://go.dev/doc/database/manage-connections", SourceRetrievedAt: "2026-08-06",
			Content: "# Connections\n\nThe sql.DB handle represents a connection pool.",
		},
	}
	for index := range documents {
		documents[index].ContentSHA256 = knowledge.SHA256Hex(documents[index].Content)
	}
	corpus := knowledge.AdvancedRetrievalEvaluationCorpus{
		DatasetVersion: "rag-advanced-v1", ChunkerVersion: knowledge.AdvancedRetrievalMarkdownChunkerV1,
		ChunkMaxRunes: 256, ChunkOverlapRunes: 32, Documents: documents,
	}
	chunks, err := knowledge.BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		t.Fatal(err)
	}
	definition := knowledge.AdvancedRetrievalEvaluationCase{
		DatasetVersion: corpus.DatasetVersion, CaseID: "cancel", Query: "cancel database operation", K: 1,
		RelevantDocumentKeys: []string{"go-cancel"},
		RelevantChunks: []knowledge.RetrievalEvaluationChunkRef{{
			DocumentKey: "go-cancel", Ordinal: 0, ContentSHA256: chunks["go-cancel"][0].ContentSHA256,
		}},
	}
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	datasetPath := filepath.Join(directory, "dataset.jsonl")
	encodedCorpus, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpusPath, encodedCorpus, 0o600); err != nil {
		t.Fatal(err)
	}
	encodedCase, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(datasetPath, append(encodedCase, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return corpusPath, datasetPath
}

func configForPairTest() config.Config {
	return config.Config{Knowledge: config.KnowledgeConfig{Retrieval: config.KnowledgeRetrievalConfig{
		ContextExpansionEnabled: true,
		ContextCompression: config.KnowledgeContextCompressionConfig{
			Enabled: true, MaxChunks: 6, MaxRunes: 3000, MinScore: 0.05,
		},
		QueryRewrite: config.KnowledgeQueryRewriteConfig{Enabled: true},
	}}}
}
