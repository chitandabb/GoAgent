package knowledgeingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
)

func TestLayoutStageCropsOnlyActionableImageRegions(t *testing.T) {
	raster := ingestionLayoutRasterFixture(t, 100, 80)
	asset := knowledgeparser.VisualAsset{
		Index: 0, Kind: knowledgeparser.VisualAssetSourceImage, SourcePath: "source",
		MediaType: raster.MediaType, SizeBytes: int64(len(raster.Content)),
		SHA256: rasterSHA256(raster.Content), Width: raster.Width, Height: raster.Height,
		Content: raster.Content,
	}
	parsed := knowledgeparser.Result{
		ParserVersion: "test-v1", VisualAssets: []knowledgeparser.VisualAsset{asset},
		Pages: []knowledgeparser.PageObservation{{
			PageNumber: 1, ExtractionComplete: true,
			VisualCandidateCount: 1, VisualCandidatesKnown: true,
		}},
		Metadata: json.RawMessage("{\"mediaType\":\"image/png\"}"),
	}
	analyzer := pageAnalyzerFunc(func(
		context.Context, knowledgelayout.AnalysisRequest,
	) (knowledgelayout.AnalysisResult, error) {
		return knowledgelayout.AnalysisResult{Plan: knowledgelayout.Plan{
			PageClass: knowledgelayout.PageScanned,
			Routes: []knowledgelayout.RegionRoute{
				testLayoutRoute(0, knowledgelayout.RegionText, knowledgelayout.RouteCloudOCR),
				testLayoutRoute(1, knowledgelayout.RegionPicture, knowledgelayout.RouteCloudVision),
				testLayoutRoute(2, knowledgelayout.RegionDecorative, knowledgelayout.RouteSkip),
			},
		}}, nil
	})
	stage := testLayoutStage(t, analyzer)
	output, err := stage.Analyze(context.Background(), knowledgeparser.Input{
		MediaType: "image/png", OriginalName: "dashboard.png", Content: raster.Content,
	}, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Pages) != 1 || len(output.Pages[0].Regions) != 3 ||
		output.Pages[0].Regions[0].Crop == nil || output.Pages[0].Regions[1].Crop == nil ||
		output.Pages[0].Regions[2].Crop != nil {
		t.Fatalf("output = %+v", output)
	}
}

func TestLayoutStagePassesPDFPageObservationsToAnalyzer(t *testing.T) {
	sourceContent := []byte("%PDF-test")
	pageNumber := 1
	parsed := knowledgeparser.Result{
		ParserVersion: "test-v1",
		Pages: []knowledgeparser.PageObservation{{
			PageNumber: 1, NativeTextRunes: 100, NonWhitespaceRunes: 80,
			PrintableRatio: 1, ExtractionComplete: true,
		}},
		VisualAssets: []knowledgeparser.VisualAsset{{
			Index: 0, Kind: knowledgeparser.VisualAssetDocumentPage, PageNumber: &pageNumber,
			SourcePath: "pages/1", MediaType: "application/pdf",
			SizeBytes: int64(len(sourceContent)), SHA256: rasterSHA256(sourceContent),
		}},
		Metadata: json.RawMessage("{\"mediaType\":\"application/pdf\"}"),
	}
	called := false
	analyzer := pageAnalyzerFunc(func(
		_ context.Context, request knowledgelayout.AnalysisRequest,
	) (knowledgelayout.AnalysisResult, error) {
		called = true
		if request.Source.MediaType != "application/pdf" || request.Source.SHA256 == "" ||
			request.Page.NativeText.RuneCount != 100 || request.Page.VisualCandidatesKnown {
			t.Fatalf("request = %+v", request)
		}
		return knowledgelayout.AnalysisResult{Plan: knowledgelayout.Plan{
			PageClass: knowledgelayout.PageMixed, Fallback: true, Partial: true,
			Routes: []knowledgelayout.RegionRoute{testLayoutRoute(
				0, knowledgelayout.RegionText, knowledgelayout.RouteNativeText,
			)},
		}}, nil
	})
	stage := testLayoutStage(t, analyzer)
	output, err := stage.Analyze(context.Background(), knowledgeparser.Input{
		MediaType: "application/pdf", OriginalName: "manual.pdf", Content: sourceContent,
	}, parsed)
	if err != nil || !called || len(output.Pages) != 1 || output.Pages[0].Regions[0].Crop != nil {
		t.Fatalf("Analyze = %+v, %v, called = %v", output, err, called)
	}
}

func TestLayoutStageBoundsDocumentLevelCropBudget(t *testing.T) {
	raster := ingestionLayoutRasterFixture(t, 100, 80)
	asset := knowledgeparser.VisualAsset{
		Index: 0, Kind: knowledgeparser.VisualAssetSourceImage, SourcePath: "source",
		MediaType: raster.MediaType, SizeBytes: int64(len(raster.Content)),
		SHA256: rasterSHA256(raster.Content), Width: raster.Width, Height: raster.Height,
		Content: raster.Content,
	}
	parsed := knowledgeparser.Result{
		ParserVersion: "test-v1", VisualAssets: []knowledgeparser.VisualAsset{asset},
		Pages: []knowledgeparser.PageObservation{{
			PageNumber: 1, ExtractionComplete: true,
			VisualCandidateCount: 1, VisualCandidatesKnown: true,
		}},
		Metadata: json.RawMessage("{\"mediaType\":\"image/png\"}"),
	}
	analyzer := pageAnalyzerFunc(func(
		context.Context, knowledgelayout.AnalysisRequest,
	) (knowledgelayout.AnalysisResult, error) {
		return knowledgelayout.AnalysisResult{Plan: knowledgelayout.Plan{
			PageClass: knowledgelayout.PageScanned,
			Routes: []knowledgelayout.RegionRoute{
				testLayoutRoute(0, knowledgelayout.RegionText, knowledgelayout.RouteCloudOCR),
				testLayoutRoute(1, knowledgelayout.RegionPicture, knowledgelayout.RouteCloudVision),
			},
		}}, nil
	})
	stage, err := NewLayoutStage(LayoutStageConfig{
		MaxPages: 10, MaxActionableRegions: 1, MaxTotalCropBytes: 16 * 1024 * 1024,
		MaxExtractedRunes: 10_000,
		CropConfig: knowledgelayout.CropConfig{
			PaddingRatio: 0.01, MaxPixels: 20_000_000, MaxBytes: 16 * 1024 * 1024,
		},
	}, analyzer)
	if err != nil {
		t.Fatal(err)
	}
	output, err := stage.Analyze(context.Background(), knowledgeparser.Input{
		MediaType: "image/png", OriginalName: "dashboard.png", Content: raster.Content,
	}, parsed)
	if err != nil {
		t.Fatal(err)
	}
	regions := output.Pages[0].Regions
	if regions[0].Crop == nil || regions[1].Crop != nil ||
		regions[1].SuppressedReason != "layout_region_count_budget_exceeded" ||
		!layoutIsPartial(&output) {
		t.Fatalf("regions = %+v", regions)
	}
}

func testLayoutStage(t *testing.T, analyzer PageLayoutAnalyzer) *LayoutStage {
	t.Helper()
	stage, err := NewLayoutStage(LayoutStageConfig{
		MaxPages: 10, MaxActionableRegions: 10, MaxTotalCropBytes: 16 * 1024 * 1024,
		MaxExtractedRunes: 10_000,
		CropConfig: knowledgelayout.CropConfig{
			PaddingRatio: 0.01, MaxPixels: 20_000_000, MaxBytes: 16 * 1024 * 1024,
		},
	}, analyzer)
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func testLayoutRoute(
	ordinal int,
	regionType knowledgelayout.RegionType,
	route knowledgelayout.ProcessingRoute,
) knowledgelayout.RegionRoute {
	reason := knowledgelayout.ReasonVisualSemanticsRequired
	switch route {
	case knowledgelayout.RouteCloudOCR:
		reason = knowledgelayout.ReasonScannedTextRegion
	case knowledgelayout.RouteTableRecovery:
		reason = knowledgelayout.ReasonTableStructureRequired
	case knowledgelayout.RouteNativeText:
		reason = knowledgelayout.ReasonNativeTextRegion
	case knowledgelayout.RouteSkip:
		reason = knowledgelayout.ReasonDecorativeRegion
	}
	return knowledgelayout.RegionRoute{
		Ordinal: ordinal, RegionType: regionType, Box: knowledgelayout.FullPageBox(),
		Confidence: 0.9, Route: route, Reason: reason,
	}
}

type pageAnalyzerFunc func(
	context.Context,
	knowledgelayout.AnalysisRequest,
) (knowledgelayout.AnalysisResult, error)

func (f pageAnalyzerFunc) Analyze(
	ctx context.Context,
	request knowledgelayout.AnalysisRequest,
) (knowledgelayout.AnalysisResult, error) {
	return f(ctx, request)
}

func ingestionLayoutRasterFixture(t *testing.T, width, height int) knowledgelayout.RasterPage {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return knowledgelayout.RasterPage{
		MediaType: "image/png", Width: width, Height: height, Content: encoded.Bytes(),
	}
}

func rasterSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
