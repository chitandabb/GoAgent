package knowledgetable

import (
	"math"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
)

func TestResultValidateAcceptsBoundedStructuredTable(t *testing.T) {
	result := validResult()
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResultValidateRejectsCorruptStructure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "empty cells", mutate: func(r *Result) { r.Cells = nil }},
		{name: "duplicate coordinate", mutate: func(r *Result) { r.Cells = append(r.Cells, r.Cells[0]) }},
		{name: "invalid span", mutate: func(r *Result) { r.Cells[0].ColumnSpan = 0 }},
		{name: "nan confidence", mutate: func(r *Result) { r.Confidence = math.NaN() }},
		{name: "partial without reason", mutate: func(r *Result) { r.Partial = true }},
		{name: "complete with reason", mutate: func(r *Result) { r.Reason = "unexpected" }},
		{name: "invalid usage", mutate: func(r *Result) { r.Usage = &Usage{PromptTokens: 2, TotalTokens: 1} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validResult()
			test.mutate(&result)
			if err := result.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRequestValidateRequiresBoundedLayoutCrop(t *testing.T) {
	page := 1
	request := Request{
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetLayoutRegion, PageNumber: &page,
			SourcePath: "pages/1/layout-regions/0", MediaType: "image/png",
			SizeBytes: 3, SHA256: knowledge.SHA256Hex("png"), Width: 1, Height: 1, Content: []byte("png"),
		},
		Reason: "table_structure_required",
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Asset.Kind = knowledgeparser.VisualAssetEmbeddedImage
	if err := request.Validate(); err == nil {
		t.Fatal("expected non-layout asset rejection")
	}
}

func validResult() Result {
	return Result{
		Provider: "dashscope", Model: "qwen3-vl-plus", PromptVersion: "table-recovery-v1",
		Markdown: "| alarm | count |\n| --- | ---: |\n| E42 | 3 |",
		Cells: []Cell{
			{Row: 0, Column: 0, RowSpan: 1, ColumnSpan: 1, Text: "alarm", Header: true},
			{Row: 0, Column: 1, RowSpan: 1, ColumnSpan: 1, Text: "count", Header: true},
			{Row: 1, Column: 0, RowSpan: 1, ColumnSpan: 1, Text: "E42"},
			{Row: 1, Column: 1, RowSpan: 1, ColumnSpan: 1, Text: "3"},
		},
		Confidence: 0.96, Usage: &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}
