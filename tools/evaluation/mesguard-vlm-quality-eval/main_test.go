package main

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
)

func TestValidateFixtureRejectsMoreThanThreeCases(t *testing.T) {
	item := validCase()
	value := fixture{Version: "v1", Cases: []evaluationCase{item, item, item, item}}
	if err := validateFixture(value); err == nil {
		t.Fatal("validateFixture accepted more than three cases")
	}
}

func TestMatchedFactsRequiresEveryTermGroup(t *testing.T) {
	facts := []semanticFact{{
		ID:    "chain",
		Terms: [][]string{{"CPU"}, {"I/O通道", "通道"}, {"控制器"}, {"设备"}, {"连接", "通过"}},
	}}
	if matched := matchedFacts("CPU通过I/O通道连接控制器，再连接设备。", facts); len(matched) != 1 {
		t.Fatalf("matchedFacts() = %v", matched)
	}
	if matched := matchedFacts("CPU、I/O通道、控制器、设备。", facts); len(matched) != 0 {
		t.Fatalf("matchedFacts accepted labels without a relation: %v", matched)
	}
}

func TestEstimatedCost(t *testing.T) {
	input, output := 1.0, 10.0
	cost := estimatedCost(&knowledgeenrichment.ProviderUsage{
		PromptTokens: 2000, CompletionTokens: 100, TotalTokens: 2100,
	}, pricing{InputPricePerMillion: &input, OutputPricePerMillion: &output})
	if cost == nil || *cost != 0.003 {
		t.Fatalf("estimatedCost() = %v", cost)
	}
	if cost := estimatedCost(&knowledgeenrichment.ProviderUsage{}, pricing{}); cost != nil {
		t.Fatalf("unpriced estimatedCost() = %v", cost)
	}
}

func TestNearestRankPercentile(t *testing.T) {
	values := []float64{4, 5, 8}
	if got := nearestRankPercentile(values, 0.50); got != 5 {
		t.Fatalf("P50 = %v", got)
	}
	if got := nearestRankPercentile(values, 0.95); got != 8 {
		t.Fatalf("P95 = %v", got)
	}
}

func validCase() evaluationCase {
	return evaluationCase{
		ID: "case", SourcePath: "slide.png",
		SourceSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Crop:         cropRectangle{Width: 100, Height: 100}, TextAnchors: []string{"CPU"},
		SemanticFacts:         []semanticFact{{ID: "fact", Terms: [][]string{{"CPU"}, {"设备"}}}},
		MinimumTextAnchorRate: 1, MinimumSemanticRate: 1,
	}
}
