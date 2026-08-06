package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
)

const (
	DefaultKnowledgeSearchLimit  = 8
	MaxKnowledgeSearchLimit      = 20
	MaxKnowledgeSearchQueryRunes = 4096
)

type RerankDocument struct {
	Content string
}

type RerankRequest struct {
	Query     string
	Documents []RerankDocument
	TopN      int
}

type RerankItem struct {
	Index          int
	RelevanceScore float64
}

type RerankUsage struct {
	TotalTokens int
}

type RerankResult struct {
	Items []RerankItem
	Usage RerankUsage
}

func (r RerankRequest) Validate(maxDocuments int) error {
	if maxDocuments < 1 || maxDocuments > 50 || strings.TrimSpace(r.Query) == "" ||
		r.Query != strings.TrimSpace(r.Query) || len([]rune(r.Query)) > MaxKnowledgeSearchQueryRunes {
		return errors.New("rerank request query is invalid")
	}
	if len(r.Documents) < 1 || len(r.Documents) > maxDocuments || r.TopN < 1 || r.TopN > len(r.Documents) {
		return errors.New("rerank request documents or topN is invalid")
	}
	for _, document := range r.Documents {
		if strings.TrimSpace(document.Content) == "" || document.Content != strings.TrimSpace(document.Content) ||
			strings.ContainsRune(document.Content, 0) || len([]rune(document.Content)) > 32_000 {
			return errors.New("rerank request document is invalid")
		}
	}
	return nil
}

func (r RerankResult) Validate(documentCount, topN int) error {
	if documentCount < 1 || topN < 1 || topN > documentCount || len(r.Items) < 1 || len(r.Items) > topN || r.Usage.TotalTokens < 0 {
		return errors.New("rerank result dimensions are invalid")
	}
	seen := make(map[int]struct{}, len(r.Items))
	for _, item := range r.Items {
		if item.Index < 0 || item.Index >= documentCount || math.IsNaN(item.RelevanceScore) || math.IsInf(item.RelevanceScore, 0) {
			return errors.New("rerank result item is invalid")
		}
		if _, exists := seen[item.Index]; exists {
			return errors.New("rerank result contains duplicate index")
		}
		seen[item.Index] = struct{}{}
	}
	return nil
}

type Reranker interface {
	Rerank(context.Context, RerankRequest) (RerankResult, error)
}

// SearchService 是高层知识检索边界。调用方不需要知道 FTS、Vector 或 RRF 的选择。
// Embedding 不可用时保留 FTS 结果并显式标记降级。
type SearchService struct {
	repository       Repository
	retriever        *HybridRetriever
	reranker         Reranker
	rerankCandidateN int
	contextExpander  ContextExpander
	contextWindow    int
	contextMaxRunes  int
	queryRewriter    QueryRewriter
	maxSubqueries    int
}

type SearchServiceOptions struct {
	Reranker         Reranker
	RerankCandidateN int
	ContextExpander  ContextExpander
	ContextWindow    int
	ContextMaxRunes  int
	QueryRewriter    QueryRewriter
	MaxSubqueries    int
}

func NewSearchService(repository Repository, embedder Embedder, profile EmbeddingProfile, vectorTopN int) (*SearchService, error) {
	return NewSearchServiceWithReranker(repository, embedder, profile, vectorTopN, nil, 0)
}

func NewSearchServiceWithReranker(
	repository Repository, embedder Embedder, profile EmbeddingProfile, vectorTopN int,
	reranker Reranker, rerankCandidateN int,
) (*SearchService, error) {
	return NewSearchServiceWithOptions(repository, embedder, profile, vectorTopN, SearchServiceOptions{
		Reranker: reranker, RerankCandidateN: rerankCandidateN,
	})
}

func NewSearchServiceWithRerankerAndContext(
	repository Repository, embedder Embedder, profile EmbeddingProfile, vectorTopN int,
	reranker Reranker, rerankCandidateN int,
	contextExpander ContextExpander, contextWindow, contextMaxRunes int,
) (*SearchService, error) {
	return NewSearchServiceWithOptions(repository, embedder, profile, vectorTopN, SearchServiceOptions{
		Reranker: reranker, RerankCandidateN: rerankCandidateN,
		ContextExpander: contextExpander, ContextWindow: contextWindow, ContextMaxRunes: contextMaxRunes,
	})
}

func NewSearchServiceWithOptions(
	repository Repository, embedder Embedder, profile EmbeddingProfile, vectorTopN int,
	options SearchServiceOptions,
) (*SearchService, error) {
	if repository == nil {
		return nil, errors.New("knowledge search repository is required")
	}
	if options.Reranker != nil && (options.RerankCandidateN < 1 || options.RerankCandidateN > 50) {
		return nil, errors.New("knowledge rerank candidate limit is invalid")
	}
	if options.ContextExpander != nil && (options.ContextWindow < 1 || options.ContextWindow > 3 ||
		options.ContextMaxRunes < 128 || options.ContextMaxRunes > 8000) {
		return nil, errors.New("knowledge context expansion config is invalid")
	}
	if options.QueryRewriter != nil && (options.MaxSubqueries < 0 || options.MaxSubqueries > MaxQuerySubqueries) {
		return nil, errors.New("knowledge query rewrite config is invalid")
	}
	service := &SearchService{
		repository: repository, reranker: options.Reranker, rerankCandidateN: options.RerankCandidateN,
		contextExpander: options.ContextExpander, contextWindow: options.ContextWindow,
		contextMaxRunes: options.ContextMaxRunes, queryRewriter: options.QueryRewriter,
		maxSubqueries: options.MaxSubqueries,
	}
	if embedder != nil {
		retriever, err := NewHybridRetriever(repository, embedder, profile, vectorTopN)
		if err != nil {
			return nil, err
		}
		service.retriever = retriever
	}
	return service, nil
}

func (s *SearchService) Search(ctx context.Context, actorID uuid.UUID, query string, limit int) (HybridSearch, error) {
	if s == nil || s.repository == nil {
		return HybridSearch{}, errors.New("knowledge search service is unavailable")
	}
	if actorID == uuid.Nil {
		return HybridSearch{}, errors.New("knowledge search actor is required")
	}
	if strings.TrimSpace(query) == "" || query != strings.TrimSpace(query) || len([]rune(query)) > MaxKnowledgeSearchQueryRunes {
		return HybridSearch{}, errors.New("knowledge search query is invalid")
	}
	if limit < 1 {
		limit = DefaultKnowledgeSearchLimit
	}
	if limit > MaxKnowledgeSearchLimit {
		limit = MaxKnowledgeSearchLimit
	}
	plan, err := OriginalQueryPlan(query)
	if err != nil {
		return HybridSearch{}, err
	}
	rewriteStatus := QueryRewriteDisabled
	var rewritePromptVersion string
	var rewriteUsage QueryRewriteUsage
	rewriteDegraded := false
	if s.queryRewriter != nil {
		rewriteStatus = QueryRewriteProviderFailed
		rewrite, rewriteErr := s.queryRewriter.Rewrite(ctx, query)
		if rewriteErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return HybridSearch{}, ctxErr
			}
			rewriteDegraded = true
		} else {
			rewritePromptVersion = rewrite.PromptVersion
			rewriteUsage = rewrite.Usage
			if rewrittenPlan, planErr := BuildQueryPlan(query, rewrite, s.maxSubqueries); planErr != nil {
				rewriteStatus = QueryRewritePolicyRejected
				rewriteDegraded = true
			} else {
				plan = rewrittenPlan
				rewriteStatus = QueryRewriteAccepted
			}
		}
	}
	candidateLimit := limit
	if s.reranker != nil && s.rerankCandidateN > candidateLimit {
		candidateLimit = s.rerankCandidateN
	}
	var result HybridSearch
	if s.retriever != nil {
		result, err = s.retriever.SearchPlan(ctx, actorID, plan, candidateLimit)
	} else {
		var results []SearchResult
		var partial bool
		results, partial, err = searchFTSQueries(ctx, s.repository, actorID, plan.FTSQueries(), candidateLimit)
		result = HybridSearch{
			Results: results, Degraded: true, Sources: []string{"fts"}, MissingChannels: []string{"vector"},
		}
		if partial {
			result.MissingChannels = appendMissingChannel(result.MissingChannels, "fts_partial")
		}
	}
	if err != nil {
		return result, err
	}
	result.QueryPlan = plan
	result.QueryRewriteStatus = rewriteStatus
	result.QueryRewritePromptVersion = rewritePromptVersion
	result.QueryRewriteUsage = rewriteUsage
	if rewriteDegraded {
		result.Degraded = true
		result.MissingChannels = appendMissingChannel(result.MissingChannels, "query_rewrite")
	}
	if s.reranker == nil || len(result.Results) == 0 {
		result.Results = limitResults(result.Results, limit)
		if err := s.expandContext(ctx, actorID, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	reranked, usage, err := s.rerank(ctx, query, result.Results, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}
		result.Degraded = true
		result.MissingChannels = appendMissingChannel(result.MissingChannels, "rerank")
		result.Results = limitResults(result.Results, limit)
		if err := s.expandContext(ctx, actorID, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	result.Results = reranked
	result.RerankApplied = true
	result.RerankUsage = usage
	if err := s.expandContext(ctx, actorID, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *SearchService) expandContext(ctx context.Context, actorID uuid.UUID, result *HybridSearch) error {
	if s.contextExpander == nil || len(result.Results) == 0 {
		return nil
	}
	groups, err := s.contextExpander.ExpandContext(
		ctx, actorID, result.Results, s.contextWindow, s.contextMaxRunes,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		result.Degraded = true
		result.MissingChannels = appendMissingChannel(result.MissingChannels, "context")
		return nil
	}
	for _, group := range groups {
		if err := group.Validate(result.Results); err != nil {
			result.Degraded = true
			result.MissingChannels = appendMissingChannel(result.MissingChannels, "context")
			return nil
		}
	}
	result.ContextGroups = append([]SearchContextGroup(nil), groups...)
	result.ContextExpanded = len(groups) > 0
	return nil
}

func (s *SearchService) rerank(
	ctx context.Context, query string, candidates []SearchResult, limit int,
) ([]SearchResult, RerankUsage, error) {
	documents := make([]RerankDocument, len(candidates))
	for index, candidate := range candidates {
		documents[index] = RerankDocument{Content: candidate.ContentText}
	}
	output, err := s.reranker.Rerank(ctx, RerankRequest{Query: query, Documents: documents, TopN: limit})
	if err != nil {
		return nil, RerankUsage{}, err
	}
	if len(output.Items) == 0 || len(output.Items) > limit {
		return nil, RerankUsage{}, errors.New("rerank result size is invalid")
	}
	seen := make(map[int]struct{}, len(output.Items))
	results := make([]SearchResult, 0, len(output.Items))
	for _, item := range output.Items {
		if item.Index < 0 || item.Index >= len(candidates) || math.IsNaN(item.RelevanceScore) || math.IsInf(item.RelevanceScore, 0) {
			return nil, RerankUsage{}, errors.New("rerank result item is invalid")
		}
		if _, exists := seen[item.Index]; exists {
			return nil, RerankUsage{}, errors.New("rerank result contains duplicate index")
		}
		seen[item.Index] = struct{}{}
		candidate := candidates[item.Index]
		candidate.Score = item.RelevanceScore
		candidate.FusedScore = item.RelevanceScore
		results = append(results, candidate)
	}
	if len(results) != limit && len(candidates) >= limit {
		return nil, RerankUsage{}, fmt.Errorf("rerank returned %d results, want %d", len(results), limit)
	}
	return results, output.Usage, nil
}

func appendMissingChannel(channels []string, channel string) []string {
	for _, value := range channels {
		if value == channel {
			return channels
		}
	}
	return append(channels, channel)
}
