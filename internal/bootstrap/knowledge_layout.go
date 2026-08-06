package bootstrap

import (
	"context"
	"errors"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/onnxlayout"
	"github.com/chitandabb/GoAgent/internal/platform/pdfiumrenderer"
)

type knowledgeLayoutRuntime struct {
	stage    *knowledgeingestion.LayoutStage
	router   *onnxlayout.Router
	renderer *pdfiumrenderer.Renderer
}

func openKnowledgeLayoutRuntime(
	ctx context.Context,
	cfg config.KnowledgeConfig,
) (*knowledgeLayoutRuntime, error) {
	layout := cfg.Layout
	if !layout.Enabled {
		return nil, nil
	}
	router, err := onnxlayout.New(onnxlayout.Config{
		RuntimeLibraryPath:                   layout.RuntimeLibraryPath,
		ModelPath:                            layout.ModelPath,
		ModelSHA256:                          layout.ModelSHA256,
		ManifestPath:                         layout.ManifestPath,
		Provider:                             layout.Provider,
		ModelName:                            layout.ModelName,
		ModelVersion:                         layout.ModelVersion,
		PreprocessVersion:                    layout.PreprocessVersion,
		PostprocessVersion:                   layout.PostprocessVersion,
		InputWidth:                           layout.InputWidth,
		InputHeight:                          layout.InputHeight,
		IntraOpThreads:                       layout.IntraOpThreads,
		InterOpThreads:                       layout.InterOpThreads,
		InferenceTimeout:                     time.Duration(layout.InferenceTimeoutMillis) * time.Millisecond,
		MaxConcurrentPages:                   layout.MaxConcurrentPages,
		MaxRegions:                           layout.MaxRegions,
		MaxRasterPixels:                      layout.MaxRasterPixels,
		MaxRasterBytes:                       layout.MaxRasterBytes,
		SuppressDecorativePictureDuplicates:  layout.SuppressDecorativePictureDuplicates,
		DecorativePictureMinIoU:              layout.DecorativePictureMinIoU,
		DecorativePictureMaxAreaRatio:        layout.DecorativePictureMaxAreaRatio,
		DecorativePictureMinConfidenceMargin: layout.DecorativePictureMinConfidenceMargin,
	})
	if err != nil {
		return nil, err
	}
	renderer, err := pdfiumrenderer.OpenWASM(ctx, pdfiumrenderer.Config{
		RendererVersion:   layout.RendererVersion,
		MaxSourceBytes:    cfg.MaxUploadBytes,
		MaxRasterPixels:   layout.MaxRasterPixels,
		MaxRasterBytes:    layout.MaxRasterBytes,
		MaxExtractedRunes: cfg.ParserMaxExtractedRunes,
		MaxConcurrent:     layout.MaxConcurrentPages,
		AcquireTimeout:    time.Duration(layout.RendererAcquireMillis) * time.Millisecond,
		RenderTimeout:     time.Duration(layout.RendererTimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		_ = router.Close()
		return nil, err
	}
	closeOnError := func(cause error) (*knowledgeLayoutRuntime, error) {
		return nil, errors.Join(cause, renderer.Close(), router.Close())
	}
	planner, err := knowledgelayout.NewRoutePlanner(knowledgelayout.PlannerConfig{
		MinNativeTextRunes:      layout.MinNativeTextRunes,
		MinNativePrintableRatio: layout.MinNativePrintableRatio,
		MinRegionConfidence:     layout.MinRegionConfidence,
		MaxRegions:              layout.MaxRegions,
		MaxRasterPixels:         layout.MaxRasterPixels,
		MaxRasterBytes:          layout.MaxRasterBytes,
	}, router)
	if err != nil {
		return closeOnError(err)
	}
	analyzer, err := knowledgelayout.NewPageAnalyzer(knowledgelayout.AnalyzerConfig{
		RenderDPI:       layout.RenderDPI,
		MaxSourceBytes:  cfg.MaxUploadBytes,
		MaxRasterPixels: layout.MaxRasterPixels,
		MaxRasterBytes:  layout.MaxRasterBytes,
	}, planner, renderer)
	if err != nil {
		return closeOnError(err)
	}
	stage, err := knowledgeingestion.NewLayoutStage(knowledgeingestion.LayoutStageConfig{
		MaxPages:             cfg.ParserMaxDocumentUnits,
		MaxActionableRegions: cfg.MaxVisualEnrichments,
		MaxTotalCropBytes:    cfg.ParserMaxTotalVisualBytes,
		MaxExtractedRunes:    cfg.ParserMaxExtractedRunes,
		CropConfig: knowledgelayout.CropConfig{
			PaddingRatio: layout.RegionPaddingRatio,
			MaxPixels:    layout.MaxRasterPixels,
			MaxBytes:     layout.MaxRasterBytes,
		},
	}, analyzer)
	if err != nil {
		return closeOnError(err)
	}
	return &knowledgeLayoutRuntime{stage: stage, router: router, renderer: renderer}, nil
}

func (r *knowledgeLayoutRuntime) Close() error {
	if r == nil {
		return nil
	}
	var rendererErr, routerErr error
	if r.renderer != nil {
		rendererErr = r.renderer.Close()
	}
	if r.router != nil {
		routerErr = r.router.Close()
	}
	return errors.Join(rendererErr, routerErr)
}
