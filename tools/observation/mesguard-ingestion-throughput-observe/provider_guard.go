package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

const (
	defaultEmbeddingPriceCNYPerMillion = 0.5
	defaultMaxProviderCostCNY          = 0.05
	defaultProviderRPM                 = 900
	defaultProviderTPM                 = 600_000
)

type providerDocumentEstimate struct {
	DocumentID         string
	Elements           int
	VisualAssets       int
	Chunks             int
	EstimatedTokens    int
	BaselineRequests   int
	ExperimentRequests int
}

type providerPlanEstimate struct {
	Documents                       int
	ChunksPerArm                    int
	EstimatedTokensPerArm           int
	BaselineRequestsPerRepetition   int
	ExperimentRequestsPerRepetition int
	PlannedRequests                 int
	PlannedEstimatedTokens          int
	EstimatedCostCNY                float64
	DocumentEstimates               []providerDocumentEstimate
}

func estimateProviderPlan(
	ctx context.Context,
	cfg config.Config,
	documents []loadedDocument,
	options commandOptions,
) (providerPlanEstimate, error) {
	parser, err := buildParser(cfg.Knowledge)
	if err != nil {
		return providerPlanEstimate{}, err
	}
	baselineBatchSize := 1
	if options.documentConcurrencyAblation {
		baselineBatchSize = cfg.Models.Embedding.BatchSize
	}
	plan := providerPlanEstimate{
		Documents:         len(documents),
		DocumentEstimates: make([]providerDocumentEstimate, 0, len(documents)),
	}
	for _, document := range documents {
		parsed, err := parser.Parse(ctx, knowledgeparserInput(document))
		if err != nil {
			return providerPlanEstimate{}, fmt.Errorf("estimate document %s: %w", document.definition.DocumentID, err)
		}
		chunks, err := chunksForProviderEstimate(parsed, knowledge.TextChunkOptions{
			MaxRunes: cfg.Knowledge.ChunkMaxRunes, OverlapRunes: cfg.Knowledge.ChunkOverlapRunes,
		})
		if err != nil {
			return providerPlanEstimate{}, fmt.Errorf("estimate chunks for %s: %w", document.definition.DocumentID, err)
		}
		estimatedTokens := estimateChunkTokens(chunks)
		plan.ChunksPerArm += len(chunks)
		plan.EstimatedTokensPerArm += estimatedTokens
		plan.BaselineRequestsPerRepetition += batches(len(chunks), baselineBatchSize)
		plan.ExperimentRequestsPerRepetition += batches(len(chunks), cfg.Models.Embedding.BatchSize)
		plan.DocumentEstimates = append(plan.DocumentEstimates, providerDocumentEstimate{
			DocumentID: document.definition.DocumentID, Elements: len(parsed.Elements),
			VisualAssets: len(parsed.VisualAssets), Chunks: len(chunks), EstimatedTokens: estimatedTokens,
			BaselineRequests:   batches(len(chunks), baselineBatchSize),
			ExperimentRequests: batches(len(chunks), cfg.Models.Embedding.BatchSize),
		})
	}
	plan.PlannedRequests = options.repetitions *
		(plan.BaselineRequestsPerRepetition + plan.ExperimentRequestsPerRepetition)
	plan.PlannedEstimatedTokens = options.repetitions * 2 * plan.EstimatedTokensPerArm
	plan.EstimatedCostCNY = estimatedProviderCostCNY(
		plan.PlannedEstimatedTokens, options.embeddingPriceCNYPerMillion,
	)
	return plan, nil
}

func printProviderPlan(
	datasetVersion string,
	plan providerPlanEstimate,
	options commandOptions,
	includeDocuments bool,
) {
	if includeDocuments {
		for _, document := range plan.DocumentEstimates {
			fmt.Printf("document=%s elements=%d visual_assets=%d chunks=%d estimated_tokens=%d baseline_embedding_requests=%d experiment_embedding_requests=%d\n",
				document.DocumentID, document.Elements, document.VisualAssets, document.Chunks,
				document.EstimatedTokens, document.BaselineRequests, document.ExperimentRequests)
		}
	}
	fmt.Printf("provider_preflight dataset=%s documents=%d repetitions=%d planned_arms=%d chunks_per_arm=%d baseline_requests_per_repetition=%d experiment_requests_per_repetition=%d planned_requests=%d estimated_tokens_per_arm=%d planned_estimated_tokens=%d estimated_cost_cny=%.4f max_cost_cny=%.4f provider_rpm=%d provider_tpm=%d\n",
		datasetVersion, plan.Documents, options.repetitions, options.repetitions*2, plan.ChunksPerArm,
		plan.BaselineRequestsPerRepetition, plan.ExperimentRequestsPerRepetition, plan.PlannedRequests,
		plan.EstimatedTokensPerArm, plan.PlannedEstimatedTokens, plan.EstimatedCostCNY,
		options.maxProviderCostCNY, options.providerRPM, options.providerTPM)
}

func knowledgeparserInput(document loadedDocument) knowledgeparser.Input {
	return knowledgeparser.Input{
		MediaType:    document.definition.MediaType,
		OriginalName: document.definition.FileName,
		Content:      document.content,
	}
}

func estimateChunkTokens(chunks []knowledge.ChunkDraft) int {
	total := 0
	for _, chunk := range chunks {
		total += estimateEmbeddingTextTokens(chunk.ContentText)
	}
	return total
}

// The estimate deliberately includes headroom. CJK and other non-ASCII runes
// are counted individually; ASCII text is approximated at four bytes per token.
func estimateEmbeddingTextTokens(text string) int {
	asciiRunes, nonASCIIRunes := 0, 0
	for _, value := range text {
		if value <= unicode.MaxASCII {
			asciiRunes++
		} else {
			nonASCIIRunes++
		}
	}
	base := (asciiRunes+3)/4 + nonASCIIRunes
	return max(1, (base*11+7)/8+8)
}

func estimatedProviderCostCNY(tokens int, pricePerMillion float64) float64 {
	return float64(tokens) * pricePerMillion / 1_000_000
}

func providerTokenBudget(maxCostCNY, pricePerMillion float64) int {
	return int(maxCostCNY / pricePerMillion * 1_000_000)
}

type guardedEmbedder struct {
	inner     knowledge.Embedder
	limiter   *smoothProviderLimiter
	cancel    context.CancelFunc
	maxTokens int

	mu              sync.Mutex
	estimatedTokens int
	actualTokens    int
	abortErr        error
}

func newGuardedEmbedder(
	inner knowledge.Embedder,
	maxTokens, rpm, tpm int,
	cancel context.CancelFunc,
) (*guardedEmbedder, error) {
	if inner == nil || maxTokens < 1 || cancel == nil {
		return nil, errors.New("provider guard dependencies are invalid")
	}
	limiter, err := newSmoothProviderLimiter(rpm, tpm)
	if err != nil {
		return nil, err
	}
	return &guardedEmbedder{inner: inner, limiter: limiter, cancel: cancel, maxTokens: maxTokens}, nil
}

func (e *guardedEmbedder) Embed(
	ctx context.Context,
	input knowledge.EmbeddingRequest,
) (knowledge.EmbeddingResult, error) {
	estimatedTokens := 0
	for _, text := range input.Texts {
		estimatedTokens += estimateEmbeddingTextTokens(text)
	}
	if err := e.reserveEstimatedTokens(estimatedTokens); err != nil {
		return knowledge.EmbeddingResult{}, err
	}
	if err := e.limiter.Wait(ctx, estimatedTokens); err != nil {
		if abortErr := e.Err(); abortErr != nil {
			return knowledge.EmbeddingResult{}, abortErr
		}
		return knowledge.EmbeddingResult{}, err
	}
	result, err := e.inner.Embed(ctx, input)
	if err != nil {
		if isProviderRateLimit(err) {
			return knowledge.EmbeddingResult{}, e.abort(fmt.Errorf("provider rate limit aborted the evaluation: %w", err))
		}
		return knowledge.EmbeddingResult{}, err
	}
	if err := e.recordActualTokens(result.Usage.TotalTokens); err != nil {
		return knowledge.EmbeddingResult{}, err
	}
	return result, nil
}

func (e *guardedEmbedder) reserveEstimatedTokens(tokens int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.abortErr != nil {
		return e.abortErr
	}
	if tokens < 1 || e.estimatedTokens+tokens > e.maxTokens {
		err := fmt.Errorf(
			"provider token budget exhausted before request: reserved=%d next=%d limit=%d",
			e.estimatedTokens, tokens, e.maxTokens,
		)
		e.abortErr = err
		e.cancel()
		return err
	}
	e.estimatedTokens += tokens
	return nil
}

func (e *guardedEmbedder) recordActualTokens(tokens int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.abortErr != nil {
		return e.abortErr
	}
	e.actualTokens += tokens
	if e.actualTokens <= e.maxTokens {
		return nil
	}
	err := fmt.Errorf(
		"provider token budget exhausted after response: actual=%d limit=%d",
		e.actualTokens, e.maxTokens,
	)
	e.abortErr = err
	e.cancel()
	return err
}

func (e *guardedEmbedder) abort(err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.abortErr == nil {
		e.abortErr = err
		e.cancel()
	}
	return e.abortErr
}

func (e *guardedEmbedder) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.abortErr
}

func isProviderRateLimit(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "status=429") ||
		strings.Contains(message, "throttling.allocationquota") ||
		strings.Contains(message, "throttling.ratequota") ||
		strings.Contains(message, "insufficient_quota")
}

type smoothProviderLimiter struct {
	mu          sync.Mutex
	rpm         int
	tpm         int
	nextRequest time.Time
	nextToken   time.Time
}

func newSmoothProviderLimiter(rpm, tpm int) (*smoothProviderLimiter, error) {
	if rpm < 1 || tpm < 1 {
		return nil, errors.New("provider rate limits must be positive")
	}
	return &smoothProviderLimiter{rpm: rpm, tpm: tpm}, nil
}

func (l *smoothProviderLimiter) Wait(ctx context.Context, tokens int) error {
	if tokens < 1 {
		return errors.New("provider token estimate must be positive")
	}
	now := time.Now()
	l.mu.Lock()
	startAt := maxTime(now, l.nextRequest, l.nextToken)
	l.nextRequest = startAt.Add(spacingDuration(1, l.rpm))
	l.nextToken = startAt.Add(spacingDuration(tokens, l.tpm))
	l.mu.Unlock()
	if !startAt.After(now) {
		return nil
	}
	timer := time.NewTimer(time.Until(startAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func spacingDuration(units, perMinute int) time.Duration {
	return time.Duration(float64(time.Minute) * float64(units) / float64(perMinute))
}

func maxTime(values ...time.Time) time.Time {
	result := values[0]
	for _, value := range values[1:] {
		if value.After(result) {
			result = value
		}
	}
	return result
}
