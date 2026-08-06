package main

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
)

func TestMatchTableAnchorsUsesDistinctTables(t *testing.T) {
	tables := []knowledge.DocumentElement{
		{ElementType: knowledge.ElementTable, ContentText: "name | value | one"},
		{ElementType: knowledge.ElementTable, ContentText: "name | value | two"},
	}
	matched := matchTableAnchors(tables, [][]string{{"name", "one"}, {"name", "two"}})
	if matched != 2 {
		t.Fatalf("matched = %d", matched)
	}
}

func TestEvaluateCasesSeparatesCountsAndRelationships(t *testing.T) {
	page := 3
	documents := map[string]parsedDocument{
		"deck": {
			elementsByPage: map[int][]knowledge.DocumentElement{page: {
				{PageNumber: &page, ElementType: knowledge.ElementText, ContentText: "Slide anchor"},
				{PageNumber: &page, ElementType: knowledge.ElementTable, ContentText: "A | B"},
			}},
			assetsByPage: map[int][]knowledgeparser.VisualAsset{page: {
				{PageNumber: &page, RelationshipID: "rId1", SourcePart: "ppt/slides/slide3.xml"},
			}},
		},
	}
	tableCount, visualCount, visualUses := 1, 1, 1
	result := evaluateCases("v1", []evaluationCase{{
		ID: "case", DocumentID: "deck", PageNumber: page,
		TextAnchors: []string{"slide anchor"}, ExpectedTableCount: &tableCount,
		TableAnchors: [][]string{{"A", "B"}}, SourceVisualUseCount: &visualUses,
		ExpectedVisualRelationshipCount: &visualCount,
	}}, documents)
	if result.PassedCases != 1 || result.TextAnchorRecall != 1 || result.TableAnchorRecall != 1 ||
		result.VisualRelationshipCompleteness != 1 {
		t.Fatalf("summary = %+v", result)
	}
}

func TestNormalizeIgnoresWhitespaceAndCase(t *testing.T) {
	if got := normalize("  File\n F1 "); got != "filef1" {
		t.Fatalf("normalize = %q", got)
	}
}
