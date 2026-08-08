package knowledgeingestion

import (
	"encoding/json"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestMergeElementsSuppressesOCRCoveredByNative(t *testing.T) {
	page := 1
	elements := []knowledge.DocumentElement{
		{
			Index: 0, PageNumber: &page, ElementType: knowledge.ElementText,
			ContentText: "Alarm E42 means the PLC connection timed out. Check the industrial network cable.",
		},
		{
			Index: 1, PageNumber: &page, ElementType: knowledge.ElementOCRText,
			ContentText: "PLC connection timed out. Check the industrial network cable.",
			Metadata:    json.RawMessage(`{"provider":"ocr"}`),
		},
	}
	merged, err := mergeElements(elements)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SuppressedCount != 1 || len(merged.SearchableElements) != 1 ||
		merged.Decisions[1].Reason != "ocr_covered_by_native" ||
		merged.Decisions[1].DuplicateOfIndex == nil || *merged.Decisions[1].DuplicateOfIndex != 0 {
		t.Fatalf("merged = %+v", merged)
	}
	var metadata map[string]any
	if err := json.Unmarshal(merged.Elements[1].Metadata, &metadata); err != nil ||
		metadata["provider"] != "ocr" || metadata["indexingDisposition"] != "suppress_duplicate" {
		t.Fatalf("metadata = %+v, err = %v", metadata, err)
	}
}

func TestMergeElementsKeepsDescriptionsAndDifferentPages(t *testing.T) {
	pageOne, pageTwo := 1, 2
	elements := []knowledge.DocumentElement{
		{Index: 0, PageNumber: &pageOne, ElementType: knowledge.ElementText, ContentText: "PLC timeout E42"},
		{Index: 1, PageNumber: &pageOne, ElementType: knowledge.ElementImageDescription, ContentText: "PLC timeout E42 with a red alarm banner"},
		{Index: 2, PageNumber: &pageTwo, ElementType: knowledge.ElementOCRText, ContentText: "PLC timeout E42"},
	}
	merged, err := mergeElements(elements)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SuppressedCount != 0 || len(merged.SearchableElements) != len(elements) {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestMergeElementsKeepsLongerOverlappingOCR(t *testing.T) {
	page := 1
	longer := "Production order PO-1001 is blocked because material MAT-42 is unavailable in warehouse A."
	shorter := "Production order PO-1001 is blocked because material MAT-42 is unavailable in warehouse"
	elements := []knowledge.DocumentElement{
		{Index: 0, PageNumber: &page, ElementType: knowledge.ElementOCRText, ContentText: shorter},
		{Index: 1, PageNumber: &page, ElementType: knowledge.ElementOCRText, ContentText: longer},
	}
	merged, err := mergeElements(elements)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SuppressedCount != 1 || merged.SearchableElements[0].Index != 1 ||
		merged.Decisions[0].DuplicateOfIndex == nil || *merged.Decisions[0].DuplicateOfIndex != 1 ||
		merged.Decisions[0].Reason != "overlapping_ocr_containment" {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestMergeElementsPrefersStructuredTableForExactText(t *testing.T) {
	page := 1
	elements := []knowledge.DocumentElement{
		{Index: 0, PageNumber: &page, ElementType: knowledge.ElementText, ContentText: "Code | Meaning"},
		{Index: 1, PageNumber: &page, ElementType: knowledge.ElementTable, ContentText: "Code | Meaning"},
	}
	merged, err := mergeElements(elements)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.SearchableElements) != 1 || merged.SearchableElements[0].Index != 1 ||
		merged.Decisions[0].DuplicateOfIndex == nil || *merged.Decisions[0].DuplicateOfIndex != 1 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestMergeElementsSuppressesNonsemanticSeparators(t *testing.T) {
	elements := []knowledge.DocumentElement{
		{Index: 0, ElementType: knowledge.ElementText, ContentText: "---"},
		{Index: 1, ElementType: knowledge.ElementText, ContentText: "PLC timeout E42"},
	}
	merged, err := mergeElements(elements)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SuppressedCount != 1 || len(merged.SearchableElements) != 1 ||
		merged.SearchableElements[0].Index != 1 ||
		merged.Decisions[0].Disposition != elementMergeSuppressNonsemantic ||
		merged.Decisions[0].Reason != "nonsemantic_content" {
		t.Fatalf("merged = %+v", merged)
	}
	var metadata map[string]any
	if err := json.Unmarshal(merged.Elements[0].Metadata, &metadata); err != nil ||
		metadata["indexingDisposition"] != "suppress_nonsemantic" {
		t.Fatalf("metadata = %+v, err = %v", metadata, err)
	}
}
