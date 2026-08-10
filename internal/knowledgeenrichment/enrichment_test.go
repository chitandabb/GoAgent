package knowledgeenrichment

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgetable"
)

func TestOrchestratorPlansSkipMissingAndBudgetDeterministically(t *testing.T) {
	orchestrator, _ := New(Config{MaxEnrichments: 2, MinPixels: 4096}, nil)
	page := 1
	assets := []knowledgeparser.VisualAsset{
		visualAsset(0, knowledgeparser.VisualAssetEmbeddedImage, "image/png", 16, 16),
		visualAsset(1, knowledgeparser.VisualAssetEmbeddedImage, "image/png", 100, 100),
		visualAsset(2, knowledgeparser.VisualAssetDocumentPage, "application/pdf", 0, 0),
		visualAsset(3, knowledgeparser.VisualAssetSourceImage, "image/jpeg", 100, 100),
	}
	assets[0].SourcePart, assets[0].RelationshipID = "word/document.xml", "rIdSmall"
	assets[1].SourcePart, assets[1].RelationshipID = "word/document.xml", "rIdLarge"
	assets[2].PageNumber = &page
	assets[2].Content = nil
	assets[2].SourcePath = "pages/1"

	output, err := orchestrator.Enrich(context.Background(), Source{}, assets, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Partial || !errors.Is(output.Cause, ErrUnavailable) || len(output.Records) != 4 ||
		output.Records[0].Status != StatusSkipped || output.Records[0].Reason != "decorative_small_image" ||
		output.Records[1].Status != StatusMissing || output.Records[2].Route != RouteOCR ||
		output.Records[3].Reason != "budget_exceeded" {
		t.Fatalf("output = %+v", output)
	}
}

func TestOrchestratorSkipsUnreferencedEmbeddedAssets(t *testing.T) {
	called := false
	orchestrator, _ := New(Config{MaxEnrichments: 1, MinPixels: 1}, processorFunc(
		func(context.Context, Request) (ProviderResult, error) {
			called = true
			return ProviderResult{}, nil
		},
	))
	output, err := orchestrator.Enrich(context.Background(), Source{}, []knowledgeparser.VisualAsset{
		visualAsset(0, knowledgeparser.VisualAssetEmbeddedImage, "image/png", 100, 100),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if called || output.Partial || len(output.Records) != 1 ||
		output.Records[0].Status != StatusSkipped || output.Records[0].Reason != "unreferenced_asset" {
		t.Fatalf("output = %+v, called = %v", output, called)
	}
}

func TestOrchestratorReindexesValidatedProviderElements(t *testing.T) {
	processor := processorFunc(func(context.Context, Request) (ProviderResult, error) {
		return ProviderResult{
			Provider: "provider", Model: "vision-v1",
			Elements: []knowledge.DocumentElement{{
				ElementType: knowledge.ElementOCRText, ContentText: "PLC timeout E42",
			}},
		}, nil
	})
	orchestrator, _ := New(Config{MaxEnrichments: 2, MinPixels: 4096}, processor)
	output, err := orchestrator.Enrich(context.Background(), Source{}, []knowledgeparser.VisualAsset{
		visualAsset(0, knowledgeparser.VisualAssetSourceImage, "image/png", 100, 100),
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if output.Partial || len(output.Elements) != 1 || output.Elements[0].Index != 3 ||
		output.Records[0].OutputElementIndexes[0] != 3 {
		t.Fatalf("output = %+v", output)
	}
}

func TestOrchestratorRoutesTableCropToIndependentProcessor(t *testing.T) {
	visualCalled := false
	tableCalled := false
	orchestrator, err := NewWithTableProcessor(
		Config{MaxEnrichments: 2, MinPixels: 1},
		processorFunc(func(context.Context, Request) (ProviderResult, error) {
			visualCalled = true
			return ProviderResult{}, errors.New("visual processor must not receive table route")
		}),
		tableProcessorFunc(func(_ context.Context, request knowledgetable.Request) (knowledgetable.Result, error) {
			tableCalled = true
			if request.Reason != "table_structure_required" {
				t.Fatalf("request = %+v", request)
			}
			return validTableResult(), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	asset := visualAsset(0, knowledgeparser.VisualAssetLayoutRegion, "image/png", 100, 100)
	page := 1
	asset.PageNumber = &page
	asset.SourcePath = "pages/1/layout-regions/0"
	output, err := orchestrator.EnrichPlanned(context.Background(), Source{}, []PlannedAsset{{
		Asset: asset, Route: RouteTable, Reason: "table_structure_required",
	}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if visualCalled || !tableCalled || output.Partial || len(output.Elements) != 1 ||
		output.Elements[0].Index != 4 || output.Elements[0].ElementType != knowledge.ElementTable ||
		output.Records[0].Route != RouteTable || output.Records[0].Usage == nil ||
		output.Records[0].Usage.TotalTokens != 15 {
		t.Fatalf("output = %+v visualCalled=%v tableCalled=%v", output, visualCalled, tableCalled)
	}
	var metadata struct {
		Method        string                `json:"method"`
		PromptVersion string                `json:"promptVersion"`
		Cells         []knowledgetable.Cell `json:"cells"`
	}
	if err := json.Unmarshal(output.Elements[0].Metadata, &metadata); err != nil ||
		metadata.Method != "table_recovery" || metadata.PromptVersion != "table-recovery-v1" ||
		len(metadata.Cells) != 2 {
		t.Fatalf("metadata = %+v err=%v", metadata, err)
	}
}

func TestOrchestratorMarksGenericVisualTableFallbackPartial(t *testing.T) {
	orchestrator, err := NewWithTableProcessor(
		Config{MaxEnrichments: 1, MinPixels: 1},
		processorFunc(func(_ context.Context, request Request) (ProviderResult, error) {
			if request.Route != RouteOCRVLM {
				t.Fatalf("request = %+v", request)
			}
			return ProviderResult{
				Provider: "dashscope", Model: "qwen3-vl-plus",
				Elements: []knowledge.DocumentElement{{
					ElementType: knowledge.ElementOCRText, ContentText: "alarm E42 count 3",
				}},
			}, nil
		}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	asset := visualAsset(0, knowledgeparser.VisualAssetLayoutRegion, "image/png", 100, 100)
	page := 1
	asset.PageNumber = &page
	asset.SourcePath = "pages/1/layout-regions/0"
	output, err := orchestrator.EnrichPlanned(context.Background(), Source{}, []PlannedAsset{{
		Asset: asset, Route: RouteTable, Reason: "table_structure_required",
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Partial || len(output.Elements) != 1 || output.Elements[0].ElementType != knowledge.ElementOCRText ||
		output.Records[0].Status != StatusPartial ||
		output.Records[0].Reason != "table_processor_unavailable_visual_fallback" {
		t.Fatalf("output = %+v", output)
	}
}

func TestProviderUsageRequiresTotalToCoverPromptAndCompletion(t *testing.T) {
	usage := ProviderUsage{PromptTokens: 60, CompletionTokens: 50, TotalTokens: 100}
	if err := usage.Validate(); err == nil {
		t.Fatal("usage with an undersized total was accepted")
	}
	usage.TotalTokens = 110
	if err := usage.Validate(); err != nil {
		t.Fatalf("valid usage rejected: %v", err)
	}
	usage = ProviderUsage{PromptTokens: math.MaxInt, CompletionTokens: 1, TotalTokens: math.MaxInt}
	if err := usage.Validate(); err == nil {
		t.Fatal("overflow-shaped usage was accepted")
	}
}

type processorFunc func(context.Context, Request) (ProviderResult, error)

func (f processorFunc) Process(ctx context.Context, request Request) (ProviderResult, error) {
	return f(ctx, request)
}

type tableProcessorFunc func(context.Context, knowledgetable.Request) (knowledgetable.Result, error)

func (f tableProcessorFunc) Recover(ctx context.Context, request knowledgetable.Request) (knowledgetable.Result, error) {
	return f(ctx, request)
}

func validTableResult() knowledgetable.Result {
	return knowledgetable.Result{
		Provider: "dashscope", Model: "qwen3-vl-plus", PromptVersion: "table-recovery-v1",
		Markdown: "| alarm | count |\n| --- | ---: |\n| E42 | 3 |",
		Cells: []knowledgetable.Cell{
			{Row: 0, Column: 0, RowSpan: 1, ColumnSpan: 1, Text: "alarm", Header: true},
			{Row: 1, Column: 0, RowSpan: 1, ColumnSpan: 1, Text: "E42"},
		},
		Confidence: 0.95, Usage: &knowledgetable.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

func visualAsset(index int, kind knowledgeparser.VisualAssetKind, mediaType string, width, height int) knowledgeparser.VisualAsset {
	content := []byte("visual")
	return knowledgeparser.VisualAsset{
		Index: index, Kind: kind, SourcePath: "media/image.png", MediaType: mediaType,
		SizeBytes: int64(len(content)), SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Width: width, Height: height, Content: content,
	}
}
