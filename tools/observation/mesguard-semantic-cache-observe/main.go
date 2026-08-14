// Command mesguard-semantic-cache-observe measures pgvector lookup and the
// configured-provider semantic cache hit path against a fixed traffic replay.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const maxFixtureBytes = 1 << 20

type latencyFixture struct {
	SchemaVersion  string        `json:"schemaVersion"`
	DatasetVersion string        `json:"datasetVersion"`
	Repetitions    int           `json:"repetitions"`
	Pairs          []latencyPair `json:"pairs"`
}

type latencyPair struct {
	ID                string `json:"id"`
	AnchorQuestion    string `json:"anchorQuestion"`
	CandidateQuestion string `json:"candidateQuestion"`
}

type pairObservation struct {
	ID         string  `json:"id"`
	Similarity float64 `json:"similarity"`
}

type performanceReport struct {
	SchemaVersion       string            `json:"schemaVersion"`
	DatasetVersion      string            `json:"datasetVersion"`
	EmbeddingProfile    string            `json:"embeddingProfile"`
	Generation          int64             `json:"generation"`
	Threshold           float64           `json:"threshold"`
	PairObservations    []pairObservation `json:"pairObservations"`
	LookupSamples       int               `json:"lookupSamples"`
	LookupP50Millis     float64           `json:"lookupP50Millis"`
	LookupP95Millis     float64           `json:"lookupP95Millis"`
	FullChainSamples    int               `json:"fullChainSamples"`
	FullChainP50Millis  float64           `json:"fullChainP50Millis"`
	FullChainP95Millis  float64           `json:"fullChainP95Millis"`
	ProviderCalls       int               `json:"providerCalls"`
	EmbeddingTokens     int               `json:"embeddingTokens"`
	MainModelCalls      int               `json:"mainModelCalls"`
	ToolCalls           int               `json:"toolCalls"`
	DegradationEvents   int               `json:"degradationEvents"`
	DegradationRate     float64           `json:"degradationRate"`
	MeasuredAt          time.Time         `json:"measuredAt"`
	MeasurementBoundary string            `json:"measurementBoundary"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mesguard-semantic-cache-observe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fixturePath := flags.String("fixture", "", "versioned semantic cache latency fixture")
	outputPath := flags.String("output", "", "new JSON report; existing files are rejected")
	maxProviderCalls := flags.Int("max-provider-calls", 21, "hard Provider call limit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*fixturePath) == "" || strings.TrimSpace(*outputPath) == "" || *maxProviderCalls < 1 {
		fmt.Fprintln(stderr, "-fixture, -output, and a positive -max-provider-calls are required")
		return 2
	}
	fixture, err := readFixture(*fixturePath)
	if err != nil {
		fmt.Fprintf(stderr, "read latency fixture: %v\n", err)
		return 1
	}
	expectedCalls := 1 + fixture.Repetitions*len(fixture.Pairs)
	if expectedCalls > *maxProviderCalls {
		fmt.Fprintf(stderr, "cost guard: observation requires %d Provider calls, limit is %d\n", expectedCalls, *maxProviderCalls)
		return 1
	}
	report, err := observe(fixture)
	if err != nil {
		fmt.Fprintf(stderr, "observe semantic cache: %v\n", err)
		return 1
	}
	if report.ProviderCalls != expectedCalls {
		fmt.Fprintf(stderr, "Provider call count %d does not match expected %d\n", report.ProviderCalls, expectedCalls)
		return 1
	}
	if err := writeNewJSON(*outputPath, report); err != nil {
		fmt.Fprintf(stderr, "write observation report: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "semantic_cache_latency pairs=%d samples=%d p95_ms=%.3f provider_calls=%d embedding_tokens=%d\n",
		len(fixture.Pairs), report.FullChainSamples, report.FullChainP95Millis, report.ProviderCalls, report.EmbeddingTokens)
	return 0
}

func readFixture(path string) (latencyFixture, error) {
	var fixture latencyFixture
	data, err := os.ReadFile(path)
	if err != nil {
		return fixture, err
	}
	if len(data) == 0 || len(data) > maxFixtureBytes {
		return fixture, errors.New("latency fixture size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return fixture, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fixture, errors.New("latency fixture contains trailing data")
	}
	if fixture.SchemaVersion != "semantic_cache_latency_fixture_v1" || strings.TrimSpace(fixture.DatasetVersion) == "" ||
		fixture.Repetitions < 1 || fixture.Repetitions > 20 || len(fixture.Pairs) < 2 || len(fixture.Pairs) > 20 {
		return fixture, errors.New("latency fixture identity or bounds are invalid")
	}
	seen := make(map[string]struct{}, len(fixture.Pairs))
	for _, pair := range fixture.Pairs {
		if strings.TrimSpace(pair.ID) == "" || strings.TrimSpace(pair.AnchorQuestion) == "" || strings.TrimSpace(pair.CandidateQuestion) == "" ||
			!semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.AnchorQuestion}) ||
			!semanticcache.EligibleForLookup(semanticcache.Question{Text: pair.CandidateQuestion}) ||
			!semanticcache.CompareQuestions(pair.AnchorQuestion, pair.CandidateQuestion).Compatible {
			return fixture, errors.New("latency fixture pair is invalid")
		}
		if _, duplicate := seen[pair.ID]; duplicate {
			return fixture, errors.New("latency fixture pair is duplicated")
		}
		seen[pair.ID] = struct{}{}
	}
	return fixture, nil
}

func observe(fixture latencyFixture) (performanceReport, error) {
	cfg, err := config.Load()
	if err != nil {
		return performanceReport{}, err
	}
	if !cfg.SemanticAnswerCache.Enabled || !cfg.SemanticAnswerCache.SemanticEnabled {
		return performanceReport{}, errors.New("semantic answer cache L2 is disabled")
	}
	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return performanceReport{}, err
	}
	if profile.Fingerprint != cfg.SemanticAnswerCache.SemanticProfileFingerprint {
		return performanceReport{}, errors.New("calibrated profile does not match configured embedding profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, zap.NewNop())
	if err != nil {
		return performanceReport{}, err
	}
	defer func() { _ = closeDB() }()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return performanceReport{}, tx.Error
	}
	defer func() { _ = tx.Rollback().Error }()
	if err := platformpostgres.NewKnowledgeWorkerRepository(tx).EnsureEmbeddingProfile(ctx, profile); err != nil {
		return performanceReport{}, err
	}
	client, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
	if err != nil {
		return performanceReport{}, err
	}
	embedder := &meteredEmbedder{delegate: client}
	texts := make([]string, 0, len(fixture.Pairs)*2)
	for _, pair := range fixture.Pairs {
		texts = append(texts, pair.AnchorQuestion, pair.CandidateQuestion)
	}
	seed, err := embedder.Embed(ctx, knowledge.EmbeddingRequest{Texts: texts, InputType: profile.QueryInputType})
	if err != nil {
		return performanceReport{}, err
	}
	if err := seed.Validate(len(texts), profile.Dimensions, profile.Normalize); err != nil {
		return performanceReport{}, err
	}
	observations := make([]pairObservation, 0, len(fixture.Pairs))
	for index, pair := range fixture.Pairs {
		similarity := cosineSimilarity(seed.Vectors[index*2], seed.Vectors[index*2+1])
		if similarity < cfg.SemanticAnswerCache.SemanticMinimumSimilarity {
			return performanceReport{}, fmt.Errorf("pair %s similarity %.6f is below threshold", pair.ID, similarity)
		}
		observations = append(observations, pairObservation{ID: pair.ID, Similarity: similarity})
	}
	state, err := seedCacheFixture(ctx, tx, cfg, profile, fixture, seed.Vectors)
	if err != nil {
		return performanceReport{}, err
	}
	lookupDurations := make([]time.Duration, 0, 200)
	for index := range 200 {
		pairIndex := index % len(fixture.Pairs)
		startedAt := time.Now()
		_, hit, lookupErr := state.cache.LookupSemantic(ctx, semanticcache.SemanticLookupInput{
			Question: fixture.Pairs[pairIndex].CandidateQuestion, Vector: seed.Vectors[pairIndex*2+1],
			ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
			NormalizationVersion: semanticcache.SemanticNormalizationVersion,
			MinimumSimilarity:    cfg.SemanticAnswerCache.SemanticMinimumSimilarity,
			CandidateLimit:       cfg.SemanticAnswerCache.SemanticCandidateLimit, Now: time.Now().UTC(),
		})
		lookupDurations = append(lookupDurations, time.Since(startedAt))
		if lookupErr != nil {
			return performanceReport{}, fmt.Errorf("pgvector lookup %d: %w", index, lookupErr)
		}
		if !hit {
			return performanceReport{}, fmt.Errorf("pgvector lookup %d unexpectedly missed", index)
		}
	}
	degradations := 0
	if _, err := state.service.WithSemanticAnswerCache(state.cache, conversation.SemanticAnswerCacheConfig{
		TTL:            time.Duration(cfg.SemanticAnswerCache.TTLSeconds) * time.Second,
		LookupTimeout:  time.Duration(cfg.SemanticAnswerCache.LookupTimeoutMillis) * time.Millisecond,
		WriteTimeout:   time.Duration(cfg.SemanticAnswerCache.WriteTimeoutMillis) * time.Millisecond,
		MaxAnswerBytes: cfg.SemanticAnswerCache.MaxAnswerBytes, MaxCitations: cfg.SemanticAnswerCache.MaxCitations,
		Semantic: &conversation.SemanticAnswerCacheSemanticConfig{
			Embedder: embedder, Profile: profile,
			MinimumSimilarity: cfg.SemanticAnswerCache.SemanticMinimumSimilarity,
			CandidateLimit:    cfg.SemanticAnswerCache.SemanticCandidateLimit,
			EmbeddingTimeout:  time.Duration(cfg.SemanticAnswerCache.SemanticEmbeddingTimeoutMillis) * time.Millisecond,
		},
	}, resilience.ObserverFunc(func(resilience.DegradationEvent) { degradations++ })); err != nil {
		return performanceReport{}, err
	}
	requests := make([]turnRequest, 0, fixture.Repetitions*len(fixture.Pairs))
	for range fixture.Repetitions {
		for _, pair := range fixture.Pairs {
			created, createErr := state.service.Create(ctx, conversation.Actor{UserID: state.userID}, conversation.CreateInput{Title: "语义缓存固定流量回放"})
			if createErr != nil {
				return performanceReport{}, createErr
			}
			requests = append(requests, turnRequest{conversationID: created.ID, question: pair.CandidateQuestion})
		}
	}
	agentCallsBefore := state.agent.calls
	fullChainDurations := make([]time.Duration, 0, len(requests))
	for index, request := range requests {
		startedAt := time.Now()
		result, executeErr := state.service.ExecuteTurn(ctx, conversation.Actor{UserID: state.userID}, uuid.NewString(), conversation.AppendMessageInput{
			ConversationID: request.conversationID, Content: request.question,
		})
		fullChainDurations = append(fullChainDurations, time.Since(startedAt))
		if executeErr != nil {
			return performanceReport{}, fmt.Errorf("full-chain request %d: %w", index, executeErr)
		}
		if result.Turn.AssistantMessage.Content == "" {
			return performanceReport{}, fmt.Errorf("full-chain request %d returned an empty answer", index)
		}
	}
	if state.agent.calls != agentCallsBefore {
		return performanceReport{}, errors.New("semantic cache hit invoked the main Agent")
	}
	lookupP50, lookupP95 := durationPercentiles(lookupDurations)
	fullP50, fullP95 := durationPercentiles(fullChainDurations)
	return performanceReport{
		SchemaVersion: "semantic_cache_performance_v1", DatasetVersion: fixture.DatasetVersion,
		EmbeddingProfile: profile.Fingerprint, Generation: state.generation,
		Threshold: cfg.SemanticAnswerCache.SemanticMinimumSimilarity, PairObservations: observations,
		LookupSamples: len(lookupDurations), LookupP50Millis: milliseconds(lookupP50), LookupP95Millis: milliseconds(lookupP95),
		FullChainSamples: len(fullChainDurations), FullChainP50Millis: milliseconds(fullP50), FullChainP95Millis: milliseconds(fullP95),
		ProviderCalls: embedder.calls, EmbeddingTokens: embedder.tokens, MainModelCalls: 0, ToolCalls: 0,
		DegradationEvents: degradations, DegradationRate: float64(degradations) / float64(len(fullChainDurations)),
		MeasuredAt:          time.Now().UTC(),
		MeasurementBoundary: "query embedding, pgvector lookup, user/assistant message and run-observation commit; excludes browser network/rendering",
	}, nil
}

type fixtureState struct {
	service    *conversation.Service
	cache      *platformpostgres.SemanticAnswerCacheRepository
	agent      *fixtureResponder
	userID     uuid.UUID
	generation int64
}

func seedCacheFixture(
	ctx context.Context,
	tx *gorm.DB,
	cfg config.Config,
	profile knowledge.EmbeddingProfile,
	fixture latencyFixture,
	vectors [][]float32,
) (fixtureState, error) {
	userID, documentID := uuid.New(), uuid.New()
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Semantic Cache Observer', 'observation-hash', 'analyst', 'active', false)`,
		userID, "semantic_cache_observer_"+uuid.NewString()[:8]).Error; err != nil {
		return fixtureState{}, err
	}
	knowledgeRepository := platformpostgres.NewKnowledgeRepository(tx)
	if _, err := knowledgeRepository.CreateDocument(ctx, knowledge.CreateDocumentInput{
		ID: documentID, Scope: knowledge.ScopeGlobal, Title: "语义缓存观测知识", CreatedBy: userID,
	}); err != nil {
		return fixtureState{}, err
	}
	content := "MESGuard 企业知识问答固定流量回放引用。"
	chunks, err := knowledge.ChunkMarkdown(content, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		return fixtureState{}, err
	}
	versionID := uuid.New()
	if _, err := knowledgeRepository.PublishVersion(ctx, knowledge.PublishVersionInput{
		ID: versionID, DocumentID: documentID, SourceMediaType: "text/markdown",
		SourceSizeBytes: int64(len([]byte(content))), SourceSHA256: knowledge.SHA256Hex(content),
		ParserVersion: "semantic-cache-observation-v1", CreatedBy: userID, Chunks: chunks,
	}); err != nil {
		return fixtureState{}, err
	}
	var chunkRecord struct {
		ID            uuid.UUID `gorm:"column:id"`
		ContentSHA256 string    `gorm:"column:content_sha256"`
	}
	if err := tx.Raw(`SELECT id, content_sha256 FROM knowledge_chunks WHERE document_version_id = ? ORDER BY ordinal LIMIT 1`, versionID).Scan(&chunkRecord).Error; err != nil {
		return fixtureState{}, err
	}
	sourceRef := "knowledge:" + versionID.String() + "/" + chunkRecord.ID.String()
	agent := &fixtureResponder{response: conversation.AgentResponse{
		Content: "固定流量回放答案。[source:" + sourceRef + "]",
		Citations: []conversation.MessageCitation{{
			Position: 0, SourceType: conversation.CitationSourceKnowledgeChunk,
			SourceRef: sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
		}},
		RunObservation: &conversation.AgentRunObservation{
			ModelProvider: "fixture", ModelID: "fixture-model", PromptVersion: "semantic-cache-observation-v1",
			ExecutionPath: conversation.AgentRunExecutionAgent, Outcome: conversation.AgentRunAnswered,
			AnswerCacheEligible: true, ToolCalls: 1,
			Usage: conversation.AgentRunUsage{ModelCalls: 1, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			RetrievedSources: []conversation.AgentRunSource{{
				SourceType: conversation.CitationSourceKnowledgeChunk,
				SourceRef:  sourceRef, ContentSHA256: chunkRecord.ContentSHA256,
			}},
		},
	}}
	conversationRepository := platformpostgres.NewConversationRepository(tx)
	service, err := conversation.NewService(conversationRepository)
	if err != nil {
		return fixtureState{}, err
	}
	if _, err := service.WithAgentResponder(agent); err != nil {
		return fixtureState{}, err
	}
	cache, err := platformpostgres.NewSemanticAnswerCacheRepositoryWithConfig(tx, platformpostgres.SemanticAnswerCacheRepositoryConfig{
		MaxRecords: cfg.SemanticAnswerCache.MaxRecords, TTLJitterRatio: cfg.SemanticAnswerCache.TTLJitterRatio,
	})
	if err != nil {
		return fixtureState{}, err
	}
	if _, err := service.WithSemanticAnswerCache(cache, conversation.SemanticAnswerCacheConfig{
		TTL:            time.Duration(cfg.SemanticAnswerCache.TTLSeconds) * time.Second,
		LookupTimeout:  time.Duration(cfg.SemanticAnswerCache.LookupTimeoutMillis) * time.Millisecond,
		WriteTimeout:   time.Duration(cfg.SemanticAnswerCache.WriteTimeoutMillis) * time.Millisecond,
		MaxAnswerBytes: cfg.SemanticAnswerCache.MaxAnswerBytes, MaxCitations: cfg.SemanticAnswerCache.MaxCitations,
	}, nil); err != nil {
		return fixtureState{}, err
	}
	for index, pair := range fixture.Pairs {
		created, createErr := service.Create(ctx, conversation.Actor{UserID: userID}, conversation.CreateInput{Title: "语义缓存锚点"})
		if createErr != nil {
			return fixtureState{}, createErr
		}
		result, executeErr := service.ExecuteTurn(ctx, conversation.Actor{UserID: userID}, uuid.NewString(), conversation.AppendMessageInput{
			ConversationID: created.ID, Content: pair.AnchorQuestion,
		})
		if executeErr != nil {
			return fixtureState{}, executeErr
		}
		questionHash, keyErr := semanticcache.ExactQuestionKey(pair.AnchorQuestion)
		if keyErr != nil {
			return fixtureState{}, keyErr
		}
		if indexErr := cache.IndexSemantic(ctx, semanticcache.SemanticIndexInput{
			QuestionHash: questionHash, Question: pair.AnchorQuestion, Vector: vectors[index*2],
			ProfileID: profile.ID, ProfileFingerprint: profile.Fingerprint,
			NormalizationVersion: semanticcache.SemanticNormalizationVersion, SourceRunID: result.TurnID,
		}); indexErr != nil {
			return fixtureState{}, indexErr
		}
	}
	var generation int64
	if err := tx.Raw("SELECT generation FROM global_knowledge_generation WHERE singleton = 1").Scan(&generation).Error; err != nil {
		return fixtureState{}, err
	}
	return fixtureState{service: service, cache: cache, agent: agent, userID: userID, generation: generation}, nil
}

type turnRequest struct {
	conversationID uuid.UUID
	question       string
}

type fixtureResponder struct {
	response conversation.AgentResponse
	calls    int
}

func (s *fixtureResponder) Respond(context.Context, conversation.AgentRequest) (conversation.AgentResponse, error) {
	s.calls++
	return s.response, nil
}

type meteredEmbedder struct {
	delegate knowledge.Embedder
	calls    int
	tokens   int
}

func (s *meteredEmbedder) Embed(ctx context.Context, request knowledge.EmbeddingRequest) (knowledge.EmbeddingResult, error) {
	s.calls++
	result, err := s.delegate.Embed(ctx, request)
	s.tokens += result.Usage.TotalTokens
	return result, err
}

func cosineSimilarity(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += float64(left[index]) * float64(right[index])
		leftNorm += float64(left[index]) * float64(left[index])
		rightNorm += float64(right[index]) * float64(right[index])
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func durationPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	values = slices.Clone(values)
	slices.Sort(values)
	return values[percentileIndex(len(values), 0.50)], values[percentileIndex(len(values), 0.95)]
}

func percentileIndex(size int, percentile float64) int {
	return max(0, min(int(math.Ceil(float64(size)*percentile))-1, size-1))
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func writeNewJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
