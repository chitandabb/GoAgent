package knowledgeingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
)

type PageLayoutAnalyzer interface {
	Analyze(context.Context, knowledgelayout.AnalysisRequest) (knowledgelayout.AnalysisResult, error)
}

type LayoutStageConfig struct {
	MaxPages             int
	MaxActionableRegions int
	MaxTotalCropBytes    int64
	MaxExtractedRunes    int
	CropConfig           knowledgelayout.CropConfig
}

func (c LayoutStageConfig) Validate() error {
	if c.MaxPages < 1 || c.MaxPages > 10_000 ||
		c.MaxActionableRegions < 1 || c.MaxActionableRegions > 10_000 ||
		c.MaxTotalCropBytes < 1 || c.MaxTotalCropBytes > 1024*1024*1024 ||
		c.MaxExtractedRunes < 1 || c.MaxExtractedRunes > 100_000_000 {
		return knowledgelayout.ErrInvalidInput
	}
	return c.CropConfig.Validate()
}

type LayoutRegion struct {
	Route            knowledgelayout.RegionRoute
	Crop             *knowledgelayout.CropResult
	AssetIndex       *int
	SuppressedReason string
}

type LayoutPage struct {
	PageNumber          int
	Plan                knowledgelayout.Plan
	Render              *knowledgelayout.RenderResult
	RecoveredNativeText string
	Regions             []LayoutRegion
}

type LayoutOutput struct {
	Pages []LayoutPage
}

type LayoutStage struct {
	config   LayoutStageConfig
	analyzer PageLayoutAnalyzer
}

func NewLayoutStage(config LayoutStageConfig, analyzer PageLayoutAnalyzer) (*LayoutStage, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if analyzer == nil {
		return nil, knowledgelayout.ErrRouterUnavailable
	}
	return &LayoutStage{config: config, analyzer: analyzer}, nil
}

func (s *LayoutStage) Analyze(
	ctx context.Context,
	source knowledgeparser.Input,
	parsed knowledgeparser.Result,
) (LayoutOutput, error) {
	if s == nil || s.analyzer == nil {
		return LayoutOutput{}, knowledgelayout.ErrRouterUnavailable
	}
	if err := ctx.Err(); err != nil {
		return LayoutOutput{}, err
	}
	if err := source.Validate(); err != nil {
		return LayoutOutput{}, err
	}
	if err := parsed.Validate(); err != nil {
		return LayoutOutput{}, err
	}
	if len(parsed.Pages) > s.config.MaxPages {
		return LayoutOutput{}, knowledgelayout.ErrInvalidInput
	}
	documentSource := layoutDocumentSource(source)
	sourceRaster, err := layoutSourceRaster(
		source.MediaType, parsed.VisualAssets, s.config.CropConfig.MaxPixels, s.config.CropConfig.MaxBytes,
	)
	if err != nil {
		return LayoutOutput{}, err
	}
	output := LayoutOutput{Pages: make([]LayoutPage, 0, len(parsed.Pages))}
	actionableRegions := 0
	var totalCropBytes int64
	extractedRunes := 0
	for _, element := range parsed.Elements {
		extractedRunes += len([]rune(element.ContentText))
	}
	for _, page := range parsed.Pages {
		if err := ctx.Err(); err != nil {
			return LayoutOutput{}, err
		}
		pageInput := knowledgelayout.PageInput{
			PageNumber: page.PageNumber,
			NativeText: knowledgelayout.NativeTextSignals{
				RuneCount: page.NativeTextRunes, NonWhitespaceRunes: page.NonWhitespaceRunes,
				PrintableRatio: page.PrintableRatio, ExtractionComplete: page.ExtractionComplete,
			},
			VisualCandidateCount:  page.VisualCandidateCount,
			VisualCandidatesKnown: page.VisualCandidatesKnown,
		}
		if sourceRaster != nil {
			cloned := *sourceRaster
			pageInput.Raster = &cloned
		}
		analysis, err := s.analyzer.Analyze(ctx, knowledgelayout.AnalysisRequest{
			Source: documentSource, Page: pageInput,
		})
		if err != nil {
			return LayoutOutput{}, err
		}
		if err := analysis.Plan.Validate(); err != nil {
			return LayoutOutput{}, err
		}
		if analysis.RecoveredNativeText != "" {
			extractedRunes += len([]rune(analysis.RecoveredNativeText))
			if extractedRunes > s.config.MaxExtractedRunes {
				return LayoutOutput{}, fmt.Errorf(
					"%w: recovered PDF text exceeds limit %d",
					knowledgeparser.ErrResourceLimit, s.config.MaxExtractedRunes,
				)
			}
		}
		raster := pageInput.Raster
		if analysis.Render != nil {
			raster = &analysis.Render.Raster
		}
		regions, err := s.cropActionableRegions(
			analysis.Plan, raster, &actionableRegions, &totalCropBytes,
		)
		if err != nil {
			return LayoutOutput{}, err
		}
		output.Pages = append(output.Pages, LayoutPage{
			PageNumber: page.PageNumber, Plan: analysis.Plan, Render: analysis.Render,
			RecoveredNativeText: analysis.RecoveredNativeText, Regions: regions,
		})
	}
	return output, nil
}

func (s *LayoutStage) cropActionableRegions(
	plan knowledgelayout.Plan,
	raster *knowledgelayout.RasterPage,
	actionableRegions *int,
	totalCropBytes *int64,
) ([]LayoutRegion, error) {
	if actionableRegions == nil || totalCropBytes == nil {
		return nil, knowledgelayout.ErrInvalidInput
	}
	regions := make([]LayoutRegion, 0, len(plan.Routes))
	for _, route := range plan.Routes {
		item := LayoutRegion{Route: route}
		switch route.Route {
		case knowledgelayout.RouteCloudOCR, knowledgelayout.RouteTableRecovery,
			knowledgelayout.RouteCloudVision:
			if raster == nil {
				return nil, knowledgelayout.ErrInvalidInput
			}
			if *actionableRegions >= s.config.MaxActionableRegions {
				item.SuppressedReason = "layout_region_count_budget_exceeded"
				regions = append(regions, item)
				continue
			}
			crop, err := knowledgelayout.CropRaster(*raster, route.Box, s.config.CropConfig)
			if err != nil {
				return nil, err
			}
			if int64(len(crop.Raster.Content)) > s.config.MaxTotalCropBytes-*totalCropBytes {
				item.SuppressedReason = "layout_region_byte_budget_exceeded"
				regions = append(regions, item)
				continue
			}
			item.Crop = &crop
			(*actionableRegions)++
			*totalCropBytes += int64(len(crop.Raster.Content))
		case knowledgelayout.RouteNativeText, knowledgelayout.RouteSkip:
		default:
			return nil, knowledgelayout.ErrInvalidInput
		}
		regions = append(regions, item)
	}
	return regions, nil
}

func layoutDocumentSource(source knowledgeparser.Input) knowledgelayout.DocumentSource {
	if source.MediaType != "application/pdf" {
		return knowledgelayout.DocumentSource{}
	}
	digest := sha256.Sum256(source.Content)
	return knowledgelayout.DocumentSource{
		MediaType: source.MediaType, Content: source.Content, SHA256: hex.EncodeToString(digest[:]),
	}
}

func layoutSourceRaster(
	mediaType string,
	assets []knowledgeparser.VisualAsset,
	maxPixels int64,
	maxBytes int64,
) (*knowledgelayout.RasterPage, error) {
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return nil, nil
	}
	if maxPixels < 1 || maxBytes < 1 || len(assets) != 1 ||
		assets[0].Kind != knowledgeparser.VisualAssetSourceImage || assets[0].MediaType != mediaType {
		return nil, knowledgelayout.ErrInvalidInput
	}
	asset := assets[0]
	if len(asset.Content) == 0 || asset.Width < 1 || asset.Height < 1 {
		return nil, knowledgelayout.ErrInvalidInput
	}
	raster := knowledgelayout.RasterPage{
		MediaType: asset.MediaType, Width: asset.Width, Height: asset.Height,
		Content: append([]byte(nil), asset.Content...),
	}
	if err := raster.Validate(maxPixels, maxBytes); err != nil {
		return nil, fmt.Errorf("%w: source image exceeds layout raster limits", knowledgeparser.ErrResourceLimit)
	}
	return &raster, nil
}
