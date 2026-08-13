package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/google/uuid"
)

func TestDocumentVersionStatusForStage(t *testing.T) {
	tests := []struct {
		name  string
		stage knowledge.IngestionStage
		want  string
	}{
		{name: "scanning", stage: knowledge.IngestionStageScanning, want: "scanning"},
		{name: "parsing", stage: knowledge.IngestionStageParsing, want: "parsing"},
		{name: "chunking", stage: knowledge.IngestionStageChunking, want: "chunking"},
		{name: "indexing", stage: knowledge.IngestionStageIndexing, want: "indexing"},
		{name: "publishing remains visible", stage: knowledge.IngestionStagePublishing, want: "publishing"},
		{name: "uploaded defaults to processing", stage: knowledge.IngestionStageUploaded, want: "processing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := documentVersionStatusForStage(test.stage); got != test.want {
				t.Fatalf("documentVersionStatusForStage(%q) = %q, want %q", test.stage, got, test.want)
			}
		})
	}
}

func TestShouldPublishKnowledgeVersionIncludesCurrentRepair(t *testing.T) {
	tests := []struct {
		candidate int
		current   int
		want      bool
	}{
		{candidate: 1, current: 0, want: true},
		{candidate: 2, current: 1, want: true},
		{candidate: 2, current: 2, want: true},
		{candidate: 1, current: 2, want: false},
	}
	for _, test := range tests {
		if got := shouldPublishKnowledgeVersion(test.candidate, test.current); got != test.want {
			t.Fatalf("shouldPublishKnowledgeVersion(%d, %d) = %v, want %v", test.candidate, test.current, got, test.want)
		}
	}
}

func TestNewKnowledgeWorkerRepositoryWithBatchSizeRejectsUnsafeValues(t *testing.T) {
	for _, batchSize := range []int{0, 501} {
		if _, err := NewKnowledgeWorkerRepositoryWithBatchSize(nil, batchSize); err == nil {
			t.Fatalf("batch size %d was accepted", batchSize)
		}
	}
}

func TestKnowledgeWriteRowsKeepsChunkAndEmbeddingIdentity(t *testing.T) {
	profile, err := knowledge.NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		knowledge.EmbeddingInputQuery, knowledge.EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	page := 2
	content := "batch insert content"
	createdAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.FixedZone("offset", 8*60*60))
	result := knowledgeworker.ExecutionResult{
		Chunks: []knowledge.ChunkDraft{{
			PageNumber: &page, ElementType: knowledge.ElementText, SectionPath: []string{"section"},
			ContentText: content, SearchText: knowledge.NormalizeSearchText(content),
			ContentSHA256: knowledge.SHA256Hex(content), Metadata: json.RawMessage(`{"source":"fixture"}`),
		}},
		EmbeddingProfile: &profile,
		Embeddings: []knowledge.ChunkEmbeddingDraft{{
			ChunkOrdinal: 0, ContentSHA256: knowledge.SHA256Hex(content), Vector: knowledgeRepositoryTestVector(1024, 0),
		}},
	}
	versionID := uuid.New()
	chunks, embeddings, err := knowledgeWriteRows(
		knowledgeworker.Lease{DocumentVersionID: versionID}, result, createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || len(embeddings) != 1 || chunks[0].ID == uuid.Nil ||
		embeddings[0].ChunkID != chunks[0].ID || chunks[0].DocumentVersionID != versionID ||
		embeddings[0].ContentSHA256 != chunks[0].ContentSHA256 ||
		embeddings[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("chunks = %+v embeddings = %+v", chunks, embeddings)
	}
}

func knowledgeRepositoryTestVector(dimensions, index int) []float32 {
	vector := make([]float32, dimensions)
	vector[index] = 1
	return vector
}
