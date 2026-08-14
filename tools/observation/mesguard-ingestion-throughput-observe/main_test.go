package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestRunValidateOnlyChecksPinnedCorpus(t *testing.T) {
	directory := t.TempDir()
	contents := []byte("public fixture content")
	digest := sha256.Sum256(contents)
	fileName := "fixture.txt"
	if err := os.WriteFile(filepath.Join(directory, fileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := corpusManifest{
		DatasetVersion: "rag-ingestion-test-v1",
		Documents: []corpusDocument{{
			DocumentID: "fixture", Title: "Fixture", SourceURL: "https://example.com/fixture.txt",
			Publisher: "Example", DownloadURL: "https://example.com/fixture.txt", UsageBasis: "Public test fixture.",
			FileName: fileName, MediaType: "text/plain", FormatClass: formatClassText, SizeBytes: int64(len(contents)),
			SHA256: hex.EncodeToString(digest[:]), PageCount: 0,
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "corpus.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{
		"-validate-only", "-corpus", manifestPath, "-source-root", directory,
	}, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
}

func TestRunEstimateOnlyDoesNotRequireProviderAuthorization(t *testing.T) {
	directory := t.TempDir()
	contents := []byte("# Public fixture\n\nBounded local estimate content.")
	digest := sha256.Sum256(contents)
	fileName := "fixture.md"
	if err := os.WriteFile(filepath.Join(directory, fileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(corpusManifest{
		DatasetVersion: "rag-ingestion-test-v1",
		Documents: []corpusDocument{{
			DocumentID: "fixture", Title: "Fixture", SourceURL: "https://example.com/fixture.md",
			Publisher: "Example", DownloadURL: "https://example.com/fixture.md", UsageBasis: "Public test fixture.",
			FileName: fileName, MediaType: "text/markdown", FormatClass: formatClassText, SizeBytes: int64(len(contents)),
			SHA256: hex.EncodeToString(digest[:]),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "corpus.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MESGUARD_CONFIG_FILE", filepath.Join("..", "..", "..", "config", "mesguard.toml"))
	if err := run(context.Background(), []string{
		"-estimate-only", "-corpus", manifestPath, "-source-root", directory,
	}, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
}

func TestRunRequiresExplicitProviderExecution(t *testing.T) {
	options, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.executeProvider || options.validateOnly || options.estimateOnly || options.maxDocuments != 1 || options.repetitions != 1 ||
		options.maxProviderCostCNY != defaultMaxProviderCostCNY ||
		options.embeddingPriceCNYPerMillion != defaultEmbeddingPriceCNYPerMillion ||
		options.providerRPM != defaultProviderRPM || options.providerTPM != defaultProviderTPM {
		t.Fatalf("options = %+v", options)
	}
}

func TestPinnedThroughputCorpusKeepsAcceptanceCoverage(t *testing.T) {
	manifest, err := readCorpus(filepath.Join("..", "..", "..", "testdata", "rag-ingestion-throughput-v1.corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DatasetVersion != "rag-ingestion-throughput-v1" || len(manifest.Documents) != 40 {
		t.Fatalf("dataset = %q, documents = %d", manifest.DatasetVersion, len(manifest.Documents))
	}
	formats := make(map[string]struct{}, 8)
	for _, document := range manifest.Documents {
		formats[document.FormatClass] = struct{}{}
	}
	if len(formats) != 8 {
		t.Fatalf("format classes = %v", formats)
	}
}

func TestParseOptionsAllowsProviderFreeDatabaseAblation(t *testing.T) {
	options, err := parseOptions([]string{"-database-ablation", "-repetitions", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.databaseAblation || options.executeProvider || options.repetitions != 2 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseOptions([]string{"-database-ablation", "-execute-provider"}); err == nil {
		t.Fatal("parseOptions accepted two execution modes")
	}
}

func TestParseOptionsRequiresProviderForDocumentConcurrencyAblation(t *testing.T) {
	if _, err := parseOptions([]string{"-document-concurrency-ablation"}); err == nil {
		t.Fatal("parseOptions accepted document concurrency ablation without provider execution")
	}
	if _, err := parseOptions([]string{"-document-concurrency-ablation", "-estimate-only"}); err != nil {
		t.Fatalf("parseOptions rejected provider-free concurrency estimate: %v", err)
	}
	options, err := parseOptions([]string{"-document-concurrency-ablation", "-execute-provider"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.documentConcurrencyAblation || !options.executeProvider {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseOptions([]string{"-execute-provider", "-max-provider-cost-cny", "0"}); err == nil {
		t.Fatal("parseOptions accepted a zero provider budget")
	}
}

func TestParseOptionsAcceptsExactDocumentSelection(t *testing.T) {
	options, err := parseOptions([]string{"-estimate-only", "-document-ids", "third, first"})
	if err != nil {
		t.Fatal(err)
	}
	if len(options.documentIDs) != 2 || options.documentIDs[0] != "third" || options.documentIDs[1] != "first" {
		t.Fatalf("documentIDs = %v", options.documentIDs)
	}
	if _, err := parseOptions([]string{"-document-ids", "first,first"}); err == nil {
		t.Fatal("parseOptions accepted duplicate document IDs")
	}
}

func TestRunAuditOnlyWritesProviderFreeClassification(t *testing.T) {
	directory := t.TempDir()
	contents := []byte("# Public fixture\n\nBounded searchable content.")
	digest := sha256.Sum256(contents)
	fileName := "fixture.md"
	if err := os.WriteFile(filepath.Join(directory, fileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(corpusManifest{
		DatasetVersion: "rag-ingestion-audit-test-v1",
		Documents: []corpusDocument{{
			DocumentID: "fixture", Title: "Fixture", SourceURL: "https://example.com/fixture.md",
			Publisher: "Example", DownloadURL: "https://example.com/fixture.md", UsageBasis: "Public test fixture.",
			FileName: fileName, MediaType: "text/markdown", FormatClass: formatClassText,
			SizeBytes: int64(len(contents)), SHA256: hex.EncodeToString(digest[:]),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "corpus.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(directory, "audit.json")
	t.Setenv("MESGUARD_CONFIG_FILE", filepath.Join("..", "..", "..", "config", "mesguard.toml"))
	if err := run(context.Background(), []string{
		"-audit-only", "-corpus", manifestPath, "-source-root", directory, "-audit-output", outputPath,
	}, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	reportBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var report corpusAuditSummary
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatal(err)
	}
	if report.Documents != 1 || report.FormatCount != 1 || report.TextReadyDocuments != 1 ||
		report.TotalChunks != 1 || len(report.Results) != 1 ||
		report.Results[0].Status != corpusAuditTextReady || !report.Results[0].SearchableWithoutProvider {
		t.Fatalf("report = %+v", report)
	}
}

func TestBuildWorkerMessageMatchesStrictParser(t *testing.T) {
	messageID, correlationID, taskID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	message, err := buildWorkerMessage(messageID, correlationID, taskID, versionID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := knowledgeworker.ParseMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MessageID != messageID || parsed.CorrelationID != correlationID ||
		parsed.TaskID != taskID || parsed.DocumentVersionID != versionID {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestFormatCountDistinguishesNativeAndScannedPDF(t *testing.T) {
	documents := []queuedDocument{
		{definition: corpusDocument{MediaType: "application/pdf", FormatClass: formatClassNativePDF}},
		{definition: corpusDocument{MediaType: "application/pdf", FormatClass: formatClassScannedPDF}},
	}
	if count := formatCount(documents); count != 2 {
		t.Fatalf("formatCount() = %d, want 2", count)
	}
}

func TestReadCorpusRejectsMissingFormatClass(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "corpus.json")
	contents := []byte(`{
  "datasetVersion": "rag-ingestion-test-v1",
  "documents": [{
    "documentId": "fixture",
    "title": "Fixture",
    "sourceUrl": "https://example.com/fixture.pdf",
    "fileName": "fixture.pdf",
    "mediaType": "application/pdf",
    "sizeBytes": 1,
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "pageCount": 1
  }]
}`)
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCorpus(manifestPath); err == nil {
		t.Fatal("readCorpus accepted a document without formatClass")
	}
}

func TestSelectCorpusDocumentsPreservesRequestedOrder(t *testing.T) {
	manifest := corpusManifest{Documents: []corpusDocument{
		{DocumentID: "first"}, {DocumentID: "second"}, {DocumentID: "third"},
	}}
	selected, err := selectCorpusDocuments(manifest, 1, []string{"third", "first"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].DocumentID != "third" || selected[1].DocumentID != "first" {
		t.Fatalf("selected = %+v", selected)
	}
	if _, err := selectCorpusDocuments(manifest, 1, []string{"missing"}); err == nil {
		t.Fatal("selectCorpusDocuments accepted an unknown document ID")
	}
}

func TestValidCorpusURLRequiresCredentialFreeHTTPS(t *testing.T) {
	for _, value := range []string{"http://example.com/file", "https://user:secret@example.com/file", " /relative"} {
		if validCorpusURL(value) {
			t.Fatalf("validCorpusURL(%q) = true", value)
		}
	}
	if !validCorpusURL("https://example.com/file?download=1") {
		t.Fatal("validCorpusURL rejected an HTTPS download URL")
	}
}

func TestDeterministicStagingVectorIsStableAndNormalized(t *testing.T) {
	first := deterministicStagingVector("chunk one", 1024)
	second := deterministicStagingVector("chunk one", 1024)
	different := deterministicStagingVector("chunk two", 1024)
	if len(first) != 1024 || len(second) != 1024 || len(different) != 1024 {
		t.Fatal("deterministic staging vector has an unexpected dimension")
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatal("deterministic staging vector changed for the same content")
		}
	}
	if err := knowledge.ValidateEmbeddingVector(first, 1024, true); err != nil {
		t.Fatal(err)
	}
	equal := true
	for index := range first {
		if first[index] != different[index] {
			equal = false
			break
		}
	}
	if equal {
		t.Fatal("deterministic staging vector ignored content")
	}
}

func TestClassifyCorpusAuditResultPreservesVisualRequirements(t *testing.T) {
	tests := []struct {
		chunks, assets int
		status         corpusAuditStatus
		searchable     bool
		requiresVisual bool
	}{
		{chunks: 3, status: corpusAuditTextReady, searchable: true},
		{chunks: 3, assets: 2, status: corpusAuditTextVisualPending, searchable: true, requiresVisual: true},
		{assets: 1, status: corpusAuditVisualRequired, requiresVisual: true},
		{status: corpusAuditNoSearchableOutput},
	}
	for _, test := range tests {
		status, searchable, requiresVisual := classifyCorpusAuditResult(test.chunks, test.assets)
		if status != test.status || searchable != test.searchable || requiresVisual != test.requiresVisual {
			t.Fatalf("classifyCorpusAuditResult(%d, %d) = %s, %t, %t", test.chunks, test.assets, status, searchable, requiresVisual)
		}
	}
}

func TestMaterializedVisualBytesDoesNotRepeatReferencedSourceBytes(t *testing.T) {
	assets := []knowledgeparser.VisualAsset{
		{Kind: knowledgeparser.VisualAssetDocumentPage, SizeBytes: 10_000},
		{Kind: knowledgeparser.VisualAssetEmbeddedImage, SizeBytes: 3, Content: []byte("img")},
	}
	if got := materializedVisualBytes(assets); got != 3 {
		t.Fatalf("materializedVisualBytes() = %d, want 3", got)
	}
}

func TestChunksForProviderEstimateAcceptsVisualOnlyDocument(t *testing.T) {
	chunks, err := chunksForProviderEstimate(knowledgeparser.Result{
		VisualAssets: []knowledgeparser.VisualAsset{{Kind: knowledgeparser.VisualAssetDocumentPage}},
	}, knowledge.TextChunkOptions{MaxRunes: 1000, OverlapRunes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks = %d, want 0", len(chunks))
	}
}

func TestChunksForProviderEstimateUsesProductionElementMerge(t *testing.T) {
	parsed := knowledgeparser.Result{Elements: []knowledge.DocumentElement{
		{Index: 0, ElementType: knowledge.ElementText, SectionPath: []string{"section"}, ContentText: "duplicate content"},
		{Index: 1, ElementType: knowledge.ElementText, SectionPath: []string{"section"}, ContentText: "duplicate content"},
	}}
	chunks, err := chunksForProviderEstimate(parsed, knowledge.TextChunkOptions{MaxRunes: 1000, OverlapRunes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
}
