package main

import (
	"fmt"
	"math"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"
)

type providerPlan struct {
	DatasetVersion            string
	Cases                     int
	Documents                 int
	Chunks                    int
	DocumentEmbeddingRequests int
	EstimatedEmbeddingTokens  int
	PlannedChatTokens         int
	EstimatedPlanningCostCNY  float64
}

func buildProviderPlan(
	corpus knowledge.AdvancedRetrievalEvaluationCorpus,
	chunksByDocument map[string][]knowledge.ChunkDraft,
	cases []qualityCaseDefinition,
	options commandOptions,
) providerPlan {
	chunkCount := 0
	embeddingTokens := 0
	for _, chunks := range chunksByDocument {
		chunkCount += len(chunks)
		for _, chunk := range chunks {
			embeddingTokens += estimateEmbeddingTextTokens(chunk.ContentText)
		}
	}
	// Query embeddings are part of online retrieval. Reserve one conservative
	// estimate per case in addition to the document fixture.
	for _, definition := range cases {
		embeddingTokens += estimateEmbeddingTextTokens(definition.UserQuery)
	}
	batchSize := 10
	documentRequests := (chunkCount + batchSize - 1) / batchSize
	// The runner settles usage after a provider call returns. Reserve one
	// additional call-sized allowance per case for admission planning, but do
	// not present this estimate as a hard provider-side limit.
	plannedChatTokens := len(cases) * (options.maxChatTokensPerCase + providerSettlementLagReserveTokens)
	plannedChatCost := float64(plannedChatTokens) * math.Max(
		options.chatInputPriceCNYPerMillion, options.chatOutputPriceCNYPerMillion,
	) / 1_000_000
	return providerPlan{
		DatasetVersion: corpus.DatasetVersion, Cases: len(cases), Documents: len(corpus.Documents),
		Chunks: chunkCount, DocumentEmbeddingRequests: documentRequests,
		EstimatedEmbeddingTokens: embeddingTokens, PlannedChatTokens: plannedChatTokens,
		EstimatedPlanningCostCNY: plannedChatCost + embeddingCost(
			embeddingTokens, options.embeddingPriceCNYPerMillion,
		),
	}
}

func printProviderPlan(plan providerPlan, options commandOptions) {
	profile := options.chatProfile
	if profile == "" {
		profile = "active"
	}
	fmt.Printf(
		"conversation_quality_preflight dataset=%s chat_profile=%s cases=%d documents=%d chunks=%d document_embedding_requests<=%d estimated_embedding_tokens<=%d planned_chat_tokens=%d estimated_planning_cost_cny=%.4f cost_guard_cny=%.4f execute_provider=%t provider_settlement_may_overshoot=true\n",
		plan.DatasetVersion, profile, plan.Cases, plan.Documents, plan.Chunks,
		plan.DocumentEmbeddingRequests, plan.EstimatedEmbeddingTokens,
		plan.PlannedChatTokens, plan.EstimatedPlanningCostCNY,
		options.maxProviderCostCNY, options.executeProvider,
	)
}

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

func onlineRunCost(
	run conversation.RecordedAgentRun,
	query string,
	options commandOptions,
) float64 {
	usage := run.Observation.Usage
	chatCost := float64(usage.PromptTokens)*options.chatInputPriceCNYPerMillion/1_000_000 +
		float64(usage.CompletionTokens)*options.chatOutputPriceCNYPerMillion/1_000_000
	queryEmbeddingCost := embeddingCost(
		estimateEmbeddingTextTokens(query), options.embeddingPriceCNYPerMillion,
	)
	return chatCost + queryEmbeddingCost
}

func embeddingCost(tokens int, priceCNYPerMillion float64) float64 {
	return float64(tokens) * priceCNYPerMillion / 1_000_000
}
