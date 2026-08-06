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

func TestHybridRetrieverMergesQueryVariantsWithinEachChannelBeforeRRF(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-query-plan", "test", "embedding", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	result := func(id uuid.UUID, content string, score float64) SearchResult {
		return SearchResult{ChunkID: id, ContentSHA256: SHA256Hex(content), Score: score}
	}
	repository := &hybridRepositoryStub{
		ftsByQuery: map[string][]SearchResult{
			"pool timeout":          {result(a, "a", 0.8)},
			"connection pool":       {result(b, "b", 0.9)},
			"pool exhaustion cause": {result(a, "a", 0.95)},
		},
		vectorResults: [][]SearchResult{
			{result(c, "c", 0.8)}, {result(b, "b", 0.95)}, {result(c, "c", 0.9)},
		},
	}
	embedder := &recordingEmbedder{}
	retriever, err := NewHybridRetriever(repository, embedder, profile, 5)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildQueryPlan("pool timeout", QueryRewriteResult{
		LexicalQuery: "connection pool", SemanticQuery: "database connection pool exhaustion",
		Subqueries: []string{"pool exhaustion cause"}, PromptVersion: "query-rewrite-v1",
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	search, err := retriever.SearchPlan(context.Background(), uuid.New(), plan, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.ftsQueries) != 3 || len(repository.vectorCalls) != 3 || len(embedder.request.Texts) != 3 ||
		len(search.Results) != 3 || search.Results[0].ChunkID != b || search.Results[0].FTSRank == 0 ||
		search.Results[0].VectorRank == 0 {
		t.Fatalf("search=%+v fts=%v vectors=%d embedding=%+v", search, repository.ftsQueries, len(repository.vectorCalls), embedder.request)
	}
}

func TestMergeQueryResultsUsesRankBeforeCrossQueryScore(t *testing.T) {
	originalID, rewrittenID := uuid.New(), uuid.New()
	originalHash := SHA256Hex("original result")
	results := mergeQueryResults([][]SearchResult{
		{{ChunkID: originalID, ContentSHA256: originalHash, Score: 0.1}},
		{
			{ChunkID: rewrittenID, ContentSHA256: SHA256Hex("rewritten result"), Score: 0.99},
			{ChunkID: originalID, ContentSHA256: originalHash, Score: 1},
		},
	}, 5)
	if len(results) != 2 || results[0].ChunkID != originalID || results[0].Score != 0.1 || results[1].ChunkID != rewrittenID {
		t.Fatalf("results = %+v", results)
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error) {
	return EmbeddingResult{}, errors.New("provider unavailable")
}

type recordingEmbedder struct {
	request EmbeddingRequest
}

func (e *recordingEmbedder) Embed(_ context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	e.request = request
	vectors := make([][]float32, len(request.Texts))
	for index := range vectors {
		if index%2 == 0 {
			vectors[index] = []float32{1, 0}
		} else {
			vectors[index] = []float32{0, 1}
		}
	}
	return EmbeddingResult{Vectors: vectors}, nil
}

type hybridRepositoryStub struct {
	fts           []SearchResult
	ftsByQuery    map[string][]SearchResult
	ftsQueries    []string
	vectorCalls   [][]float32
	vectorResults [][]SearchResult
}

func (r *hybridRepositoryStub) CreateDocument(context.Context, CreateDocumentInput) (Document, error) {
	return Document{}, errors.New("not implemented")
}
func (r *hybridRepositoryStub) PublishVersion(context.Context, PublishVersionInput) (DocumentVersion, error) {
	return DocumentVersion{}, errors.New("not implemented")
}

func (r *hybridRepositoryStub) SearchFTS(_ context.Context, _ uuid.UUID, query string, _ int) ([]SearchResult, error) {
	r.ftsQueries = append(r.ftsQueries, query)
	if r.ftsByQuery != nil {
		return r.ftsByQuery[query], nil
	}
	return r.fts, nil
}
func (r *hybridRepositoryStub) SearchVector(_ context.Context, _ uuid.UUID, _ uuid.UUID, vector []float32, _ int) ([]SearchResult, error) {
	r.vectorCalls = append(r.vectorCalls, vector)
	index := len(r.vectorCalls) - 1
	if index < len(r.vectorResults) {
		return r.vectorResults[index], nil
	}
	return nil, nil
}
