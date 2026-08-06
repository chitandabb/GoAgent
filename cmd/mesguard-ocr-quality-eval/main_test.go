package main

import (
	"math"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
)

func TestParseFlagsDefaultsToDryRun(t *testing.T) {
	options, err := parseFlags([]string{"-input", "scan.pdf", "-page", "8"})
	if err != nil || options.executeProvider {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestEstimatedOCRCost(t *testing.T) {
	cost := estimatedOCRCost(&knowledgeenrichment.ProviderUsage{
		PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200,
	}, 0.3, 0.5)
	if math.Abs(cost-0.0004) > 0.0000001 {
		t.Fatalf("cost = %.8f", cost)
	}
}

func TestPairedCharacterSimilarity(t *testing.T) {
	distance, similarity, compared := pairedCharacterSimilarity("abc", "adc")
	if !compared || distance != 1 || math.Abs(similarity-2.0/3.0) > 0.000001 {
		t.Fatalf("distance = %d, similarity = %.6f, compared = %v", distance, similarity, compared)
	}
}

func TestPairedCharacterSimilaritySkipsOversizedComparison(t *testing.T) {
	text := make([]byte, 9_000)
	for index := range text {
		text[index] = 'a'
	}
	if _, _, compared := pairedCharacterSimilarity(string(text), string(text)); compared {
		t.Fatal("oversized comparison was not skipped")
	}
}
