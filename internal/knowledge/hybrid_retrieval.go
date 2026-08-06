package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
)

const defaultRRFK = 60

type HybridSearch struct {
	Results         []SearchResult
	Degraded        bool
	Sources         []string
	MissingChannels []string
	RerankApplied   bool
	RerankUsage     RerankUsage
}

type HybridRetriever struct {
	repository Repository
	embedder   Embedder
	profile    EmbeddingProfile
	rrfK       int
	vectorTopN int
}

func NewHybridRetriever(repository Repository, embedder Embedder, profile EmbeddingProfile, vectorTopN int) (*HybridRetriever, error) {
	if repository == nil || embedder == nil {
		return nil, errors.New("hybrid retriever dependencies are nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if vectorTopN < 1 || vectorTopN > 100 {
		return nil, errors.New("hybrid retriever vector topN is invalid")
	}
	return &HybridRetriever{repository: repository, embedder: embedder, profile: profile, rrfK: defaultRRFK, vectorTopN: vectorTopN}, nil
}

func (r *HybridRetriever) Search(ctx context.Context, actorID uuid.UUID, query string, limit int) (HybridSearch, error) {
	if r == nil || r.repository == nil || r.embedder == nil {
		return HybridSearch{}, errors.New("hybrid retriever is unavailable")
	}
	if actorID == uuid.Nil || limit < 1 || limit > 50 {
		return HybridSearch{}, errors.New("hybrid retrieval request is invalid")
	}
	var (
		ftsResults   []SearchResult
		ftsErr       error
		embedding    EmbeddingResult
		embeddingErr error
	)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		ftsResults, ftsErr = r.repository.SearchFTS(ctx, actorID, query, r.vectorTopN)
	}()
	go func() {
		defer group.Done()
		embedding, embeddingErr = r.embedder.Embed(ctx, EmbeddingRequest{
			Texts: []string{query}, InputType: r.profile.QueryInputType,
		})
		if embeddingErr == nil {
			embeddingErr = embedding.Validate(1, r.profile.Dimensions, r.profile.Normalize)
		}
	}()
	group.Wait()

	var vectorResults []SearchResult
	var vectorErr error
	if embeddingErr == nil {
		vectorResults, vectorErr = r.repository.SearchVector(ctx, actorID, r.profile.ID, embedding.Vectors[0], r.vectorTopN)
	}
	if ftsErr != nil && (embeddingErr != nil || vectorErr != nil) {
		return HybridSearch{}, fmt.Errorf("hybrid retrieval failed: fts=%v vector=%v embedding=%v", ftsErr, vectorErr, embeddingErr)
	}
	if ftsErr != nil {
		return HybridSearch{
			Results: limitResults(vectorResults, limit), Degraded: true,
			Sources: []string{"vector"}, MissingChannels: []string{"fts"},
		}, nil
	}
	if embeddingErr != nil || vectorErr != nil {
		return HybridSearch{
			Results: limitResults(ftsResults, limit), Degraded: true,
			Sources: []string{"fts"}, MissingChannels: []string{"vector"},
		}, nil
	}
	return HybridSearch{Results: fuseRRF(ftsResults, vectorResults, r.rrfK, limit), Sources: []string{"fts", "vector"}}, nil
}

func fuseRRF(fts, vector []SearchResult, rrfK, limit int) []SearchResult {
	type candidate struct {
		result SearchResult
		seen   map[string]struct{}
	}
	candidates := make(map[string]*candidate, len(fts)+len(vector))
	add := func(items []SearchResult, source string) {
		for rank, item := range items {
			rank++
			key := item.ContentSHA256
			if key == "" {
				key = item.ChunkID.String()
			}
			entry := candidates[key]
			if entry == nil {
				copy := item
				entry = &candidate{result: copy, seen: make(map[string]struct{}, 2)}
				candidates[key] = entry
			}
			if _, exists := entry.seen[source]; exists {
				continue
			}
			entry.seen[source] = struct{}{}
			if source == "fts" {
				entry.result.FTSRank = rank
			} else {
				entry.result.VectorRank = rank
			}
			entry.result.FusedScore += 1 / float64(rrfK+rank)
		}
	}
	add(fts, "fts")
	add(vector, "vector")
	results := make([]SearchResult, 0, len(candidates))
	for _, entry := range candidates {
		results = append(results, entry.result)
	}
	sort.SliceStable(results, func(left, right int) bool {
		if results[left].FusedScore != results[right].FusedScore {
			return results[left].FusedScore > results[right].FusedScore
		}
		return results[left].ChunkID.String() < results[right].ChunkID.String()
	})
	return limitResults(results, limit)
}

func limitResults(results []SearchResult, limit int) []SearchResult {
	if len(results) > limit {
		results = results[:limit]
	}
	return append([]SearchResult(nil), results...)
}
