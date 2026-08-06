// Package knowledgeingestion implements the deterministic ingestion pipeline
// behind the generic knowledgeworker.Executor boundary.
package knowledgeingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const elementArtifactSchemaVersion = 5

type Config struct {
	MaxSourceBytes   int64
	MaxArtifactBytes int64
	ChunkOptions     knowledge.TextChunkOptions
	VisualConfig     knowledgeenrichment.Config
	VisualProcessor  knowledgeenrichment.Processor
	LayoutStage      *LayoutStage
	Embedding        *EmbeddingConfig
	Clock            func() time.Time
	NewID            func() uuid.UUID
}

type EmbeddingConfig struct {
	Profile       knowledge.EmbeddingProfile
	Embedder      knowledge.Embedder
	BatchSize     int
	MaxConcurrent int
}

type Parser interface {
	Parse(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error)
}

type Executor struct {
	store            objectstore.Store
	parser           Parser
	maxSourceBytes   int64
	maxArtifactBytes int64
	chunkOptions     knowledge.TextChunkOptions
	visualEnricher   *knowledgeenrichment.Orchestrator
	layoutStage      *LayoutStage
	embedding        *EmbeddingConfig
	clock            func() time.Time
	newID            func() uuid.UUID
}

func NewExecutor(store objectstore.Store, parser Parser, cfg Config) (*Executor, error) {
	if store == nil || parser == nil {
		return nil, errors.New("knowledge ingestion executor dependencies are nil")
	}
	if cfg.MaxSourceBytes < 1 || cfg.MaxArtifactBytes < 1 {
		return nil, errors.New("knowledge ingestion executor byte limits must be positive")
	}
	visualEnricher, err := knowledgeenrichment.New(cfg.VisualConfig, cfg.VisualProcessor)
	if err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = uuid.New
	}
	if cfg.Embedding != nil {
		if err := cfg.Embedding.Profile.Validate(); err != nil {
			return nil, err
		}
		if cfg.Embedding.Embedder == nil || cfg.Embedding.BatchSize < 1 || cfg.Embedding.BatchSize > 10 ||
			cfg.Embedding.MaxConcurrent < 1 || cfg.Embedding.MaxConcurrent > 8 {
			return nil, errors.New("knowledge ingestion embedding configuration is invalid")
		}
	}
	return &Executor{
		store: store, parser: parser, maxSourceBytes: cfg.MaxSourceBytes,
		maxArtifactBytes: cfg.MaxArtifactBytes, chunkOptions: cfg.ChunkOptions,
		visualEnricher: visualEnricher, layoutStage: cfg.LayoutStage, embedding: cfg.Embedding,
		clock: cfg.Clock, newID: cfg.NewID,
	}, nil
}

func (e *Executor) Execute(
	ctx context.Context,
	task knowledgeworker.Task,
	report func(context.Context, knowledgeworker.CheckpointUpdate) error,
) (knowledgeworker.ExecutionResult, error) {
	if e == nil || e.store == nil || e.parser == nil || report == nil {
		return knowledgeworker.ExecutionResult{}, errors.New("knowledge ingestion executor is unavailable")
	}
	if task.Source.SizeBytes > e.maxSourceBytes {
		return knowledgeworker.ExecutionResult{}, permanentError("source exceeds configured parser limit")
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageScanning, 10, map[string]any{
		"sourceSizeBytes": task.Source.SizeBytes,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}

	content, err := e.readVerifiedSource(ctx, task.Source)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageParsing, 30, map[string]any{
		"sourceSha256": task.Source.SHA256, "sourceVerified": true,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	parserInput := knowledgeparser.Input{
		MediaType: task.Source.MediaType, OriginalName: task.Source.OriginalName, Content: content,
	}
	parsed, err := e.parser.Parse(ctx, parserInput)
	if err != nil {
		if errors.Is(err, knowledgeparser.ErrUnsupportedMediaType) || errors.Is(err, knowledgeparser.ErrInvalidContent) ||
			errors.Is(err, knowledgeparser.ErrResourceLimit) {
			return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
		}
		return knowledgeworker.ExecutionResult{}, err
	}
	var layout *LayoutOutput
	if e.layoutStage != nil && len(parsed.Pages) > 0 {
		analyzed, analyzeErr := e.layoutStage.Analyze(ctx, parserInput, parsed)
		if analyzeErr != nil {
			return knowledgeworker.ExecutionResult{}, analyzeErr
		}
		if len(analyzed.Pages) > 0 {
			layout = &analyzed
		}
	}
	if err := appendRecoveredNativeText(&parsed, layout); err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	plans, err := e.prepareVisualPlans(&parsed, layout)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	layoutPartial := layoutIsPartial(layout)
	if err := report(ctx, checkpoint(knowledge.IngestionStageChunking, 60, map[string]any{
		"elementCount": len(parsed.Elements), "visualAssetCount": len(parsed.VisualAssets),
		"parserVersion": parsed.ParserVersion, "layoutPageCount": layoutPageCount(layout),
		"layoutPartial": layoutPartial,
	})); err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	enrichment, err := e.visualEnricher.EnrichPlanned(ctx, knowledgeenrichment.Source{
		MediaType: task.Source.MediaType, OriginalName: task.Source.OriginalName, Content: content,
	}, plans, len(parsed.Elements))
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	parsed.Elements = append(parsed.Elements, enrichment.Elements...)
	if len(parsed.Elements) == 0 {
		if errors.Is(enrichment.Cause, knowledgeenrichment.ErrUnsupportedInput) {
			return knowledgeworker.ExecutionResult{}, permanentCause(enrichment.Cause)
		}
		if enrichment.Cause != nil && !errors.Is(enrichment.Cause, knowledgeenrichment.ErrUnavailable) {
			return knowledgeworker.ExecutionResult{}, enrichment.Cause
		}
		return knowledgeworker.ExecutionResult{}, permanentError("document has no searchable content and requires configured visual enrichment")
	}
	merged, err := mergeElements(parsed.Elements)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	parsed.Elements = merged.Elements
	chunks, err := knowledge.ChunkElements(merged.SearchableElements, e.chunkOptions)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	embeddings, embeddingUsage, err := e.embedChunks(ctx, chunks)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}

	partial := enrichment.Partial || layoutPartial
	artifactBytes, parserMetadata, err := buildElementArtifact(task, parsed, enrichment, layout, merged, len(chunks))
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	parserMetadata, err = appendEmbeddingMetadata(parserMetadata, e.embedding, len(embeddings), embeddingUsage)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, permanentError(err.Error())
	}
	if int64(len(artifactBytes)) > e.maxArtifactBytes {
		return knowledgeworker.ExecutionResult{}, permanentError("element artifact exceeds configured object limit")
	}
	artifactKey, err := objectstore.NewObjectKey(
		objectstore.BucketKnowledgeArtifacts, e.newID(), e.clock().UTC(),
	)
	if err != nil {
		return knowledgeworker.ExecutionResult{}, err
	}
	artifact, err := e.store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeArtifacts, ObjectKey: artifactKey,
		Content: bytes.NewReader(artifactBytes), SizeBytes: int64(len(artifactBytes)),
		MediaType: "application/json", OriginalName: task.Source.OriginalName + ".elements.json",
	})
	if err != nil {
		return knowledgeworker.ExecutionResult{}, fmt.Errorf("store element artifact: %w", err)
	}
	if err := report(ctx, checkpoint(knowledge.IngestionStageIndexing, 85, map[string]any{
		"artifactSha256": artifact.SHA256, "chunkCount": len(chunks), "elementCount": len(parsed.Elements),
		"searchableElementCount":   len(merged.SearchableElements),
		"suppressedDuplicateCount": merged.SuppressedCount,
		"visualAssetCount":         len(parsed.VisualAssets), "partial": partial,
		"embeddingCount": len(embeddings), "embeddingTokens": embeddingUsage.TotalTokens,
		"embeddingProfileFingerprint": embeddingProfileFingerprint(e.embedding),
	})); err != nil {
		e.cleanupArtifact(artifact)
		return knowledgeworker.ExecutionResult{}, err
	}
	finalCheckpoint, err := json.Marshal(map[string]any{
		"artifactSha256": artifact.SHA256, "chunkCount": len(chunks), "elementCount": len(parsed.Elements),
		"searchableElementCount":   len(merged.SearchableElements),
		"suppressedDuplicateCount": merged.SuppressedCount,
		"visualAssetCount":         len(parsed.VisualAssets), "partial": partial,
		"embeddingCount": len(embeddings), "embeddingTokens": embeddingUsage.TotalTokens,
		"embeddingProfileFingerprint": embeddingProfileFingerprint(e.embedding),
	})
	if err != nil {
		e.cleanupArtifact(artifact)
		return knowledgeworker.ExecutionResult{}, err
	}
	result := knowledgeworker.ExecutionResult{
		Partial: partial, ParserVersion: parsed.ParserVersion, ParserMetadata: parserMetadata,
		Checkpoint: finalCheckpoint, Artifact: artifact, Chunks: chunks,
		Embeddings: embeddings, EmbeddingUsage: embeddingUsage,
	}
	if e.embedding != nil {
		profile := e.embedding.Profile
		result.EmbeddingProfile = &profile
	}
	return result, nil
}

func (e *Executor) embedChunks(
	ctx context.Context,
	chunks []knowledge.ChunkDraft,
) ([]knowledge.ChunkEmbeddingDraft, knowledge.EmbeddingUsage, error) {
	if e.embedding == nil {
		return nil, knowledge.EmbeddingUsage{}, nil
	}
	batchCount := (len(chunks) + e.embedding.BatchSize - 1) / e.embedding.BatchSize
	batchVectors := make([][][]float32, batchCount)
	var totalTokens int
	var usageMu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(e.embedding.MaxConcurrent)
	for batchIndex := 0; batchIndex < batchCount; batchIndex++ {
		batchIndex := batchIndex
		start := batchIndex * e.embedding.BatchSize
		end := min(start+e.embedding.BatchSize, len(chunks))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = chunks[index].ContentText
		}
		group.Go(func() error {
			output, err := e.embedding.Embedder.Embed(groupCtx, knowledge.EmbeddingRequest{
				Texts: texts, InputType: e.embedding.Profile.DocumentInputType,
			})
			if err != nil {
				return fmt.Errorf("embed knowledge chunks batch %d: %w", batchIndex, err)
			}
			if err := output.Validate(len(texts), e.embedding.Profile.Dimensions, e.embedding.Profile.Normalize); err != nil {
				return fmt.Errorf("validate knowledge chunk embeddings batch %d: %w", batchIndex, err)
			}
			batchVectors[batchIndex] = output.Vectors
			usageMu.Lock()
			totalTokens += output.Usage.TotalTokens
			usageMu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, knowledge.EmbeddingUsage{}, err
	}
	embeddings := make([]knowledge.ChunkEmbeddingDraft, 0, len(chunks))
	for batchIndex, vectors := range batchVectors {
		start := batchIndex * e.embedding.BatchSize
		for offset, vector := range vectors {
			ordinal := start + offset
			embeddings = append(embeddings, knowledge.ChunkEmbeddingDraft{
				ChunkOrdinal: ordinal, ContentSHA256: chunks[ordinal].ContentSHA256,
				Vector: append([]float32(nil), vector...),
			})
		}
	}
	return embeddings, knowledge.EmbeddingUsage{TotalTokens: totalTokens}, nil
}

func appendEmbeddingMetadata(
	raw json.RawMessage,
	cfg *EmbeddingConfig,
	count int,
	usage knowledge.EmbeddingUsage,
) (json.RawMessage, error) {
	if cfg == nil {
		return raw, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return nil, errors.New("parser metadata is not a JSON object")
	}
	metadata["embeddingProfileId"] = cfg.Profile.ID.String()
	metadata["embeddingProfileFingerprint"] = cfg.Profile.Fingerprint
	metadata["embeddingModel"] = cfg.Profile.Model
	metadata["embeddingDimensions"] = cfg.Profile.Dimensions
	metadata["embeddingCount"] = count
	metadata["embeddingTokens"] = usage.TotalTokens
	return json.Marshal(metadata)
}

func embeddingProfileFingerprint(cfg *EmbeddingConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Profile.Fingerprint
}

func appendRecoveredNativeText(parsed *knowledgeparser.Result, layout *LayoutOutput) error {
	if parsed == nil || layout == nil {
		return nil
	}
	for _, page := range layout.Pages {
		if page.RecoveredNativeText == "" {
			continue
		}
		metadata, err := json.Marshal(map[string]any{
			"extractionProvider": "pdfium-wasm",
			"extractionReason":   "embedded_parser_empty",
		})
		if err != nil {
			return err
		}
		pageNumber := page.PageNumber
		parsed.Elements = append(parsed.Elements, knowledge.DocumentElement{
			Index: len(parsed.Elements), PageNumber: &pageNumber,
			ElementType: knowledge.ElementText, ContentText: page.RecoveredNativeText,
			Metadata: metadata,
		})
	}
	return nil
}

func (e *Executor) prepareVisualPlans(
	parsed *knowledgeparser.Result,
	layout *LayoutOutput,
) ([]knowledgeenrichment.PlannedAsset, error) {
	plans := e.visualEnricher.PlanAssets(parsed.VisualAssets)
	if layout == nil {
		return plans, nil
	}
	coveredPages := make(map[int]struct{}, len(layout.Pages))
	for _, page := range layout.Pages {
		coveredPages[page.PageNumber] = struct{}{}
	}
	for index := range plans {
		if layoutSupersedesAsset(plans[index].Asset, coveredPages) {
			plans[index].Route = knowledgeenrichment.RouteSkip
			plans[index].Reason = "superseded_by_layout_regions"
		}
	}
	for pageIndex := range layout.Pages {
		page := &layout.Pages[pageIndex]
		for regionIndex := range page.Regions {
			region := &page.Regions[regionIndex]
			enrichmentRoute, actionable := layoutEnrichmentRoute(region.Route.Route)
			if !actionable {
				continue
			}
			if region.Crop == nil {
				if region.SuppressedReason != "" {
					continue
				}
				return nil, errors.New("actionable layout region has no crop")
			}
			assetIndex := len(parsed.VisualAssets)
			asset, err := knowledgeparser.NewLayoutRegionVisualAsset(
				assetIndex,
				page.PageNumber,
				fmt.Sprintf("pages/%d/layout-regions/%d", page.PageNumber, region.Route.Ordinal),
				region.Crop.Raster.Content,
			)
			if err != nil {
				return nil, err
			}
			parsed.VisualAssets = append(parsed.VisualAssets, asset)
			region.AssetIndex = newIntPointer(assetIndex)
			plans = append(plans, knowledgeenrichment.PlannedAsset{
				Asset: asset, Route: enrichmentRoute, Reason: string(region.Route.Reason),
			})
		}
	}
	return plans, nil
}

func layoutSupersedesAsset(asset knowledgeparser.VisualAsset, coveredPages map[int]struct{}) bool {
	switch asset.Kind {
	case knowledgeparser.VisualAssetSourceImage:
		_, covered := coveredPages[1]
		return covered
	case knowledgeparser.VisualAssetDocumentPage:
		if asset.PageNumber == nil {
			return false
		}
		_, covered := coveredPages[*asset.PageNumber]
		return covered
	default:
		return false
	}
}

func layoutEnrichmentRoute(route knowledgelayout.ProcessingRoute) (knowledgeenrichment.Route, bool) {
	switch route {
	case knowledgelayout.RouteCloudOCR:
		return knowledgeenrichment.RouteOCR, true
	case knowledgelayout.RouteTableRecovery, knowledgelayout.RouteCloudVision:
		return knowledgeenrichment.RouteOCRVLM, true
	case knowledgelayout.RouteNativeText, knowledgelayout.RouteSkip:
		return knowledgeenrichment.RouteSkip, false
	default:
		return knowledgeenrichment.RouteSkip, false
	}
}

func layoutIsPartial(layout *LayoutOutput) bool {
	if layout == nil {
		return false
	}
	for _, page := range layout.Pages {
		if page.Plan.Partial {
			return true
		}
		for _, region := range page.Regions {
			if region.SuppressedReason != "" {
				return true
			}
		}
	}
	return false
}

func layoutPageCount(layout *LayoutOutput) int {
	if layout == nil {
		return 0
	}
	return len(layout.Pages)
}

func newIntPointer(value int) *int {
	return &value
}

func (e *Executor) readVerifiedSource(ctx context.Context, ref objectstore.ObjectRef) ([]byte, error) {
	if err := ref.Validate(); err != nil || ref.Bucket != objectstore.BucketKnowledgeSources {
		return nil, permanentError("source reference is invalid")
	}
	read, err := e.store.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("read knowledge source: %w", err)
	}
	defer read.Content.Close()
	content, err := io.ReadAll(io.LimitReader(read.Content, e.maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read knowledge source body: %w", err)
	}
	if int64(len(content)) > e.maxSourceBytes || int64(len(content)) != ref.SizeBytes {
		return nil, permanentError("source size does not match immutable reference")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != strings.ToLower(ref.SHA256) {
		return nil, permanentError("source sha256 does not match immutable reference")
	}
	return content, nil
}

type elementArtifact struct {
	SchemaVersion     int                   `json:"schemaVersion"`
	DocumentVersionID string                `json:"documentVersionId"`
	Source            artifactSource        `json:"source"`
	ParserVersion     string                `json:"parserVersion"`
	Elements          []artifactElement     `json:"elements"`
	VisualAssets      []artifactVisualAsset `json:"visualAssets"`
	Layout            *artifactLayout       `json:"layout,omitempty"`
	ElementMerge      artifactElementMerge  `json:"elementMerge"`
}

type artifactElementMerge struct {
	Version         string                 `json:"version"`
	SuppressedCount int                    `json:"suppressedCount"`
	Decisions       []elementMergeDecision `json:"decisions"`
}

type artifactLayout struct {
	Pages []artifactLayoutPage `json:"pages"`
}

type artifactLayoutPage struct {
	PageNumber   int                         `json:"pageNumber"`
	PageClass    knowledgelayout.PageClass   `json:"pageClass"`
	DetectorUsed bool                        `json:"detectorUsed"`
	Fallback     bool                        `json:"fallback"`
	Partial      bool                        `json:"partial"`
	Model        *knowledgelayout.ModelTrace `json:"model,omitempty"`
	Render       *artifactLayoutRender       `json:"render,omitempty"`
	Regions      []artifactLayoutRegion      `json:"regions"`
}

type artifactLayoutRender struct {
	DPI          int                           `json:"dpi"`
	RequestedDPI int                           `json:"requestedDpi"`
	Downscaled   bool                          `json:"downscaled"`
	RasterSHA256 string                        `json:"rasterSha256"`
	Width        int                           `json:"width"`
	Height       int                           `json:"height"`
	Renderer     knowledgelayout.RendererTrace `json:"renderer"`
}

type artifactLayoutRegion struct {
	Ordinal          int                             `json:"ordinal"`
	RegionType       knowledgelayout.RegionType      `json:"regionType"`
	Box              knowledgelayout.BoundingBox     `json:"box"`
	Confidence       float64                         `json:"confidence"`
	Route            knowledgelayout.ProcessingRoute `json:"route"`
	Reason           knowledgelayout.ReasonCode      `json:"reason"`
	AssetIndex       *int                            `json:"assetIndex,omitempty"`
	SuppressedReason string                          `json:"suppressedReason,omitempty"`
	Crop             *artifactLayoutCrop             `json:"crop,omitempty"`
}

type artifactLayoutCrop struct {
	AppliedBox         knowledgelayout.BoundingBox `json:"appliedBox"`
	Pixels             knowledgelayout.PixelBox    `json:"pixels"`
	SourceRasterSHA256 string                      `json:"sourceRasterSha256"`
	RasterSHA256       string                      `json:"rasterSha256"`
	EncoderVersion     string                      `json:"encoderVersion"`
}

type artifactVisualAsset struct {
	Index                int                                `json:"index"`
	Kind                 knowledgeparser.VisualAssetKind    `json:"kind"`
	PageNumber           *int                               `json:"pageNumber,omitempty"`
	SectionPath          []string                           `json:"sectionPath"`
	SourcePath           string                             `json:"sourcePath"`
	SourcePart           string                             `json:"sourcePart,omitempty"`
	RelationshipID       string                             `json:"relationshipId,omitempty"`
	MediaType            string                             `json:"mediaType"`
	SizeBytes            int64                              `json:"sizeBytes"`
	ContentSHA256        string                             `json:"contentSha256"`
	Width                int                                `json:"width,omitempty"`
	Height               int                                `json:"height,omitempty"`
	Route                knowledgeenrichment.Route          `json:"route"`
	Status               knowledgeenrichment.Status         `json:"status"`
	Reason               string                             `json:"reason"`
	Provider             string                             `json:"provider,omitempty"`
	Model                string                             `json:"model,omitempty"`
	Usage                *knowledgeenrichment.ProviderUsage `json:"usage,omitempty"`
	OutputElementIndexes []int                              `json:"outputElementIndexes"`
}

type artifactSource struct {
	MediaType    string `json:"mediaType"`
	OriginalName string `json:"originalName"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
}

type artifactElement struct {
	Index       int             `json:"index"`
	PageNumber  *int            `json:"pageNumber,omitempty"`
	ElementType string          `json:"elementType"`
	SectionPath []string        `json:"sectionPath"`
	ContentText string          `json:"contentText"`
	Metadata    json.RawMessage `json:"metadata"`
}

func buildElementArtifact(
	task knowledgeworker.Task,
	parsed knowledgeparser.Result,
	enrichment knowledgeenrichment.Output,
	layout *LayoutOutput,
	merge elementMergeOutput,
	chunkCount int,
) ([]byte, json.RawMessage, error) {
	elements := make([]artifactElement, 0, len(parsed.Elements))
	for _, item := range parsed.Elements {
		metadata := item.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		elements = append(elements, artifactElement{
			Index: item.Index, PageNumber: item.PageNumber, ElementType: string(item.ElementType),
			SectionPath: append([]string{}, item.SectionPath...), ContentText: item.ContentText,
			Metadata: append(json.RawMessage(nil), metadata...),
		})
	}
	records := make(map[int]knowledgeenrichment.Record, len(enrichment.Records))
	for _, record := range enrichment.Records {
		records[record.AssetIndex] = record
	}
	visualAssets := make([]artifactVisualAsset, 0, len(parsed.VisualAssets))
	missingVisualAssets := 0
	for _, asset := range parsed.VisualAssets {
		record := records[asset.Index]
		if record.Status == knowledgeenrichment.StatusMissing {
			missingVisualAssets++
		}
		visualAssets = append(visualAssets, artifactVisualAsset{
			Index: asset.Index, Kind: asset.Kind, PageNumber: asset.PageNumber,
			SectionPath: append([]string{}, asset.SectionPath...), SourcePath: asset.SourcePath,
			SourcePart: asset.SourcePart, RelationshipID: asset.RelationshipID, MediaType: asset.MediaType,
			SizeBytes: asset.SizeBytes, ContentSHA256: asset.SHA256,
			Width: asset.Width, Height: asset.Height, Route: record.Route, Status: record.Status,
			Reason: record.Reason, Provider: record.Provider, Model: record.Model,
			Usage:                cloneArtifactProviderUsage(record.Usage),
			OutputElementIndexes: append([]int{}, record.OutputElementIndexes...),
		})
	}
	artifactBytes, err := json.Marshal(elementArtifact{
		SchemaVersion: elementArtifactSchemaVersion, DocumentVersionID: task.DocumentVersionID.String(),
		Source: artifactSource{
			MediaType: task.Source.MediaType, OriginalName: task.Source.OriginalName,
			SizeBytes: task.Source.SizeBytes, SHA256: task.Source.SHA256,
		},
		ParserVersion: parsed.ParserVersion, Elements: elements, VisualAssets: visualAssets,
		Layout: buildArtifactLayout(layout), ElementMerge: artifactElementMerge{
			Version: merge.Version, SuppressedCount: merge.SuppressedCount,
			Decisions: append([]elementMergeDecision(nil), merge.Decisions...),
		},
	})
	if err != nil {
		return nil, nil, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(parsed.Metadata, &metadata); err != nil {
		return nil, nil, err
	}
	metadata["artifactSchemaVersion"] = elementArtifactSchemaVersion
	metadata["chunkCount"] = chunkCount
	metadata["visualAssetCount"] = len(parsed.VisualAssets)
	metadata["missingVisualAssetCount"] = missingVisualAssets
	metadata["visualEnrichmentPartial"] = enrichment.Partial
	metadata["layoutPageCount"] = layoutPageCount(layout)
	metadata["layoutPartial"] = layoutIsPartial(layout)
	metadata["elementMergeVersion"] = merge.Version
	metadata["searchableElementCount"] = len(merge.SearchableElements)
	metadata["suppressedDuplicateCount"] = merge.SuppressedCount
	metadataBytes, err := json.Marshal(metadata)
	return artifactBytes, metadataBytes, err
}

func cloneArtifactProviderUsage(value *knowledgeenrichment.ProviderUsage) *knowledgeenrichment.ProviderUsage {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func buildArtifactLayout(layout *LayoutOutput) *artifactLayout {
	if layout == nil {
		return nil
	}
	result := &artifactLayout{Pages: make([]artifactLayoutPage, 0, len(layout.Pages))}
	for _, page := range layout.Pages {
		item := artifactLayoutPage{
			PageNumber: page.PageNumber, PageClass: page.Plan.PageClass,
			DetectorUsed: page.Plan.DetectorUsed, Fallback: page.Plan.Fallback,
			Partial: page.Plan.Partial, Model: page.Plan.Model,
			Regions: make([]artifactLayoutRegion, 0, len(page.Regions)),
		}
		if page.Render != nil {
			item.Render = &artifactLayoutRender{
				DPI: page.Render.DPI, RequestedDPI: page.Render.RequestedDPI,
				Downscaled:   page.Render.DPI < page.Render.RequestedDPI,
				RasterSHA256: page.Render.RasterSHA256,
				Width:        page.Render.Raster.Width, Height: page.Render.Raster.Height,
				Renderer: page.Render.Renderer,
			}
		}
		for _, region := range page.Regions {
			artifactRegion := artifactLayoutRegion{
				Ordinal: region.Route.Ordinal, RegionType: region.Route.RegionType,
				Box: region.Route.Box, Confidence: region.Route.Confidence,
				Route: region.Route.Route, Reason: region.Route.Reason,
				AssetIndex: cloneIntPointer(region.AssetIndex), SuppressedReason: region.SuppressedReason,
			}
			if region.Crop != nil {
				artifactRegion.Crop = &artifactLayoutCrop{
					AppliedBox: region.Crop.AppliedBox, Pixels: region.Crop.Pixels,
					SourceRasterSHA256: region.Crop.SourceRasterSHA256,
					RasterSHA256:       region.Crop.RasterSHA256,
					EncoderVersion:     region.Crop.EncoderVersion,
				}
			}
			item.Regions = append(item.Regions, artifactRegion)
		}
		result.Pages = append(result.Pages, item)
	}
	return result
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func checkpoint(stage knowledge.IngestionStage, progress int, values map[string]any) knowledgeworker.CheckpointUpdate {
	raw, err := json.Marshal(values)
	if err != nil {
		raw = json.RawMessage(`{}`)
	}
	return knowledgeworker.CheckpointUpdate{Stage: stage, ProgressPercent: progress, Checkpoint: raw}
}

func permanentError(message string) error {
	return errors.Join(knowledgeworker.ErrPermanentInput, errors.New(message))
}

func permanentCause(cause error) error {
	return errors.Join(knowledgeworker.ErrPermanentInput, cause)
}

func (e *Executor) cleanupArtifact(ref objectstore.ObjectRef) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.store.Remove(ctx, ref)
}
