package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/resilience"

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
	repository           Repository
	retriever            *HybridRetriever
	reranker             Reranker
	rerankCandidateN     int
	contextExpander      ContextExpander
	contextWindow        int
	contextMaxRunes      int
	contextCompression   ContextCompressionConfig
	queryRewriter        QueryRewriter
	maxSubqueries        int
	queryRewriteProvider string
	queryRewriteModel    string
	rerankProvider       string
	rerankModel          string
	embeddingProvider    string
	embeddingModel       string
	degradationObserver  resilience.Observer
}

type SearchServiceOptions struct {
	Reranker             Reranker
	RerankCandidateN     int
	ContextExpander      ContextExpander
	ContextWindow        int
	ContextMaxRunes      int
	ContextCompression   ContextCompressionConfig
	QueryRewriter        QueryRewriter
	MaxSubqueries        int
	QueryRewriteProvider string
	QueryRewriteModel    string
	RerankProvider       string
	RerankModel          string
	DegradationObserver  resilience.Observer
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
	if err := options.ContextCompression.Validate(); err != nil {
		return nil, err
	}
	if options.ContextCompression.Enabled && options.ContextExpander == nil {
		return nil, errors.New("knowledge context compression requires context expansion")
	}
	if options.QueryRewriter != nil && (options.MaxSubqueries < 0 || options.MaxSubqueries > MaxQuerySubqueries) {
		return nil, errors.New("knowledge query rewrite config is invalid")
	}
	if (strings.TrimSpace(options.QueryRewriteProvider) == "") != (strings.TrimSpace(options.QueryRewriteModel) == "") ||
		options.QueryRewriteProvider != strings.TrimSpace(options.QueryRewriteProvider) ||
		options.QueryRewriteModel != strings.TrimSpace(options.QueryRewriteModel) ||
		len(options.QueryRewriteProvider) > 128 || len(options.QueryRewriteModel) > 128 {
		return nil, errors.New("knowledge query rewrite model identity is invalid")
	}
	if (strings.TrimSpace(options.RerankProvider) == "") != (strings.TrimSpace(options.RerankModel) == "") ||
		options.RerankProvider != strings.TrimSpace(options.RerankProvider) ||
		options.RerankModel != strings.TrimSpace(options.RerankModel) ||
		len(options.RerankProvider) > 128 || len(options.RerankModel) > 128 {
		return nil, errors.New("knowledge rerank model identity is invalid")
	}
	service := &SearchService{
		repository: repository, reranker: options.Reranker, rerankCandidateN: options.RerankCandidateN,
		contextExpander: options.ContextExpander, contextWindow: options.ContextWindow,
		contextMaxRunes: options.ContextMaxRunes, contextCompression: options.ContextCompression,
		queryRewriter:        options.QueryRewriter,
		maxSubqueries:        options.MaxSubqueries,
		queryRewriteProvider: options.QueryRewriteProvider,
		queryRewriteModel:    options.QueryRewriteModel,
		rerankProvider:       options.RerankProvider,
		rerankModel:          options.RerankModel,
		degradationObserver:  options.DegradationObserver,
	}
	if embedder != nil {
		retriever, err := NewHybridRetriever(repository, embedder, profile, vectorTopN)
		if err != nil {
			return nil, err
		}
		service.retriever = retriever
		service.embeddingProvider = profile.Provider
		service.embeddingModel = profile.Model
	}
	return service, nil
}

func (s *SearchService) Search(ctx context.Context, actorID uuid.UUID, query string, limit int) (HybridSearch, error) {
	if s == nil || s.repository == nil {
		return HybridSearch{}, errors.New("knowledge search service is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return HybridSearch{}, err
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
	if _, ok := resilience.RunIdentityFromContext(ctx); !ok {
		ctx = resilience.WithRunIdentity(ctx, resilience.RunIdentity{RunID: uuid.NewString()})
	}
	plan, err := OriginalQueryPlan(query)
	if err != nil {
		return HybridSearch{}, err
	}
	rewriteStatus := QueryRewriteDisabled
	var rewritePromptVersion string
	var rewriteUsage QueryRewriteUsage
	rewriteDegraded := false
	var result HybridSearch
	if s.queryRewriter != nil {
		rewriteStatus = QueryRewriteProviderFailed
		rewriteStartedAt := time.Now()
		rewrite, rewriteErr := s.queryRewriter.Rewrite(ctx, query)
		if rewriteErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return HybridSearch{}, ctxErr
			}
			rewriteDegraded = true
			if eventErr := s.appendDegradation(ctx, &result, resilience.DegradationEvent{
				Operation: "query_rewrite", Policy: resilience.PolicyBestEffort,
				Fallback: "original_query", ReasonCode: providerFailureReason(rewriteErr),
				Provider: s.queryRewriteProvider, Model: s.queryRewriteModel,
				DurationMillis: elapsedMillis(rewriteStartedAt),
			}); eventErr != nil {
				return HybridSearch{}, eventErr
			}
		} else {
			rewritePromptVersion = rewrite.PromptVersion
			rewriteUsage = rewrite.Usage
			if rewrittenPlan, planErr := BuildQueryPlan(query, rewrite, s.maxSubqueries); planErr != nil {
				rewriteStatus = QueryRewritePolicyRejected
				rewriteDegraded = true
				if eventErr := s.appendDegradation(ctx, &result, resilience.DegradationEvent{
					Operation: "query_rewrite", Policy: resilience.PolicyBestEffort,
					Fallback: "original_query", ReasonCode: "policy_rejected",
					Provider: s.queryRewriteProvider, Model: s.queryRewriteModel,
					DurationMillis: elapsedMillis(rewriteStartedAt),
				}); eventErr != nil {
					return HybridSearch{}, eventErr
				}
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
	rewriteDegradations := append([]resilience.DegradationEvent(nil), result.Degradations...)
	retrievalStartedAt := time.Now()
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
	result.Degradations = append(rewriteDegradations, result.Degradations...)
	if err != nil {
		return result, err
	}
	if eventErr := s.appendRetrievalDegradations(ctx, &result, elapsedMillis(retrievalStartedAt)); eventErr != nil {
		return result, eventErr
	}
	result.QueryPlan = plan
	result.QueryRewriteStatus = rewriteStatus
	result.QueryRewritePromptVersion = rewritePromptVersion
	result.QueryRewriteUsage = rewriteUsage
	result.ContextCompressionEnabled = s.contextCompression.Enabled
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
	rerankStartedAt := time.Now()
	reranked, usage, err := s.rerank(ctx, query, result.Results, limit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		result.Degraded = true
		result.MissingChannels = appendMissingChannel(result.MissingChannels, "rerank")
		if eventErr := s.appendDegradation(ctx, &result, resilience.DegradationEvent{
			Operation: "rerank", Policy: resilience.PolicyBestEffort,
			Fallback: "retrieval_order", ReasonCode: providerFailureReason(err),
			Provider: s.rerankProvider, Model: s.rerankModel,
			DurationMillis: elapsedMillis(rerankStartedAt),
		}); eventErr != nil {
			return result, eventErr
		}
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

func (s *SearchService) appendRetrievalDegradations(ctx context.Context, result *HybridSearch, durationMillis int64) error {
	for _, channel := range result.MissingChannels {
		switch channel {
		case "vector":
			reason := "dependency_unavailable"
			if s.retriever != nil {
				reason = "provider_error"
			}
			if err := s.appendDegradation(ctx, result, resilience.DegradationEvent{
				Operation: "vector_retrieval", Policy: resilience.PolicyBestEffort,
				Fallback: "fts", ReasonCode: reason,
				Provider: s.embeddingProvider, Model: s.embeddingModel, DurationMillis: durationMillis,
			}); err != nil {
				return err
			}
		case "fts":
			if err := s.appendDegradation(ctx, result, resilience.DegradationEvent{
				Operation: "fts_retrieval", Policy: resilience.PolicyBestEffort,
				Fallback: "vector", ReasonCode: "repository_error", DurationMillis: durationMillis,
			}); err != nil {
				return err
			}
		case "fts_partial":
			if err := s.appendDegradation(ctx, result, resilience.DegradationEvent{
				Operation: "fts_retrieval", Policy: resilience.PolicyBestEffort,
				Fallback: "available_results", ReasonCode: "partial_failure", DurationMillis: durationMillis,
			}); err != nil {
				return err
			}
		case "vector_partial":
			if err := s.appendDegradation(ctx, result, resilience.DegradationEvent{
				Operation: "vector_retrieval", Policy: resilience.PolicyBestEffort,
				Fallback: "available_results", ReasonCode: "partial_failure",
				Provider: s.embeddingProvider, Model: s.embeddingModel, DurationMillis: durationMillis,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SearchService) appendDegradation(ctx context.Context, result *HybridSearch, event resilience.DegradationEvent) error {
	identity, ok := resilience.RunIdentityFromContext(ctx)
	if !ok {
		identity.RunID = uuid.NewString()
	}
	event.RunID = identity.RunID
	event.TraceID = identity.TraceID
	if err := event.Validate(); err != nil {
		return fmt.Errorf("build knowledge degradation event: %w", err)
	}
	result.Degradations = append(result.Degradations, event)
	if s.degradationObserver != nil {
		s.degradationObserver.ObserveDegradation(event)
	}
	return nil
}

func elapsedMillis(startedAt time.Time) int64 {
	duration := time.Since(startedAt).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func providerFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "provider_cancelled"
	default:
		return "provider_error"
	}
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
	if s.contextCompression.Enabled && len(groups) > 0 {
		compressed, stats, compressionErr := CompressSearchContext(
			result.QueryPlan, result.Results, groups, s.contextCompression,
		)
		if compressionErr != nil {
			result.Degraded = true
			result.MissingChannels = appendMissingChannel(result.MissingChannels, "context_compression")
		} else {
			groups = compressed
			result.ContextCompressionApplied = true
			result.ContextCompression = stats
		}
	}
	result.ContextGroups = cloneSearchContextGroups(groups)
	result.ContextExpanded = len(result.ContextGroups) > 0
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
