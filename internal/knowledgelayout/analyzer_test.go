package knowledgelayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestPageAnalyzerSkipsRenderingForConfirmedNativePage(t *testing.T) {
	called := false
	analyzer := testAnalyzer(t, rendererFunc(func(context.Context, RenderRequest) (RenderResult, error) {
		called = true
		return RenderResult{}, nil
	}), nil)
	result, err := analyzer.Analyze(context.Background(), AnalysisRequest{Page: PageInput{
		PageNumber: 1, VisualCandidatesKnown: true,
		NativeText: strongNativeSignals(),
	}})
	if err != nil || called || result.Plan.PageClass != PageNativeDigital || result.Render != nil {
		t.Fatalf("Analyze = %+v, %v, renderer called = %v", result, err, called)
	}
}

func TestPageAnalyzerRendersUnknownPDFBeforeLayoutDetection(t *testing.T) {
	source := testDocumentSource()
	raster := *testRaster()
	rasterDigest := sha256.Sum256(raster.Content)
	renderer := rendererFunc(func(_ context.Context, request RenderRequest) (RenderResult, error) {
		return RenderResult{
			PageNumber: request.PageNumber, RequestedDPI: request.DPI, DPI: request.DPI,
			SourceSHA256: request.Source.SHA256,
			RasterSHA256: hex.EncodeToString(rasterDigest[:]), Raster: raster,
			Renderer: RendererTrace{Provider: "pdfium", Name: "pdfium", Version: "pinned-v1"},
		}, nil
	})
	router := routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{Model: testModelTrace(), Regions: []DetectedRegion{{
			Type: RegionTable, Box: FullPageBox(), Confidence: 0.9,
		}}}, nil
	})
	analyzer := testAnalyzer(t, renderer, router)
	result, err := analyzer.Analyze(context.Background(), AnalysisRequest{
		Source: source, Page: PageInput{PageNumber: 1, NativeText: strongNativeSignals()},
	})
	if err != nil || result.Render == nil || result.Plan.PageClass != PageMixed ||
		result.Plan.Routes[0].Route != RouteTableRecovery {
		t.Fatalf("Analyze = %+v, %v", result, err)
	}
}

func TestPageAnalyzerRecoversNativeTextFromRenderer(t *testing.T) {
	source := testDocumentSource()
	raster := *testRaster()
	rasterDigest := sha256.Sum256(raster.Content)
	renderer := rendererFunc(func(_ context.Context, request RenderRequest) (RenderResult, error) {
		return RenderResult{
			PageNumber: request.PageNumber, RequestedDPI: request.DPI, DPI: request.DPI,
			SourceSHA256: request.Source.SHA256,
			RasterSHA256: hex.EncodeToString(rasterDigest[:]), Raster: raster,
			Renderer:                     RendererTrace{Provider: "pdfium", Name: "pdfium", Version: "pinned-v1"},
			NativeText:                   strings.TrimSpace(strings.Repeat("Recovered native PDF text. ", 12)),
			NativeTextExtractionComplete: true,
		}, nil
	})
	router := routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{Model: testModelTrace(), Regions: []DetectedRegion{{
			Type: RegionText, Box: FullPageBox(), Confidence: 0.9,
		}}}, nil
	})
	result, err := testAnalyzer(t, renderer, router).Analyze(context.Background(), AnalysisRequest{
		Source: source, Page: PageInput{PageNumber: 1},
	})
	if err != nil || result.RecoveredNativeText == "" || result.Plan.PageClass != PageNativeDigital ||
		result.Plan.Routes[0].Route != RouteNativeText {
		t.Fatalf("Analyze = %+v, %v", result, err)
	}
}

func TestPageAnalyzerRetainsNativeTextWhenRendererUnavailable(t *testing.T) {
	analyzer := testAnalyzer(t, rendererFunc(func(context.Context, RenderRequest) (RenderResult, error) {
		return RenderResult{}, ErrRendererUnavailable
	}), nil)
	result, err := analyzer.Analyze(context.Background(), AnalysisRequest{
		Source: testDocumentSource(),
		Page: PageInput{PageNumber: 1, NativeText: NativeTextSignals{
			RuneCount: 20, NonWhitespaceRunes: 12, PrintableRatio: 0.9,
		}},
	})
	if err != nil || !result.Plan.Partial || !result.Plan.Fallback ||
		result.Plan.Routes[0].Route != RouteNativeText ||
		result.Plan.Routes[0].Reason != ReasonRendererUnavailableNative {
		t.Fatalf("Analyze = %+v, %v", result, err)
	}
}

func TestPageAnalyzerRejectsScannedPageWhenRendererUnavailable(t *testing.T) {
	analyzer := testAnalyzer(t, nil, nil)
	_, err := analyzer.Analyze(context.Background(), AnalysisRequest{
		Page: PageInput{PageNumber: 1},
	})
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("Analyze error = %v", err)
	}
}

func TestPageAnalyzerRejectsRendererHashMismatch(t *testing.T) {
	analyzer := testAnalyzer(t, rendererFunc(func(_ context.Context, request RenderRequest) (RenderResult, error) {
		return RenderResult{
			PageNumber: request.PageNumber, RequestedDPI: request.DPI, DPI: request.DPI,
			SourceSHA256: request.Source.SHA256,
			RasterSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Raster:       *testRaster(), Renderer: RendererTrace{Provider: "pdfium", Name: "pdfium", Version: "v1"},
		}, nil
	}), nil)
	_, err := analyzer.Analyze(context.Background(), AnalysisRequest{
		Source: testDocumentSource(), Page: PageInput{PageNumber: 1},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Analyze error = %v", err)
	}
}

func testAnalyzer(t *testing.T, renderer PageRenderer, router LayoutRouter) *PageAnalyzer {
	t.Helper()
	planner := testPlanner(t, router)
	analyzer, err := NewPageAnalyzer(AnalyzerConfig{
		RenderDPI: 144, MaxSourceBytes: 1024,
		MaxRasterPixels: 20_000_000, MaxRasterBytes: 16 * 1024 * 1024,
	}, planner, renderer)
	if err != nil {
		t.Fatal(err)
	}
	return analyzer
}

func strongNativeSignals() NativeTextSignals {
	return NativeTextSignals{
		RuneCount: 200, NonWhitespaceRunes: 160, PrintableRatio: 0.99, ExtractionComplete: true,
	}
}

func testDocumentSource() DocumentSource {
	content := []byte("%PDF-test")
	digest := sha256.Sum256(content)
	return DocumentSource{
		MediaType: "application/pdf", Content: content, SHA256: hex.EncodeToString(digest[:]),
	}
}

type rendererFunc func(context.Context, RenderRequest) (RenderResult, error)

func (f rendererFunc) RenderPage(ctx context.Context, request RenderRequest) (RenderResult, error) {
	return f(ctx, request)
}
