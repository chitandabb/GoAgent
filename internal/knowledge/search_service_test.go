package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type rerankerStub struct {
	request RerankRequest
	result  RerankResult
	err     error
}

func (s *rerankerStub) Rerank(_ context.Context, request RerankRequest) (RerankResult, error) {
	s.request = request
	return s.result, s.err
}

func TestSearchServiceReranksCandidateSet(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	repository := &hybridRepositoryStub{fts: []SearchResult{
		{ChunkID: ids[0], ContentText: "first"},
		{ChunkID: ids[1], ContentText: "second"},
		{ChunkID: ids[2], ContentText: "third"},
	}}
	reranker := &rerankerStub{result: RerankResult{
		Items: []RerankItem{{Index: 2, RelevanceScore: 0.9}, {Index: 0, RelevanceScore: 0.7}},
		Usage: RerankUsage{TotalTokens: 9},
	}}
	service, err := NewSearchServiceWithReranker(repository, nil, EmbeddingProfile{}, 3, reranker, 3)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "query", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 || result.Results[0].ChunkID != ids[2] || result.Results[1].ChunkID != ids[0] ||
		result.Results[0].Score != 0.9 || !result.RerankApplied || result.RerankUsage.TotalTokens != 9 ||
		reranker.request.TopN != 2 || len(reranker.request.Documents) != 3 {
		t.Fatalf("result=%+v request=%+v", result, reranker.request)
	}
	if !result.Degraded || len(result.MissingChannels) != 1 || result.MissingChannels[0] != "vector" {
		t.Fatalf("degradation = %+v", result)
	}
}

func TestSearchServiceKeepsRetrievalOrderWhenRerankFails(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	repository := &hybridRepositoryStub{fts: []SearchResult{
		{ChunkID: first, ContentText: "first"}, {ChunkID: second, ContentText: "second"},
	}}
	service, err := NewSearchServiceWithReranker(
		repository, nil, EmbeddingProfile{}, 2, &rerankerStub{err: errors.New("provider unavailable")}, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "query", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].ChunkID != first || !result.Degraded ||
		len(result.MissingChannels) != 2 || result.MissingChannels[1] != "rerank" {
		t.Fatalf("result = %+v", result)
	}
}
