package knowledgeparser

import (
	"context"
	"encoding/json"
	"fmt"
)

const ImageParserVersion = "raster-image-assets-v1"

type ImageParser struct {
	limits Limits
}

func NewImageParser(limits Limits) (ImageParser, error) {
	if err := limits.Validate(); err != nil {
		return ImageParser{}, err
	}
	return ImageParser{limits: limits}, nil
}

func (ImageParser) Supports(mediaType string) bool {
	return mediaType == "image/png" || mediaType == "image/jpeg"
}

func (p ImageParser) Parse(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if int64(len(input.Content)) > p.limits.MaxVisualAssetBytes ||
		int64(len(input.Content)) > p.limits.MaxTotalVisualBytes {
		return Result{}, fmt.Errorf("%w: source image exceeds configured visual byte limit", ErrResourceLimit)
	}
	asset, err := newRasterVisualAsset(
		0, VisualAssetSourceImage, "source", nil, nil, "", input.Content,
	)
	if err != nil {
		return Result{}, err
	}
	if asset.MediaType != input.MediaType {
		return Result{}, fmt.Errorf("%w: source image signature does not match media type", ErrInvalidContent)
	}
	metadata, err := json.Marshal(map[string]any{
		"mediaType": input.MediaType, "extractionMode": "source_image",
		"visualAssetCount": 1, "visualEnrichmentRequired": true,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		ParserVersion: ImageParserVersion,
		VisualAssets:  []VisualAsset{asset},
		Pages: []PageObservation{{
			PageNumber: 1, ExtractionComplete: true,
			VisualCandidateCount: 1, VisualCandidatesKnown: true,
		}},
		Metadata: metadata,
	}, nil
}
