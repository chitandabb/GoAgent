package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestFuseRRFUsesBothRanksAndContentHashDeduplication(t *testing.T) {
	firstID, secondID, duplicateID := uuid.New(), uuid.New(), uuid.New()
	fts := []SearchResult{
		{ChunkID: firstID, ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ChunkID: duplicateID, ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	vector := []SearchResult{
		{ChunkID: secondID, ContentSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
		{ChunkID: duplicateID, ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	results := fuseRRF(fts, vector, 60, 10)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for _, result := range results {
		if result.ContentSHA256 == fts[1].ContentSHA256 && (result.FTSRank != 2 || result.VectorRank != 2) {
			t.Fatalf("deduplicated result = %+v", result)
		}
	}
	if results[0].FusedScore <= results[2].FusedScore {
		t.Fatalf("results are not sorted by fused score: %+v", results)
	}
}

func TestHybridRetrieverDegradesToFTSWhenEmbeddingFails(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	retriever, err := NewHybridRetriever(repository, failingEmbedder{}, profile, 5)
	if err != nil {
		t.Fatal(err)
	}
	result, err := retriever.Search(context.Background(), uuid.New(), "query", 5)
	if err != nil || !result.Degraded || len(result.Results) != 1 || len(repository.vectorCalls) != 0 ||
		len(result.MissingChannels) != 1 || result.MissingChannels[0] != "vector" {
		t.Fatalf("result=%+v err=%v vectorCalls=%v", result, err, repository.vectorCalls)
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error) {
	return EmbeddingResult{}, errors.New("provider unavailable")
}

type hybridRepositoryStub struct {
	fts         []SearchResult
	vectorCalls [][]float32
}

func (r *hybridRepositoryStub) CreateDocument(context.Context, CreateDocumentInput) (Document, error) {
	return Document{}, errors.New("not implemented")
}
func (r *hybridRepositoryStub) PublishVersion(context.Context, PublishVersionInput) (DocumentVersion, error) {
	return DocumentVersion{}, errors.New("not implemented")
}
func (r *hybridRepositoryStub) SearchFTS(context.Context, uuid.UUID, string, int) ([]SearchResult, error) {
	return r.fts, nil
}
func (r *hybridRepositoryStub) SearchVector(_ context.Context, _ uuid.UUID, _ uuid.UUID, vector []float32, _ int) ([]SearchResult, error) {
	r.vectorCalls = append(r.vectorCalls, vector)
	return nil, nil
}
