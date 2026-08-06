package knowledgelayout

import (
	"context"
	"errors"
	"sort"
)

type ProcessingRoute string

const (
	RouteNativeText    ProcessingRoute = "native_text"
	RouteCloudOCR      ProcessingRoute = "cloud_ocr"
	RouteTableRecovery ProcessingRoute = "table_recovery"
	RouteCloudVision   ProcessingRoute = "cloud_vision"
	RouteSkip          ProcessingRoute = "skip"
)

func (r ProcessingRoute) Valid() bool {
	switch r {
	case RouteNativeText, RouteCloudOCR, RouteTableRecovery, RouteCloudVision, RouteSkip:
		return true
	default:
		return false
	}
}

type ReasonCode string

const (
	ReasonNativeTextFastPath          ReasonCode = "native_text_fast_path"
	ReasonNativeTextRegion            ReasonCode = "native_text_region"
	ReasonScannedTextRegion           ReasonCode = "scanned_text_region"
	ReasonTableStructureRequired      ReasonCode = "table_structure_required"
	ReasonVisualSemanticsRequired     ReasonCode = "visual_semantics_required"
	ReasonFormulaSemanticsRequired    ReasonCode = "formula_semantics_required"
	ReasonDecorativeRegion            ReasonCode = "decorative_region"
	ReasonLowConfidenceNativeFallback ReasonCode = "low_confidence_native_fallback"
	ReasonLowConfidenceOCRFallback    ReasonCode = "low_confidence_ocr_fallback"
	ReasonLowConfidenceDecorativeSkip ReasonCode = "low_confidence_decorative_skipped"
	ReasonLowConfidenceVisualFallback ReasonCode = "low_confidence_visual_fallback"
	ReasonRouterUnavailableNative     ReasonCode = "router_unavailable_native_retained"
	ReasonRendererUnavailableNative   ReasonCode = "renderer_unavailable_native_retained"
	ReasonRouterUnavailableOCR        ReasonCode = "router_unavailable_page_ocr"
	ReasonNoRegionsNative             ReasonCode = "no_regions_native_retained"
	ReasonNoRegionsOCR                ReasonCode = "no_regions_page_ocr"
)

func (c ReasonCode) Valid() bool {
	switch c {
	case ReasonNativeTextFastPath, ReasonNativeTextRegion, ReasonScannedTextRegion,
		ReasonTableStructureRequired, ReasonVisualSemanticsRequired,
		ReasonFormulaSemanticsRequired, ReasonDecorativeRegion,
		ReasonLowConfidenceNativeFallback, ReasonLowConfidenceOCRFallback,
		ReasonLowConfidenceDecorativeSkip,
		ReasonLowConfidenceVisualFallback, ReasonRouterUnavailableNative,
		ReasonRendererUnavailableNative, ReasonRouterUnavailableOCR,
		ReasonNoRegionsNative, ReasonNoRegionsOCR:
		return true
	default:
		return false
	}
}

type PlannerConfig struct {
	MinNativeTextRunes      int
	MinNativePrintableRatio float64
	MinRegionConfidence     float64
	MaxRegions              int
	MaxRasterPixels         int64
	MaxRasterBytes          int64
}

func (c PlannerConfig) Validate() error {
	if c.MinNativeTextRunes < 1 || c.MinNativeTextRunes > 1_000_000 ||
		c.MinNativePrintableRatio <= 0 || c.MinNativePrintableRatio > 1 ||
		c.MinRegionConfidence <= 0 || c.MinRegionConfidence > 1 ||
		c.MaxRegions < 1 || c.MaxRegions > 10_000 ||
		c.MaxRasterPixels < 1 || c.MaxRasterPixels > 1_000_000_000 ||
		c.MaxRasterBytes < 1 || c.MaxRasterBytes > 256*1024*1024 {
		return ErrInvalidInput
	}
	return nil
}

type RegionRoute struct {
	Ordinal    int
	RegionType RegionType
	Box        BoundingBox
	Confidence float64
	Route      ProcessingRoute
	Reason     ReasonCode
}

func (r RegionRoute) Validate() error {
	if r.Ordinal < 0 || !r.RegionType.Valid() || !r.Route.Valid() || !r.Reason.Valid() {
		return ErrInvalidInput
	}
	region := DetectedRegion{Type: r.RegionType, Box: r.Box, Confidence: r.Confidence}
	return region.Validate()
}

type Plan struct {
	PageClass    PageClass
	DetectorUsed bool
	Fallback     bool
	Partial      bool
	Model        *ModelTrace
	Routes       []RegionRoute
}

func (p Plan) Validate() error {
	if !p.PageClass.Valid() || len(p.Routes) == 0 {
		return ErrInvalidInput
	}
	if p.DetectorUsed != (p.Model != nil) {
		return ErrInvalidInput
	}
	if p.Model != nil {
		if err := p.Model.Validate(); err != nil {
			return err
		}
	}
	for index, route := range p.Routes {
		if route.Ordinal != index {
			return ErrInvalidInput
		}
		if err := route.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type RoutePlanner struct {
	config PlannerConfig
	router LayoutRouter
}

func NewRoutePlanner(config PlannerConfig, router LayoutRouter) (*RoutePlanner, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &RoutePlanner{config: config, router: router}, nil
}

func (p *RoutePlanner) Plan(ctx context.Context, input PageInput) (Plan, error) {
	if p == nil {
		return Plan{}, ErrRouterUnavailable
	}
	if err := input.Validate(); err != nil {
		return Plan{}, err
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	strongNative := p.strongNative(input.NativeText)
	usableNative := p.usableNative(input.NativeText)
	if strongNative && input.VisualCandidatesKnown && input.VisualCandidateCount == 0 {
		return validatedPlan(singleRoutePlan(PageNativeDigital, RouteNativeText, ReasonNativeTextFastPath, false, false))
	}
	if input.Raster == nil {
		if usableNative {
			return validatedPlan(singleRoutePlan(PageMixed, RouteNativeText, ReasonRouterUnavailableNative, true, true))
		}
		return Plan{}, ErrInvalidInput
	}
	if err := input.Raster.Validate(p.config.MaxRasterPixels, p.config.MaxRasterBytes); err != nil {
		return Plan{}, err
	}
	if p.router == nil {
		return p.fallbackPlan(usableNative, ReasonRouterUnavailableNative, ReasonRouterUnavailableOCR)
	}
	detected, err := p.router.Detect(ctx, *input.Raster)
	if err != nil {
		if ctx.Err() != nil {
			return Plan{}, ctx.Err()
		}
		if errors.Is(err, ErrRouterUnavailable) {
			return p.fallbackPlan(usableNative, ReasonRouterUnavailableNative, ReasonRouterUnavailableOCR)
		}
		return Plan{}, err
	}
	if err := detected.Validate(p.config.MaxRegions); err != nil {
		return Plan{}, err
	}
	if len(detected.Regions) == 0 {
		plan, fallbackErr := p.fallbackPlan(usableNative, ReasonNoRegionsNative, ReasonNoRegionsOCR)
		if fallbackErr != nil {
			return Plan{}, fallbackErr
		}
		plan.DetectorUsed = true
		plan.Model = cloneModelTrace(detected.Model)
		return validatedPlan(plan)
	}

	regions := append([]DetectedRegion(nil), detected.Regions...)
	sort.SliceStable(regions, func(i, j int) bool {
		left, right := regions[i], regions[j]
		if left.Box.Top != right.Box.Top {
			return left.Box.Top < right.Box.Top
		}
		if left.Box.Left != right.Box.Left {
			return left.Box.Left < right.Box.Left
		}
		if left.Box.Bottom != right.Box.Bottom {
			return left.Box.Bottom < right.Box.Bottom
		}
		if left.Box.Right != right.Box.Right {
			return left.Box.Right < right.Box.Right
		}
		return left.Type < right.Type
	})
	routes := make([]RegionRoute, 0, len(regions))
	hasNonSkipRoute := false
	for index, region := range regions {
		route, reason := p.routeRegion(region, strongNative)
		if route != RouteSkip {
			hasNonSkipRoute = true
		}
		routes = append(routes, RegionRoute{
			Ordinal: index, RegionType: region.Type, Box: region.Box,
			Confidence: region.Confidence, Route: route, Reason: reason,
		})
	}
	if !hasNonSkipRoute && !usableNative {
		routes = []RegionRoute{{
			Ordinal: 0, RegionType: RegionText, Box: FullPageBox(), Confidence: 1,
			Route: RouteCloudOCR, Reason: ReasonLowConfidenceOCRFallback,
		}}
	}
	pageClass := PageScanned
	if usableNative {
		pageClass = PageNativeDigital
		for _, route := range routes {
			if route.Route != RouteNativeText && route.Route != RouteSkip {
				pageClass = PageMixed
				break
			}
		}
	}
	return validatedPlan(Plan{
		PageClass: pageClass, DetectorUsed: true, Model: cloneModelTrace(detected.Model), Routes: routes,
	})
}

// RequiresRaster distinguishes a confirmed text-only fast path from a page
// whose visual-candidate state is unknown. Unknown is intentionally not treated
// as zero because doing so can silently miss mixed PDF pages.
func (p *RoutePlanner) RequiresRaster(input PageInput) (bool, error) {
	if p == nil {
		return false, ErrRouterUnavailable
	}
	if err := input.Validate(); err != nil {
		return false, err
	}
	return !(p.strongNative(input.NativeText) && input.VisualCandidatesKnown &&
		input.VisualCandidateCount == 0), nil
}

func (p *RoutePlanner) strongNative(signals NativeTextSignals) bool {
	return signals.ExtractionComplete &&
		signals.NonWhitespaceRunes >= p.config.MinNativeTextRunes &&
		signals.PrintableRatio >= p.config.MinNativePrintableRatio
}

func (p *RoutePlanner) usableNative(signals NativeTextSignals) bool {
	return signals.NonWhitespaceRunes > 0 && signals.PrintableRatio >= 0.5
}

func (p *RoutePlanner) routeRegion(region DetectedRegion, strongNative bool) (ProcessingRoute, ReasonCode) {
	if region.Confidence < p.config.MinRegionConfidence {
		switch region.Type {
		case RegionText, RegionCaption, RegionTable:
			if strongNative {
				return RouteNativeText, ReasonLowConfidenceNativeFallback
			}
			return RouteCloudOCR, ReasonLowConfidenceOCRFallback
		case RegionDecorative:
			return RouteSkip, ReasonLowConfidenceDecorativeSkip
		case RegionPicture, RegionFormula:
			return RouteCloudVision, ReasonLowConfidenceVisualFallback
		default:
			return RouteCloudVision, ReasonLowConfidenceVisualFallback
		}
	}
	switch region.Type {
	case RegionText, RegionCaption:
		if strongNative {
			return RouteNativeText, ReasonNativeTextRegion
		}
		return RouteCloudOCR, ReasonScannedTextRegion
	case RegionTable:
		return RouteTableRecovery, ReasonTableStructureRequired
	case RegionFormula:
		return RouteCloudVision, ReasonFormulaSemanticsRequired
	case RegionPicture:
		return RouteCloudVision, ReasonVisualSemanticsRequired
	case RegionDecorative:
		return RouteSkip, ReasonDecorativeRegion
	default:
		return RouteCloudVision, ReasonLowConfidenceVisualFallback
	}
}

func (p *RoutePlanner) fallbackPlan(usableNative bool, nativeReason, ocrReason ReasonCode) (Plan, error) {
	if usableNative {
		return validatedPlan(singleRoutePlan(PageMixed, RouteNativeText, nativeReason, true, true))
	}
	return validatedPlan(singleRoutePlan(PageScanned, RouteCloudOCR, ocrReason, true, false))
}

func singleRoutePlan(pageClass PageClass, route ProcessingRoute, reason ReasonCode, fallback, partial bool) Plan {
	return Plan{
		PageClass: pageClass, Fallback: fallback, Partial: partial,
		Routes: []RegionRoute{{
			Ordinal: 0, RegionType: RegionText, Box: FullPageBox(), Confidence: 1,
			Route: route, Reason: reason,
		}},
	}
}

func cloneModelTrace(model ModelTrace) *ModelTrace {
	cloned := model
	return &cloned
}

func validatedPlan(plan Plan) (Plan, error) {
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}
