package knowledgelayout

import (
	"context"
	"errors"
)

type AnalyzerConfig struct {
	RenderDPI       int
	MaxSourceBytes  int64
	MaxRasterPixels int64
	MaxRasterBytes  int64
}

func (c AnalyzerConfig) Validate() error {
	if c.RenderDPI < 72 || c.RenderDPI > 600 || c.MaxSourceBytes < 1 ||
		c.MaxRasterPixels < 1 || c.MaxRasterPixels > 1_000_000_000 ||
		c.MaxRasterBytes < 1 || c.MaxRasterBytes > 256*1024*1024 {
		return ErrInvalidInput
	}
	return nil
}

type AnalysisRequest struct {
	Source DocumentSource
	Page   PageInput
}

type AnalysisResult struct {
	Plan                Plan
	Render              *RenderResult
	RecoveredNativeText string
}

type PageAnalyzer struct {
	config   AnalyzerConfig
	planner  *RoutePlanner
	renderer PageRenderer
}

func NewPageAnalyzer(config AnalyzerConfig, planner *RoutePlanner, renderer PageRenderer) (*PageAnalyzer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if planner == nil {
		return nil, ErrRouterUnavailable
	}
	return &PageAnalyzer{config: config, planner: planner, renderer: renderer}, nil
}

func (a *PageAnalyzer) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResult, error) {
	if a == nil || a.planner == nil {
		return AnalysisResult{}, ErrRouterUnavailable
	}
	if err := ctx.Err(); err != nil {
		return AnalysisResult{}, err
	}
	requiresRaster, err := a.planner.RequiresRaster(request.Page)
	if err != nil {
		return AnalysisResult{}, err
	}
	if !requiresRaster || request.Page.Raster != nil {
		plan, planErr := a.planner.Plan(ctx, request.Page)
		return AnalysisResult{Plan: plan}, planErr
	}
	if a.renderer == nil {
		return a.fallbackWithoutRenderer(request.Page)
	}
	renderRequest := RenderRequest{
		Source: request.Source, PageNumber: request.Page.PageNumber, DPI: a.config.RenderDPI,
	}
	if err := renderRequest.Validate(a.config.MaxSourceBytes); err != nil {
		return AnalysisResult{}, err
	}
	rendered, err := a.renderer.RenderPage(ctx, renderRequest)
	if err != nil {
		if ctx.Err() != nil {
			return AnalysisResult{}, ctx.Err()
		}
		if errors.Is(err, ErrRendererUnavailable) {
			return a.fallbackWithoutRenderer(request.Page)
		}
		return AnalysisResult{}, err
	}
	if err := rendered.Validate(renderRequest, a.config.MaxRasterPixels, a.config.MaxRasterBytes); err != nil {
		return AnalysisResult{}, err
	}
	request.Page.Raster = &rendered.Raster
	recoveredNativeText := ""
	if !a.planner.usableNative(request.Page.NativeText) && rendered.NativeText != "" {
		signals := ObserveNativeText(rendered.NativeText, rendered.NativeTextExtractionComplete)
		if a.planner.usableNative(signals) {
			request.Page.NativeText = signals
			recoveredNativeText = rendered.NativeText
		}
	}
	plan, err := a.planner.Plan(ctx, request.Page)
	if err != nil {
		return AnalysisResult{}, err
	}
	cloned := rendered
	return AnalysisResult{Plan: plan, Render: &cloned, RecoveredNativeText: recoveredNativeText}, nil
}

func (a *PageAnalyzer) fallbackWithoutRenderer(input PageInput) (AnalysisResult, error) {
	if !a.planner.usableNative(input.NativeText) {
		return AnalysisResult{}, ErrRendererUnavailable
	}
	plan, err := validatedPlan(singleRoutePlan(
		PageMixed, RouteNativeText, ReasonRendererUnavailableNative, true, true,
	))
	return AnalysisResult{Plan: plan}, err
}
