package knowledgelayout

import (
	"context"
	"errors"
	"testing"
)

func TestRoutePlannerUsesNativeFastPathWithoutRouter(t *testing.T) {
	called := false
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		called = true
		return DetectionResult{}, nil
	}))
	plan, err := planner.Plan(context.Background(), PageInput{
		PageNumber: 1, VisualCandidatesKnown: true,
		NativeText: NativeTextSignals{
			RuneCount: 200, NonWhitespaceRunes: 160, PrintableRatio: 0.99, ExtractionComplete: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || plan.PageClass != PageNativeDigital || plan.DetectorUsed || plan.Fallback || plan.Partial ||
		len(plan.Routes) != 1 || plan.Routes[0].Route != RouteNativeText ||
		plan.Routes[0].Reason != ReasonNativeTextFastPath {
		t.Fatalf("plan = %+v, called = %v", plan, called)
	}
}

func TestRoutePlannerRoutesMixedRegionsDeterministically(t *testing.T) {
	router := routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{
			Model: testModelTrace(),
			Regions: []DetectedRegion{
				{Type: RegionPicture, Box: BoundingBox{Left: 0.5, Top: 0.5, Right: 0.9, Bottom: 0.9}, Confidence: 0.9},
				{Type: RegionText, Box: BoundingBox{Left: 0.1, Top: 0.1, Right: 0.9, Bottom: 0.2}, Confidence: 0.95},
				{Type: RegionTable, Box: BoundingBox{Left: 0.1, Top: 0.3, Right: 0.9, Bottom: 0.45}, Confidence: 0.88},
				{Type: RegionDecorative, Box: BoundingBox{Left: 0.05, Top: 0.95, Right: 0.1, Bottom: 0.99}, Confidence: 0.99},
			},
		}, nil
	})
	planner := testPlanner(t, router)
	plan, err := planner.Plan(context.Background(), PageInput{
		PageNumber: 1, VisualCandidateCount: 1, VisualCandidatesKnown: true,
		NativeText: NativeTextSignals{
			RuneCount: 200, NonWhitespaceRunes: 160, PrintableRatio: 0.99, ExtractionComplete: true,
		},
		Raster: testRaster(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRoutes := []ProcessingRoute{RouteNativeText, RouteTableRecovery, RouteCloudVision, RouteSkip}
	if plan.PageClass != PageMixed || !plan.DetectorUsed || plan.Model == nil || len(plan.Routes) != len(wantRoutes) {
		t.Fatalf("plan = %+v", plan)
	}
	for index, want := range wantRoutes {
		if plan.Routes[index].Ordinal != index || plan.Routes[index].Route != want {
			t.Fatalf("route[%d] = %+v, want %s", index, plan.Routes[index], want)
		}
	}
}

func TestRoutePlannerClassifiesDetectedNativeTextOnlyPage(t *testing.T) {
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{
			Model: testModelTrace(),
			Regions: []DetectedRegion{{
				Type: RegionText, Box: FullPageBox(), Confidence: 0.95,
			}},
		}, nil
	}))
	plan, err := planner.Plan(context.Background(), PageInput{
		PageNumber: 1,
		NativeText: NativeTextSignals{
			RuneCount: 200, NonWhitespaceRunes: 160, PrintableRatio: 0.99, ExtractionComplete: true,
		},
		Raster: testRaster(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PageClass != PageNativeDigital || len(plan.Routes) != 1 || plan.Routes[0].Route != RouteNativeText {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRoutePlannerUsesConservativeFallbackForLowConfidenceRegion(t *testing.T) {
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{
			Model: testModelTrace(),
			Regions: []DetectedRegion{{
				Type: RegionDecorative, Box: FullPageBox(), Confidence: 0.49,
			}},
		}, nil
	}))
	plan, err := planner.Plan(context.Background(), PageInput{PageNumber: 1, Raster: testRaster()})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PageClass != PageScanned || plan.Routes[0].Route != RouteCloudOCR ||
		plan.Routes[0].Reason != ReasonLowConfidenceOCRFallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRoutePlannerDoesNotEscalateLowConfidenceTextToVision(t *testing.T) {
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{Model: testModelTrace(), Regions: []DetectedRegion{
			{Type: RegionText, Box: FullPageBox(), Confidence: 0.49},
		}}, nil
	}))
	plan, err := planner.Plan(context.Background(), PageInput{PageNumber: 1, Raster: testRaster()})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Routes[0].Route != RouteCloudOCR || plan.Routes[0].Reason != ReasonLowConfidenceOCRFallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRoutePlannerFallsBackWhenRouterIsUnavailable(t *testing.T) {
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		return DetectionResult{}, ErrRouterUnavailable
	}))
	tests := []struct {
		name       string
		native     NativeTextSignals
		wantClass  PageClass
		wantRoute  ProcessingRoute
		wantReason ReasonCode
		partial    bool
	}{
		{
			name: "native text retained",
			native: NativeTextSignals{
				RuneCount: 20, NonWhitespaceRunes: 12, PrintableRatio: 0.9, ExtractionComplete: false,
			},
			wantClass: PageMixed, wantRoute: RouteNativeText,
			wantReason: ReasonRouterUnavailableNative, partial: true,
		},
		{
			name:      "scanned page OCR",
			wantClass: PageScanned, wantRoute: RouteCloudOCR,
			wantReason: ReasonRouterUnavailableOCR,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.Plan(context.Background(), PageInput{
				PageNumber: 1, NativeText: test.native, Raster: testRaster(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Fallback || plan.Partial != test.partial || plan.PageClass != test.wantClass ||
				plan.Routes[0].Route != test.wantRoute || plan.Routes[0].Reason != test.wantReason {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

func TestRoutePlannerRejectsInvalidModelOutput(t *testing.T) {
	planner := testPlanner(t, routerFunc(func(context.Context, RasterPage) (DetectionResult, error) {
		result := DetectionResult{Model: testModelTrace(), Regions: []DetectedRegion{{
			Type: RegionText, Box: BoundingBox{Left: 0.8, Top: 0.1, Right: 0.2, Bottom: 0.9}, Confidence: 0.9,
		}}}
		return result, nil
	}))
	_, err := planner.Plan(context.Background(), PageInput{PageNumber: 1, Raster: testRaster()})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Plan error = %v", err)
	}
}

func TestRoutePlannerRequiresRasterWhenVisualCandidatesAreUnknown(t *testing.T) {
	planner := testPlanner(t, nil)
	input := PageInput{
		PageNumber: 1,
		NativeText: NativeTextSignals{
			RuneCount: 200, NonWhitespaceRunes: 160, PrintableRatio: 0.99, ExtractionComplete: true,
		},
	}
	required, err := planner.RequiresRaster(input)
	if err != nil || !required {
		t.Fatalf("RequiresRaster = %v, %v", required, err)
	}
	input.VisualCandidatesKnown = true
	required, err = planner.RequiresRaster(input)
	if err != nil || required {
		t.Fatalf("RequiresRaster confirmed text-only = %v, %v", required, err)
	}
}

func testPlanner(t *testing.T, router LayoutRouter) *RoutePlanner {
	t.Helper()
	planner, err := NewRoutePlanner(PlannerConfig{
		MinNativeTextRunes: 64, MinNativePrintableRatio: 0.85,
		MinRegionConfidence: 0.65, MaxRegions: 256,
		MaxRasterPixels: 20_000_000, MaxRasterBytes: 16 * 1024 * 1024,
	}, router)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func testRaster() *RasterPage {
	return &RasterPage{MediaType: "image/png", Width: 100, Height: 200, Content: []byte("png")}
}

func testModelTrace() ModelTrace {
	return ModelTrace{
		Provider: "onnxruntime", Name: "layout-model", Version: "v1",
		SHA256:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PreprocessVersion: "layout-pre-v1", PostprocessVersion: "layout-post-v1",
	}
}

type routerFunc func(context.Context, RasterPage) (DetectionResult, error)

func (f routerFunc) Detect(ctx context.Context, page RasterPage) (DetectionResult, error) {
	return f(ctx, page)
}
