package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type rerankerStub struct {
	request RerankRequest
	result  RerankResult
	err     error
	calls   int
}

type contextExpanderStub struct {
	hits     []SearchResult
	window   int
	maxRunes int
	groups   []SearchContextGroup
	err      error
}

type queryRewriterStub struct {
	query  string
	result QueryRewriteResult
	err    error
	calls  int
}

type embedderFunc func(context.Context, EmbeddingRequest) (EmbeddingResult, error)

func (f embedderFunc) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	return f(ctx, request)
}

func (s *queryRewriterStub) Rewrite(_ context.Context, query string) (QueryRewriteResult, error) {
	s.calls++
	s.query = query
	return s.result, s.err
}

func (s *contextExpanderStub) ExpandContext(
	_ context.Context, _ uuid.UUID, hits []SearchResult, window, maxRunes int,
) ([]SearchContextGroup, error) {
	s.hits = append([]SearchResult(nil), hits...)
	s.window, s.maxRunes = window, maxRunes
	return append([]SearchContextGroup(nil), s.groups...), s.err
}

func (s *rerankerStub) Rerank(_ context.Context, request RerankRequest) (RerankResult, error) {
	s.calls++
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
	var observed []resilience.DegradationEvent
	reranker := &rerankerStub{err: errors.New("provider unavailable")}
	service, err := NewSearchServiceWithOptions(
		repository, nil, EmbeddingProfile{}, 2, SearchServiceOptions{
			Reranker: reranker, RerankCandidateN: 2,
			RerankProvider: "dashscope", RerankModel: "gte-rerank-v2",
			DegradationObserver: resilience.ObserverFunc(func(event resilience.DegradationEvent) {
				observed = append(observed, event)
			}),
		},
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
	if len(result.Degradations) != 2 || len(observed) != 2 || reranker.calls != 1 {
		t.Fatalf("degradations=%+v observed=%+v", result.Degradations, observed)
	}
	event := result.Degradations[1]
	if event.Operation != "rerank" || event.Fallback != "retrieval_order" ||
		event.ReasonCode != "provider_error" || event.Provider != "dashscope" || event.Model != "gte-rerank-v2" {
		t.Fatalf("rerank degradation = %+v", event)
	}
}

func TestSearchServiceExpandsContextAfterFinalRanking(t *testing.T) {
	documentID, versionID := uuid.New(), uuid.New()
	firstID, secondID, neighborID := uuid.New(), uuid.New(), uuid.New()
	section := []string{"报工", "超时"}
	newResult := func(chunkID uuid.UUID, ordinal int, content string) SearchResult {
		return SearchResult{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "生产手册", Scope: ScopeGlobal, Ordinal: ordinal, ElementType: ElementText,
			SectionPath: append([]string(nil), section...), ContentText: content, ContentSHA256: SHA256Hex(content),
		}
	}
	first := newResult(firstID, 2, "检查连接池配置。")
	second := newResult(secondID, 4, "检查 ERP 接口状态。")
	neighborContent := "同时核对事务日志中的超时错误码。"
	expander := &contextExpanderStub{groups: []SearchContextGroup{{
		DocumentID: documentID, DocumentVersionID: versionID, SectionPath: section,
		HitChunkIDs: []uuid.UUID{firstID}, Chunks: []SearchContextChunk{{
			ChunkID: neighborID, Ordinal: 3, ElementType: ElementText,
			ContentText: neighborContent, ContentSHA256: SHA256Hex(neighborContent),
		}},
	}}}
	service, err := NewSearchServiceWithRerankerAndContext(
		&hybridRepositoryStub{fts: []SearchResult{first, second}}, nil, EmbeddingProfile{}, 2,
		nil, 0, expander, 1, 1800,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "报工超时", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ContextExpanded || len(result.ContextGroups) != 1 || len(expander.hits) != 1 ||
		expander.hits[0].ChunkID != firstID || expander.window != 1 || expander.maxRunes != 1800 {
		t.Fatalf("result=%+v expander=%+v", result, expander)
	}
}

func TestSearchServiceKeepsHitsWhenContextExpansionFails(t *testing.T) {
	hit := SearchResult{ChunkID: uuid.New(), ContentText: "命中内容"}
	expander := &contextExpanderStub{err: errors.New("context unavailable")}
	service, err := NewSearchServiceWithRerankerAndContext(
		&hybridRepositoryStub{fts: []SearchResult{hit}}, nil, EmbeddingProfile{}, 2,
		nil, 0, expander, 1, 1800,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "报工超时", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].ChunkID != hit.ChunkID || !result.Degraded ||
		len(result.MissingChannels) != 2 || result.MissingChannels[1] != "context" || result.ContextExpanded {
		t.Fatalf("result=%+v", result)
	}
}

func TestSearchServiceCompressesExpandedContextUnderGlobalBudget(t *testing.T) {
	documentID, versionID, hitID := uuid.New(), uuid.New(), uuid.New()
	hitContent := "ERP-504 无法报工"
	hit := SearchResult{
		DocumentID: documentID, DocumentVersionID: versionID, ChunkID: hitID,
		Title: "报工手册", Scope: ScopeGlobal, Ordinal: 2, ElementType: ElementText,
		ContentText: hitContent, ContentSHA256: SHA256Hex(hitContent),
	}
	relevantContent := "无法报工时检查 ERP-504 网关状态。"
	noiseContent := "访客登记流程。"
	expander := &contextExpanderStub{groups: []SearchContextGroup{{
		DocumentID: documentID, DocumentVersionID: versionID, HitChunkIDs: []uuid.UUID{hitID},
		Chunks: []SearchContextChunk{
			{ChunkID: uuid.New(), Ordinal: 1, ElementType: ElementText, ContentText: noiseContent, ContentSHA256: SHA256Hex(noiseContent)},
			{ChunkID: uuid.New(), Ordinal: 3, ElementType: ElementText, ContentText: relevantContent, ContentSHA256: SHA256Hex(relevantContent)},
		},
	}}}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{fts: []SearchResult{hit}}, nil, EmbeddingProfile{}, 2,
		SearchServiceOptions{
			ContextExpander: expander, ContextWindow: 1, ContextMaxRunes: 1800,
			ContextCompression: ContextCompressionConfig{Enabled: true, MaxChunks: 1, MaxRunes: 128, MinScore: 0.05},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "ERP-504 无法报工", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ContextCompressionEnabled || !result.ContextCompressionApplied ||
		result.ContextCompression.InputChunks != 2 || result.ContextCompression.OutputChunks != 1 ||
		result.ContextCompression.OmittedChunks != 1 || len(result.ContextGroups) != 1 ||
		len(result.ContextGroups[0].Chunks) != 1 || result.ContextGroups[0].Chunks[0].ContentText != relevantContent {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchServicePropagatesContextCancellation(t *testing.T) {
	service, err := NewSearchServiceWithRerankerAndContext(
		&hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "命中内容"}}},
		nil, EmbeddingProfile{}, 2, nil, 0, &contextExpanderStub{err: context.Canceled}, 1, 1800,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), uuid.New(), "报工超时", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
}

func TestSearchServiceUsesValidatedQueryPlan(t *testing.T) {
	rewriter := &queryRewriterStub{result: QueryRewriteResult{
		LexicalQuery: "connection pool", SemanticQuery: "database connection pool exhaustion",
		Subqueries: []string{"pool exhaustion cause"}, PromptVersion: "query-rewrite-v1",
		Usage: QueryRewriteUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}
	repository := &hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "命中"}}}
	service, err := NewSearchServiceWithOptions(
		repository, nil, EmbeddingProfile{}, 5,
		SearchServiceOptions{QueryRewriter: rewriter, MaxSubqueries: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "pool timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryRewriteStatus != QueryRewriteAccepted || !result.QueryPlan.RewriteApplied ||
		result.QueryRewriteUsage.TotalTokens != 15 || result.QueryPlan.Usage.TotalTokens != 15 ||
		rewriter.query != "pool timeout" || len(repository.ftsQueries) != 3 {
		t.Fatalf("result=%+v queries=%v", result, repository.ftsQueries)
	}
}

func TestSearchServiceRejectsUnsafeRewriteAndKeepsOriginalQuery(t *testing.T) {
	rewriter := &queryRewriterStub{result: QueryRewriteResult{
		LexicalQuery: "ERP timeout", SemanticQuery: "ERP timeout", PromptVersion: "query-rewrite-v1",
	}}
	repository := &hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "命中"}}}
	service, err := NewSearchServiceWithOptions(
		repository, nil, EmbeddingProfile{}, 5,
		SearchServiceOptions{QueryRewriter: rewriter, MaxSubqueries: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "ERP-504 timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryRewriteStatus != QueryRewritePolicyRejected || result.QueryPlan.RewriteAttempted || result.QueryPlan.RewriteApplied ||
		result.QueryRewriteUsage != (QueryRewriteUsage{}) ||
		result.QueryPlan.OriginalQuery != "ERP-504 timeout" || !result.Degraded ||
		len(result.MissingChannels) != 2 || result.MissingChannels[1] != "query_rewrite" ||
		len(repository.ftsQueries) != 1 || repository.ftsQueries[0] != "ERP-504 timeout" {
		t.Fatalf("result=%+v queries=%v", result, repository.ftsQueries)
	}
}

func TestSearchServiceFallsBackWhenQueryRewriterTimesOutInternally(t *testing.T) {
	rewriter := &queryRewriterStub{err: context.DeadlineExceeded}
	repository := &hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "命中"}}}
	service, err := NewSearchServiceWithOptions(
		repository, nil, EmbeddingProfile{}, 5,
		SearchServiceOptions{QueryRewriter: rewriter, MaxSubqueries: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "connection timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryRewriteStatus != QueryRewriteProviderFailed || result.QueryPlan.RewriteAttempted || !result.Degraded ||
		len(repository.ftsQueries) != 1 || repository.ftsQueries[0] != "connection timeout" || rewriter.calls != 1 ||
		len(result.Degradations) != 2 || result.Degradations[0].ReasonCode != "timeout" {
		t.Fatalf("result=%+v queries=%v", result, repository.ftsQueries)
	}
}

func TestSearchServiceReportsOneQueryRewriteDegradationThroughPublicResult(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	var observed []resilience.DegradationEvent
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "命中"}}},
		&recordingEmbedder{}, profile, 5,
		SearchServiceOptions{
			QueryRewriter: &queryRewriterStub{err: errors.New("provider unavailable")}, MaxSubqueries: 2,
			QueryRewriteProvider: "dashscope", QueryRewriteModel: "qwen-flash",
			DegradationObserver: resilience.ObserverFunc(func(event resilience.DegradationEvent) {
				observed = append(observed, event)
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := resilience.WithRunIdentity(context.Background(), resilience.RunIdentity{RunID: "turn-42", TraceID: "trace-7"})
	result, err := service.Search(ctx, uuid.New(), "connection timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Degradations) != 1 || len(observed) != 1 || result.Degradations[0] != observed[0] {
		t.Fatalf("result events=%+v observed=%+v", result.Degradations, observed)
	}
	event := result.Degradations[0]
	if event.Operation != "query_rewrite" || event.Policy != resilience.PolicyBestEffort ||
		event.Fallback != "original_query" || event.ReasonCode != "provider_error" ||
		event.RunID != "turn-42" || event.TraceID != "trace-7" ||
		event.Provider != "dashscope" || event.Model != "qwen-flash" || event.DurationMillis < 0 {
		t.Fatalf("event = %+v", event)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "retrieval.knowledge_search" {
		t.Fatalf("unexpected Retrieval spans: %#v", spans)
	}
	if got := retrievalAttribute(spans[0], "mesguard.retrieval.result_count"); got != "1" {
		t.Fatalf("retrieval result count = %q, want 1", got)
	}
}

func retrievalAttribute(span tracetest.SpanStub, key string) string {
	for _, item := range span.Attributes {
		if string(item.Key) == key {
			return item.Value.Emit()
		}
	}
	return ""
}

func TestSearchServiceDoesNotReportDegradationForNormalNoMatch(t *testing.T) {
	var observed []resilience.DegradationEvent
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{}, &recordingEmbedder{}, profile, 5,
		SearchServiceOptions{DegradationObserver: resilience.ObserverFunc(func(event resilience.DegradationEvent) {
			observed = append(observed, event)
		})},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "no matching handbook", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 0 || result.Degraded || len(result.Degradations) != 0 || len(observed) != 0 {
		t.Fatalf("result=%+v observed=%+v", result, observed)
	}
}

func TestSearchServiceReportsEmbeddingFailureWhenFallingBackToFTS(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	var observed []resilience.DegradationEvent
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "FTS 命中"}}},
		failingEmbedder{}, profile, 5,
		SearchServiceOptions{DegradationObserver: resilience.ObserverFunc(func(event resilience.DegradationEvent) {
			observed = append(observed, event)
		})},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "connection timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || len(result.Degradations) != 1 || len(observed) != 1 {
		t.Fatalf("result=%+v observed=%+v", result, observed)
	}
	event := result.Degradations[0]
	if event.Operation != "vector_retrieval" || event.Fallback != "fts" ||
		event.ReasonCode != "provider_error" || event.Provider != profile.Provider || event.Model != profile.Model {
		t.Fatalf("event = %+v", event)
	}
}

func TestSearchServiceTreatsInternalEmbeddingTimeoutAsChannelDegradation(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "FTS 命中"}}},
		embedderFunc(func(context.Context, EmbeddingRequest) (EmbeddingResult, error) {
			return EmbeddingResult{}, context.DeadlineExceeded
		}),
		profile, 5, SearchServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "connection timeout", 2)
	if err != nil || len(result.Results) != 1 || len(result.Degradations) != 1 ||
		result.Degradations[0].Fallback != "fts" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSearchServicePropagatesCallerCancellationWithoutFallback(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{}, &recordingEmbedder{}, profile, 5, SearchServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.Search(ctx, uuid.New(), "connection timeout", 2)
	if !errors.Is(err, context.Canceled) || len(result.Degradations) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSearchServiceTreatsInternalRerankTimeoutAsBestEffort(t *testing.T) {
	repository := &hybridRepositoryStub{fts: []SearchResult{{ChunkID: uuid.New(), ContentText: "FTS 命中"}}}
	service, err := NewSearchServiceWithOptions(
		repository, nil, EmbeddingProfile{}, 2,
		SearchServiceOptions{Reranker: &rerankerStub{err: context.DeadlineExceeded}, RerankCandidateN: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "connection timeout", 1)
	if err != nil || len(result.Results) != 1 || len(result.Degradations) != 2 ||
		result.Degradations[1].Operation != "rerank" || result.Degradations[1].ReasonCode != "timeout" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSearchServiceDistinguishesAllChannelFailureFromNoMatch(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchServiceWithOptions(
		&hybridRepositoryStub{ftsErr: errors.New("postgres unavailable")}, failingEmbedder{}, profile, 5,
		SearchServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), uuid.New(), "connection timeout", 2)
	var operationErr *resilience.OperationError
	if !errors.As(err, &operationErr) || operationErr.Operation != "knowledge_retrieval" ||
		operationErr.Policy != resilience.PolicyBestEffort || operationErr.ReasonCode != "all_channels_failed" {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchServiceReportsFTSFailureWhenFallingBackToVector(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	vectorHit := SearchResult{ChunkID: uuid.New(), ContentText: "Vector 命中"}
	repository := &hybridRepositoryStub{
		ftsErr: errors.New("postgres FTS unavailable"), vectorResults: [][]SearchResult{{vectorHit}},
	}
	service, err := NewSearchServiceWithOptions(repository, &recordingEmbedder{}, profile, 5, SearchServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), uuid.New(), "connection timeout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].ChunkID != vectorHit.ChunkID || len(result.Degradations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	event := result.Degradations[0]
	if event.Operation != "fts_retrieval" || event.Fallback != "vector" || event.ReasonCode != "repository_error" {
		t.Fatalf("event = %+v", event)
	}
}

func TestSearchServiceReportsPartialChannelFallbacksExactlyOnce(t *testing.T) {
	profile, err := NewEmbeddingProfile(
		"knowledge-test", "dashscope", "text-embedding-v4", 2, "cosine",
		EmbeddingInputQuery, EmbeddingInputDocument, true, "v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	rewrite := QueryRewriteResult{
		LexicalQuery: "connection pool", SemanticQuery: "database connection pool exhaustion",
		Subqueries: []string{"pool exhaustion cause"}, PromptVersion: "query-rewrite-v1",
	}
	hit := SearchResult{ChunkID: uuid.New(), ContentText: "命中", ContentSHA256: SHA256Hex("命中")}
	tests := []struct {
		name       string
		repository *hybridRepositoryStub
		channel    string
	}{
		{
			name: "fts partial", channel: "fts_partial",
			repository: &hybridRepositoryStub{
				ftsByQuery:    map[string][]SearchResult{"pool timeout": {hit}, "connection pool": {hit}},
				ftsErrByQuery: map[string]error{"pool exhaustion cause": errors.New("fts subquery failed")},
				vectorResults: [][]SearchResult{{hit}, {hit}, {hit}},
			},
		},
		{
			name: "vector partial", channel: "vector_partial",
			repository: &hybridRepositoryStub{
				ftsByQuery: map[string][]SearchResult{
					"pool timeout": {hit}, "connection pool": {hit}, "pool exhaustion cause": {hit},
				},
				vectorResults: [][]SearchResult{{hit}, nil, {hit}},
				vectorErrors:  []error{nil, errors.New("vector subquery failed"), nil},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, serviceErr := NewSearchServiceWithOptions(
				test.repository, &recordingEmbedder{}, profile, 5,
				SearchServiceOptions{
					QueryRewriter: &queryRewriterStub{result: rewrite}, MaxSubqueries: 2,
					QueryRewriteProvider: "dashscope", QueryRewriteModel: "qwen-flash",
				},
			)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			result, searchErr := service.Search(context.Background(), uuid.New(), "pool timeout", 2)
			if searchErr != nil {
				t.Fatal(searchErr)
			}
			if len(result.Degradations) != 1 || result.MissingChannels[0] != test.channel {
				t.Fatalf("result = %+v", result)
			}
			event := result.Degradations[0]
			if event.Fallback != "available_results" || event.ReasonCode != "partial_failure" {
				t.Fatalf("event = %+v", event)
			}
		})
	}
}
