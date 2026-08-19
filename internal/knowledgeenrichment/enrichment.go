package knowledgeenrichment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgetable"
)

var (
	ErrUnavailable      = errors.New("knowledge visual enrichment is unavailable")
	ErrUnsupportedInput = errors.New("knowledge visual enrichment input is unsupported")
	ErrNoUsableContent  = errors.New("knowledge visual enrichment produced no usable content")
)

type Route string

const (
	RouteSkip   Route = "skip"
	RouteOCR    Route = "ocr"
	RouteOCRVLM Route = "ocr_vlm"
	RouteTable  Route = "table"
)

type Status string

const (
	StatusCompleted Status = "completed"
	StatusPartial   Status = "partial"
	StatusMissing   Status = "missing"
	StatusSkipped   Status = "skipped"
)

type Config struct {
	MaxEnrichments int
	MinPixels      int64
}

func (c Config) Validate() error {
	if c.MaxEnrichments < 1 || c.MaxEnrichments > 100 || c.MinPixels < 1 || c.MinPixels > 100_000_000 {
		return errors.New("knowledge visual enrichment configuration is invalid")
	}
	return nil
}

type Source struct {
	MediaType    string
	OriginalName string
	Content      []byte
}

type Request struct {
	Source Source
	Asset  knowledgeparser.VisualAsset
	Route  Route
	Reason string
}

type ProviderResult struct {
	Provider string
	Model    string
	Elements []knowledge.DocumentElement
	Partial  bool
	Reason   string
	Usage    *ProviderUsage
}

type ProviderUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

func (u ProviderUsage) Validate() error {
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.TotalTokens < u.PromptTokens || u.TotalTokens-u.PromptTokens < u.CompletionTokens {
		return errors.New("knowledge visual enrichment provider usage is invalid")
	}
	return nil
}

const (
	ProviderFailureNoUsableContent = "no_usable_content"
	ProviderFailureOutputTruncated = "output_truncated"
	ProviderFailureInvalidOutput   = "invalid_provider_output"
	ProviderFailureEmptyResponse   = "empty_provider_response"
	providerFailureProcessorFailed = "processor_failed"
)

// ProviderFailure preserves safe audit metadata for a provider call that did
// not produce indexable content. Its wrapped cause remains available to the
// orchestrator for retry and partial-ingestion decisions.
type ProviderFailure struct {
	Provider string
	Model    string
	Reason   string
	Usage    *ProviderUsage
	cause    error
}

func NewProviderFailure(provider, model, reason string, usage *ProviderUsage, cause error) *ProviderFailure {
	if cause == nil {
		cause = errors.New("knowledge visual enrichment provider failed")
	}
	return &ProviderFailure{
		Provider: provider, Model: model, Reason: reason,
		Usage: cloneProviderUsage(usage), cause: cause,
	}
}

func (f *ProviderFailure) Error() string {
	if f == nil || f.cause == nil {
		return "knowledge visual enrichment provider failed"
	}
	return f.cause.Error()
}

func (f *ProviderFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

type Processor interface {
	Process(context.Context, Request) (ProviderResult, error)
}

type Record struct {
	AssetIndex           int
	Route                Route
	Status               Status
	Reason               string
	Provider             string
	Model                string
	Usage                *ProviderUsage
	OutputElementIndexes []int
}

type Output struct {
	Elements []knowledge.DocumentElement
	Records  []Record
	Partial  bool
	Cause    error
}

type PlannedAsset struct {
	Asset  knowledgeparser.VisualAsset
	Route  Route
	Reason string
}

func (p PlannedAsset) Validate() error {
	if err := p.Asset.Validate(); err != nil {
		return err
	}
	if p.Route != RouteSkip && p.Route != RouteOCR && p.Route != RouteOCRVLM && p.Route != RouteTable {
		return errors.New("knowledge visual enrichment planned route is invalid")
	}
	if strings.TrimSpace(p.Reason) == "" || p.Reason != strings.TrimSpace(p.Reason) || len(p.Reason) > 256 {
		return errors.New("knowledge visual enrichment planned reason is invalid")
	}
	return nil
}

type Orchestrator struct {
	config         Config
	processor      Processor
	tableProcessor knowledgetable.Processor
}

func New(config Config, processor Processor) (*Orchestrator, error) {
	return NewWithTableProcessor(config, processor, nil)
}

func NewWithTableProcessor(
	config Config,
	processor Processor,
	tableProcessor knowledgetable.Processor,
) (*Orchestrator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Orchestrator{config: config, processor: processor, tableProcessor: tableProcessor}, nil
}

func (o *Orchestrator) Enrich(
	ctx context.Context,
	source Source,
	assets []knowledgeparser.VisualAsset,
	firstElementIndex int,
) (Output, error) {
	return o.EnrichPlanned(ctx, source, o.PlanAssets(assets), firstElementIndex)
}

func (o *Orchestrator) PlanAssets(assets []knowledgeparser.VisualAsset) []PlannedAsset {
	if o == nil {
		return nil
	}
	plans := make([]PlannedAsset, 0, len(assets))
	for _, asset := range assets {
		route, reason := o.route(asset)
		plans = append(plans, PlannedAsset{Asset: asset, Route: route, Reason: reason})
	}
	return plans
}

func (o *Orchestrator) EnrichPlanned(
	ctx context.Context,
	source Source,
	plans []PlannedAsset,
	firstElementIndex int,
) (Output, error) {
	if o == nil {
		return Output{}, errors.New("knowledge visual enrichment orchestrator is unavailable")
	}
	if firstElementIndex < 0 {
		return Output{}, errors.New("knowledge visual enrichment first element index is invalid")
	}
	output := Output{Records: make([]Record, 0, len(plans))}
	actionable := 0
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return Output{}, err
		}
		if err := plan.Validate(); err != nil {
			return Output{}, err
		}
		asset, route, reason := plan.Asset, plan.Route, plan.Reason
		if route != RouteSkip {
			if actionable >= o.config.MaxEnrichments {
				route, reason = RouteSkip, "budget_exceeded"
			} else {
				actionable++
			}
		}
		record := Record{AssetIndex: asset.Index, Route: route, Reason: reason}
		if route == RouteSkip {
			if missingReason(reason) {
				record.Status = StatusMissing
				output.Partial = true
			} else {
				record.Status = StatusSkipped
			}
			output.Records = append(output.Records, record)
			continue
		}
		if o.processor == nil && (route != RouteTable || o.tableProcessor == nil) {
			record.Status = StatusMissing
			record.Reason = "processor_disabled"
			output.Partial = true
			if output.Cause == nil {
				output.Cause = ErrUnavailable
			}
			output.Records = append(output.Records, record)
			continue
		}
		processed, err := o.process(ctx, source, asset, route, reason)
		if err != nil {
			if ctx.Err() != nil {
				return Output{}, ctx.Err()
			}
			failureReason := captureProviderFailure(&record, err)
			if errors.Is(err, ErrNoUsableContent) {
				record.Status = StatusSkipped
				record.Reason = ProviderFailureNoUsableContent
			} else {
				record.Status = StatusMissing
				record.Reason = providerFailureProcessorFailed
				if failureReason != "" {
					record.Reason = failureReason
				}
				output.Partial = true
				if output.Cause == nil {
					output.Cause = err
				}
			}
			output.Records = append(output.Records, record)
			continue
		}
		if err := validateProviderResult(route, processed); err != nil {
			return Output{}, err
		}
		record.Status = StatusCompleted
		record.Provider = strings.TrimSpace(processed.Provider)
		record.Model = strings.TrimSpace(processed.Model)
		record.Usage = cloneProviderUsage(processed.Usage)
		if processed.Partial {
			record.Status = StatusPartial
			record.Reason = strings.TrimSpace(processed.Reason)
			output.Partial = true
		}
		for _, element := range processed.Elements {
			element.Index = firstElementIndex + len(output.Elements)
			if element.PageNumber == nil {
				element.PageNumber = clonePageNumber(asset.PageNumber)
			}
			if len(element.SectionPath) == 0 {
				element.SectionPath = append([]string(nil), asset.SectionPath...)
			}
			if err := element.Validate(); err != nil {
				return Output{}, fmt.Errorf("visual enrichment element: %w", err)
			}
			record.OutputElementIndexes = append(record.OutputElementIndexes, element.Index)
			output.Elements = append(output.Elements, element)
		}
		output.Records = append(output.Records, record)
	}
	return output, nil
}

func captureProviderFailure(record *Record, err error) string {
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure == nil || !validProviderFailureReason(failure.Reason) {
		return ""
	}
	if strings.TrimSpace(failure.Provider) != "" {
		record.Provider = strings.TrimSpace(failure.Provider)
	}
	if strings.TrimSpace(failure.Model) != "" {
		record.Model = strings.TrimSpace(failure.Model)
	}
	if failure.Usage != nil && failure.Usage.Validate() == nil {
		record.Usage = cloneProviderUsage(failure.Usage)
	}
	return failure.Reason
}

func validProviderFailureReason(reason string) bool {
	switch reason {
	case ProviderFailureNoUsableContent, ProviderFailureOutputTruncated,
		ProviderFailureInvalidOutput, ProviderFailureEmptyResponse:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) process(
	ctx context.Context,
	source Source,
	asset knowledgeparser.VisualAsset,
	route Route,
	reason string,
) (ProviderResult, error) {
	if route != RouteTable {
		if o.processor == nil {
			return ProviderResult{}, ErrUnavailable
		}
		return o.processor.Process(ctx, Request{Source: source, Asset: asset, Route: route, Reason: reason})
	}
	if o.tableProcessor != nil {
		table, err := o.tableProcessor.Recover(ctx, knowledgetable.Request{Asset: asset, Reason: reason})
		if err == nil {
			return tableProviderResult(asset, reason, table)
		}
		if !errors.Is(err, knowledgetable.ErrUnavailable) || o.processor == nil {
			return ProviderResult{}, err
		}
	}
	if o.processor == nil {
		return ProviderResult{}, knowledgetable.ErrUnavailable
	}
	fallback, err := o.processor.Process(ctx, Request{
		Source: source, Asset: asset, Route: RouteOCRVLM, Reason: reason,
	})
	if err != nil {
		return ProviderResult{}, err
	}
	fallback.Partial = true
	fallback.Reason = "table_processor_unavailable_visual_fallback"
	return fallback, nil
}

func tableProviderResult(
	asset knowledgeparser.VisualAsset,
	reason string,
	result knowledgetable.Result,
) (ProviderResult, error) {
	if err := result.Validate(); err != nil {
		return ProviderResult{}, err
	}
	metadata, err := json.Marshal(map[string]any{
		"assetIndex": asset.Index, "sourcePath": asset.SourcePath,
		"sourcePart": asset.SourcePart, "relationshipId": asset.RelationshipID,
		"method": "table_recovery", "provider": result.Provider, "model": result.Model,
		"promptVersion": result.PromptVersion, "routeReason": reason,
		"confidence": result.Confidence, "warnings": append([]string(nil), result.Warnings...),
		"cells": append([]knowledgetable.Cell(nil), result.Cells...),
	})
	if err != nil {
		return ProviderResult{}, err
	}
	var usage *ProviderUsage
	if result.Usage != nil {
		usage = &ProviderUsage{
			PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens: result.Usage.TotalTokens,
		}
	}
	return ProviderResult{
		Provider: result.Provider, Model: result.Model, Partial: result.Partial, Reason: result.Reason,
		Usage: usage, Elements: []knowledge.DocumentElement{{
			ElementType: knowledge.ElementTable, ContentText: result.Markdown, Metadata: metadata,
		}},
	}, nil
}

func missingReason(reason string) bool {
	switch reason {
	case "budget_exceeded", "unsupported_media_type", "missing_dimensions":
		return true
	default:
		return false
	}
}

func (o *Orchestrator) route(asset knowledgeparser.VisualAsset) (Route, string) {
	if asset.Kind == knowledgeparser.VisualAssetDocumentPage {
		return RouteOCR, "missing_embedded_text"
	}
	if asset.Kind == knowledgeparser.VisualAssetEmbeddedImage && asset.RelationshipID == "" {
		return RouteSkip, "unreferenced_asset"
	}
	if asset.MediaType != "image/png" && asset.MediaType != "image/jpeg" && asset.MediaType != "image/gif" {
		return RouteSkip, "unsupported_media_type"
	}
	if asset.Width < 1 || asset.Height < 1 {
		return RouteSkip, "missing_dimensions"
	}
	pixels := int64(asset.Width) * int64(asset.Height)
	if pixels < o.config.MinPixels {
		return RouteSkip, "decorative_small_image"
	}
	return RouteOCRVLM, "visual_semantics_required"
}

func validateProviderResult(route Route, result ProviderResult) error {
	if strings.TrimSpace(result.Provider) == "" || strings.TrimSpace(result.Model) == "" || len(result.Elements) == 0 {
		return errors.New("knowledge visual enrichment provider result is incomplete")
	}
	if result.Partial && strings.TrimSpace(result.Reason) == "" {
		return errors.New("knowledge visual enrichment partial result has no reason")
	}
	for _, element := range result.Elements {
		validVisual := element.ElementType == knowledge.ElementOCRText || element.ElementType == knowledge.ElementImageDescription
		validTable := route == RouteTable && element.ElementType == knowledge.ElementTable
		if !validVisual && !validTable {
			return errors.New("knowledge visual enrichment returned an invalid element type")
		}
	}
	if result.Usage != nil {
		if err := result.Usage.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func cloneProviderUsage(value *ProviderUsage) *ProviderUsage {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func clonePageNumber(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
