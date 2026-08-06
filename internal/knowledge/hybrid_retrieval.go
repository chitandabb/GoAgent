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
	Results                   []SearchResult
	ContextGroups             []SearchContextGroup
	QueryPlan                 QueryPlan
	Degraded                  bool
	Sources                   []string
	MissingChannels           []string
	RerankApplied             bool
	RerankUsage               RerankUsage
	ContextExpanded           bool
	QueryRewriteStatus        QueryRewriteStatus
	QueryRewritePromptVersion string
	QueryRewriteUsage         QueryRewriteUsage
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
	plan, err := OriginalQueryPlan(query)
	if err != nil {
		return HybridSearch{}, err
	}
	return r.SearchPlan(ctx, actorID, plan, limit)
}

func (r *HybridRetriever) SearchPlan(ctx context.Context, actorID uuid.UUID, plan QueryPlan, limit int) (HybridSearch, error) {
	if r == nil || r.repository == nil || r.embedder == nil {
		return HybridSearch{}, errors.New("hybrid retriever is unavailable")
	}
	if actorID == uuid.Nil || limit < 1 || limit > 50 {
		return HybridSearch{}, errors.New("hybrid retrieval request is invalid")
	}
	if err := plan.Validate(); err != nil {
		return HybridSearch{}, err
	}
	var (
		ftsResults    []SearchResult
		ftsErr        error
		ftsPartial    bool
		vectorResults []SearchResult
		vectorErr     error
		vectorPartial bool
	)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		ftsResults, ftsPartial, ftsErr = searchFTSQueries(
			ctx, r.repository, actorID, plan.FTSQueries(), r.vectorTopN,
		)
	}()
	go func() {
		defer group.Done()
		vectorResults, vectorPartial, vectorErr = r.searchVectorQueries(ctx, actorID, plan.VectorQueries())
	}()
	group.Wait()

	if errors.Is(ftsErr, context.Canceled) || errors.Is(ftsErr, context.DeadlineExceeded) {
		return HybridSearch{}, ftsErr
	}
	if errors.Is(vectorErr, context.Canceled) || errors.Is(vectorErr, context.DeadlineExceeded) {
		return HybridSearch{}, vectorErr
	}
	if ftsErr != nil && vectorErr != nil {
		return HybridSearch{}, fmt.Errorf("hybrid retrieval failed: fts=%v vector=%v", ftsErr, vectorErr)
	}
	if ftsErr != nil {
		return HybridSearch{
			Results: limitResults(vectorResults, limit), Degraded: true,
			Sources: []string{"vector"}, MissingChannels: []string{"fts"},
		}, nil
	}
	if vectorErr != nil {
		return HybridSearch{
			Results: limitResults(ftsResults, limit), Degraded: true,
			Sources: []string{"fts"}, MissingChannels: []string{"vector"},
		}, nil
	}
	result := HybridSearch{
		Results: fuseRRF(ftsResults, vectorResults, r.rrfK, limit), Sources: []string{"fts", "vector"},
	}
	if ftsPartial {
		result.Degraded = true
		result.MissingChannels = append(result.MissingChannels, "fts_partial")
	}
	if vectorPartial {
		result.Degraded = true
		result.MissingChannels = append(result.MissingChannels, "vector_partial")
	}
	return result, nil
}

func searchFTSQueries(
	ctx context.Context, repository Repository, actorID uuid.UUID, queries []string, limit int,
) ([]SearchResult, bool, error) {
	if len(queries) == 0 {
		return nil, false, errors.New("knowledge FTS query plan is empty")
	}
	groups := make([][]SearchResult, 0, len(queries))
	var failures []error
	for _, query := range queries {
		results, err := repository.SearchFTS(ctx, actorID, query, limit)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, false, err
			}
			failures = append(failures, err)
			continue
		}
		groups = append(groups, results)
	}
	if len(groups) == 0 {
		return nil, false, errors.Join(failures...)
	}
	return mergeQueryResults(groups, limit), len(failures) > 0, nil
}

func (r *HybridRetriever) searchVectorQueries(
	ctx context.Context, actorID uuid.UUID, queries []string,
) ([]SearchResult, bool, error) {
	if len(queries) == 0 {
		return nil, false, errors.New("knowledge vector query plan is empty")
	}
	embedding, err := r.embedder.Embed(ctx, EmbeddingRequest{Texts: queries, InputType: r.profile.QueryInputType})
	if err != nil {
		return nil, false, err
	}
	if err := embedding.Validate(len(queries), r.profile.Dimensions, r.profile.Normalize); err != nil {
		return nil, false, err
	}
	groups := make([][]SearchResult, 0, len(queries))
	var failures []error
	for index := range queries {
		results, err := r.repository.SearchVector(
			ctx, actorID, r.profile.ID, embedding.Vectors[index], r.vectorTopN,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, false, err
			}
			failures = append(failures, err)
			continue
		}
		groups = append(groups, results)
	}
	if len(groups) == 0 {
		return nil, false, errors.Join(failures...)
	}
	return mergeQueryResults(groups, r.vectorTopN), len(failures) > 0, nil
}

func mergeQueryResults(groups [][]SearchResult, limit int) []SearchResult {
	type rankedResult struct {
		result     SearchResult
		bestRank   int
		queryIndex int
	}
	merged := make(map[string]rankedResult)
	for queryIndex, results := range groups {
		for index, result := range results {
			rank := index + 1
			key := result.ContentSHA256
			if key == "" {
				key = result.ChunkID.String()
			}
			current, exists := merged[key]
			if !exists || rank < current.bestRank ||
				(rank == current.bestRank && (queryIndex < current.queryIndex ||
					(queryIndex == current.queryIndex && (result.Score > current.result.Score ||
						(result.Score == current.result.Score && result.ChunkID.String() < current.result.ChunkID.String()))))) {
				merged[key] = rankedResult{result: result, bestRank: rank, queryIndex: queryIndex}
			}
		}
	}
	ranked := make([]rankedResult, 0, len(merged))
	for _, result := range merged {
		ranked = append(ranked, result)
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].bestRank != ranked[right].bestRank {
			return ranked[left].bestRank < ranked[right].bestRank
		}
		if ranked[left].queryIndex != ranked[right].queryIndex {
			return ranked[left].queryIndex < ranked[right].queryIndex
		}
		if ranked[left].result.Score != ranked[right].result.Score {
			return ranked[left].result.Score > ranked[right].result.Score
		}
		return ranked[left].result.ChunkID.String() < ranked[right].result.ChunkID.String()
	})
	results := make([]SearchResult, 0, len(ranked))
	for _, result := range ranked {
		results = append(results, result.result)
	}
	return limitResults(results, limit)
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
