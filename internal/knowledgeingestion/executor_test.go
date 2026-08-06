package knowledgeingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/google/uuid"
)

func TestExecutorProducesArtifactAndChunksFromVerifiedMarkdown(t *testing.T) {
	content := []byte("# 连接池故障\n\n检查 max connections。\n\n| 参数 | 值 |\n| --- | --- |\n| timeout | 30s |")
	store := &memoryStore{source: content}
	router, _ := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	executor, err := NewExecutor(store, router, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 4096,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
		Clock:        func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) },
		NewID:        func() uuid.UUID { return uuid.MustParse("018f6bb7-6e72-7d44-9b0e-f6f8a4e5e9c0") },
	})
	if err != nil {
		t.Fatal(err)
	}
	task := executorTask(content, "text/markdown")
	var stages []knowledge.IngestionStage
	result, err := executor.Execute(context.Background(), task, func(_ context.Context, update knowledgeworker.CheckpointUpdate) error {
		stages = append(stages, update.Stage)
		return nil
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Chunks) != 2 || result.Artifact.Bucket != objectstore.BucketKnowledgeArtifacts {
		t.Fatalf("result = %+v", result)
	}
	if got := stages; len(got) != 4 || got[0] != knowledge.IngestionStageScanning || got[3] != knowledge.IngestionStageIndexing {
		t.Fatalf("stages = %v", got)
	}
	var artifact elementArtifact
	if err := json.Unmarshal(store.artifact, &artifact); err != nil || len(artifact.Elements) != 2 {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestExecutorEmbedsChunksInBoundedBatchesAndPreservesOrdinal(t *testing.T) {
	content := []byte("# First\n\n" + strings.Repeat("alpha ", 30) + "\n\n# Second\n\n" + strings.Repeat("beta ", 30))
	store := &memoryStore{source: content}
	router, _ := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	profile, err := knowledge.NewEmbeddingProfile(
		"knowledge-v1", "dashscope", "text-embedding-v4", 1024, "cosine",
		knowledge.EmbeddingInputQuery, knowledge.EmbeddingInputDocument, true, "embedding-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &recordingEmbedder{}
	executor, err := NewExecutor(store, router, Config{
		MaxSourceBytes: 4096, MaxArtifactBytes: 16384,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
		Embedding: &EmbeddingConfig{
			Profile: profile, Embedder: embedder, BatchSize: 1, MaxConcurrent: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(
		context.Background(), executorTask(content, "text/markdown"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.EmbeddingProfile == nil || result.EmbeddingProfile.Fingerprint != profile.Fingerprint ||
		len(result.Embeddings) != len(result.Chunks) || result.EmbeddingUsage.TotalTokens != len(result.Chunks) {
		t.Fatalf("embedding result = %+v", result)
	}
	for ordinal, item := range result.Embeddings {
		expectedIndex := len([]rune(result.Chunks[ordinal].ContentText)) % profile.Dimensions
		if item.ChunkOrdinal != ordinal || item.ContentSHA256 != result.Chunks[ordinal].ContentSHA256 ||
			item.Vector[expectedIndex] != 1 {
			t.Fatalf("embedding %d is not aligned with its chunk", ordinal)
		}
	}
	if embedder.inputTypeCount(knowledge.EmbeddingInputDocument) != len(result.Chunks) {
		t.Fatalf("input types = %v", embedder.inputTypes)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.ParserMetadata, &metadata); err != nil ||
		metadata["embeddingProfileFingerprint"] != profile.Fingerprint {
		t.Fatalf("parser metadata = %v err=%v", metadata, err)
	}
}

func TestAppendRecoveredNativeTextPreservesProvenance(t *testing.T) {
	parsed := knowledgeparser.Result{Elements: []knowledge.DocumentElement{{
		Index: 0, ElementType: knowledge.ElementText, ContentText: "existing",
	}}}
	layout := &LayoutOutput{Pages: []LayoutPage{{
		PageNumber: 2, RecoveredNativeText: "recovered by PDFium",
	}}}
	if err := appendRecoveredNativeText(&parsed, layout); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Elements) != 2 || parsed.Elements[1].Index != 1 ||
		parsed.Elements[1].PageNumber == nil || *parsed.Elements[1].PageNumber != 2 ||
		parsed.Elements[1].ContentText != "recovered by PDFium" {
		t.Fatalf("elements = %+v", parsed.Elements)
	}
	var metadata map[string]string
	if err := json.Unmarshal(parsed.Elements[1].Metadata, &metadata); err != nil ||
		metadata["extractionProvider"] != "pdfium-wasm" ||
		metadata["extractionReason"] != "embedded_parser_empty" {
		t.Fatalf("metadata = %v, err = %v", metadata, err)
	}
}

func TestExecutorRejectsIntegrityMismatchAndUnsupportedParserPermanently(t *testing.T) {
	router, _ := knowledgeparser.NewRouter(knowledgeparser.TextParser{})
	for _, test := range []struct {
		name      string
		mediaType string
		mutate    func(*knowledgeworker.Task)
	}{
		{name: "sha mismatch", mediaType: "text/plain", mutate: func(task *knowledgeworker.Task) { task.Source.SHA256 = knowledge.SHA256Hex("other") }},
		{name: "unsupported PDF", mediaType: "application/pdf", mutate: func(*knowledgeworker.Task) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte("content")
			executor, _ := NewExecutor(&memoryStore{source: content}, router, Config{
				MaxSourceBytes: 1024, MaxArtifactBytes: 4096,
				ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
				VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
			})
			task := executorTask(content, test.mediaType)
			test.mutate(&task)
			_, err := executor.Execute(context.Background(), task, func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil })
			if !errors.Is(err, knowledgeworker.ErrPermanentInput) {
				t.Fatalf("Execute error = %v", err)
			}
		})
	}
}

func TestExecutorClassifiesParserResourceLimitAsPermanentInput(t *testing.T) {
	content := []byte("content")
	executor, err := NewExecutor(&memoryStore{source: content}, parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{}, errors.Join(knowledgeparser.ErrResourceLimit, errors.New("too many pages"))
	}), Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 4096,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), executorTask(content, "application/pdf"), func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil })
	if !errors.Is(err, knowledgeworker.ErrPermanentInput) {
		t.Fatalf("Execute error = %v", err)
	}
}

func TestExecutorPersistsMissingVisualAssetsAsPartialArtifact(t *testing.T) {
	content := []byte("source")
	visualContent := []byte{0xde, 0xad, 0xbe, 0xef}
	parser := parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{
			ParserVersion: "mixed-parser-v1",
			Elements: []knowledge.DocumentElement{{
				Index: 0, ElementType: knowledge.ElementText, ContentText: "native text",
			}},
			VisualAssets: []knowledgeparser.VisualAsset{{
				Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage,
				SourcePath: "word/media/image1.png", SourcePart: "word/document.xml",
				RelationshipID: "rIdImage", MediaType: "image/png",
				SizeBytes: int64(len(visualContent)), SHA256: knowledge.SHA256Hex(string(visualContent)),
				Width: 100, Height: 100, Content: visualContent,
			}},
			Metadata: json.RawMessage(`{"visualAssetCount":1}`),
		}, nil
	})
	store := &memoryStore{source: content}
	executor, err := NewExecutor(store, parser, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 16 * 1024,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(
		context.Background(), executorTask(content, "application/test"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Partial || len(result.Chunks) != 1 {
		t.Fatalf("result = %+v", result)
	}
	var artifact elementArtifact
	if err := json.Unmarshal(store.artifact, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 5 || artifact.ElementMerge.Version != elementMergeVersion ||
		len(artifact.VisualAssets) != 1 ||
		artifact.VisualAssets[0].Status != knowledgeenrichment.StatusMissing ||
		artifact.VisualAssets[0].SourcePart != "word/document.xml" ||
		artifact.VisualAssets[0].ContentSHA256 != knowledge.SHA256Hex(string(visualContent)) ||
		bytes.Contains(store.artifact, visualContent) {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestExecutorUsesLayoutRegionPlanWithoutDuplicatingWholeImageCall(t *testing.T) {
	raster := ingestionLayoutRasterFixture(t, 100, 80)
	content := raster.Content
	parser := parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{
			ParserVersion: "image-parser-v2",
			VisualAssets: []knowledgeparser.VisualAsset{{
				Index: 0, Kind: knowledgeparser.VisualAssetSourceImage, SourcePath: "source",
				MediaType: "image/png", SizeBytes: int64(len(content)),
				SHA256: knowledge.SHA256Hex(string(content)), Width: 100, Height: 80,
				Content: content,
			}},
			Pages: []knowledgeparser.PageObservation{{
				PageNumber: 1, ExtractionComplete: true,
				VisualCandidateCount: 1, VisualCandidatesKnown: true,
			}},
			Metadata: json.RawMessage(`{"mediaType":"image/png"}`),
		}, nil
	})
	stage := testLayoutStage(t, pageAnalyzerFunc(func(
		context.Context, knowledgelayout.AnalysisRequest,
	) (knowledgelayout.AnalysisResult, error) {
		return knowledgelayout.AnalysisResult{Plan: knowledgelayout.Plan{
			PageClass: knowledgelayout.PageScanned,
			Routes: []knowledgelayout.RegionRoute{testLayoutRoute(
				0, knowledgelayout.RegionTable, knowledgelayout.RouteTableRecovery,
			)},
		}}, nil
	}))
	processorCalls := 0
	processor := processorFunc(func(_ context.Context, request knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error) {
		processorCalls++
		if request.Asset.Kind != knowledgeparser.VisualAssetLayoutRegion ||
			request.Route != knowledgeenrichment.RouteOCRVLM || request.Asset.Index != 1 {
			t.Fatalf("request = %+v", request)
		}
		return knowledgeenrichment.ProviderResult{
			Provider: "dashscope", Model: "qwen3-vl-plus",
			Usage: &knowledgeenrichment.ProviderUsage{
				PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150,
			},
			Elements: []knowledge.DocumentElement{{
				ElementType: knowledge.ElementOCRText, ContentText: "| alarm | count |\n| --- | --- |\n| E42 | 3 |",
			}},
		}, nil
	})
	store := &memoryStore{source: content}
	executor, err := NewExecutor(store, parser, Config{
		MaxSourceBytes: int64(len(content)) + 1, MaxArtifactBytes: 32 * 1024,
		ChunkOptions:    knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig:    knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 1},
		VisualProcessor: processor, LayoutStage: stage,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(
		context.Background(), executorTask(content, "image/png"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if processorCalls != 1 || len(result.Chunks) != 1 || result.Partial {
		t.Fatalf("calls=%d chunks=%d partial=%v", processorCalls, len(result.Chunks), result.Partial)
	}
	var artifact elementArtifact
	if err := json.Unmarshal(store.artifact, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 5 || artifact.ElementMerge.Version != elementMergeVersion ||
		len(artifact.VisualAssets) != 2 || artifact.Layout == nil ||
		len(artifact.Layout.Pages) != 1 || len(artifact.Layout.Pages[0].Regions) != 1 {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.VisualAssets[0].Status != knowledgeenrichment.StatusSkipped ||
		artifact.VisualAssets[0].Reason != "superseded_by_layout_regions" ||
		artifact.VisualAssets[1].Status != knowledgeenrichment.StatusCompleted ||
		artifact.VisualAssets[1].Usage == nil ||
		artifact.VisualAssets[1].Usage.PromptTokens != 120 ||
		artifact.VisualAssets[1].Usage.CompletionTokens != 30 ||
		artifact.VisualAssets[1].Usage.TotalTokens != 150 {
		t.Fatalf("visual assets = %+v", artifact.VisualAssets)
	}
	region := artifact.Layout.Pages[0].Regions[0]
	if region.AssetIndex == nil || *region.AssetIndex != 1 || region.Crop == nil ||
		region.Route != knowledgelayout.RouteTableRecovery {
		t.Fatalf("layout region = %+v", region)
	}
}

func TestExecutorSuppressesDuplicateOCRBeforeChunking(t *testing.T) {
	content := []byte("source")
	visualContent := []byte("visual")
	nativeText := "Alarm E42 means the PLC connection timed out. Check the industrial network cable before restart."
	ocrText := "PLC connection timed out. Check the industrial network cable before restart."
	parser := parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{
			ParserVersion: "mixed-parser-v1",
			Elements: []knowledge.DocumentElement{{
				Index: 0, PageNumber: intPointer(1), ElementType: knowledge.ElementText, ContentText: nativeText,
			}},
			VisualAssets: []knowledgeparser.VisualAsset{{
				Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage,
				SourcePath: "word/media/image1.png", SourcePart: "word/document.xml",
				RelationshipID: "rIdImage", MediaType: "image/png",
				SizeBytes: int64(len(visualContent)), SHA256: knowledge.SHA256Hex(string(visualContent)),
				Width: 100, Height: 100, Content: visualContent, PageNumber: intPointer(1),
			}},
			Metadata: json.RawMessage(`{"visualAssetCount":1}`),
		}, nil
	})
	processor := processorFunc(func(context.Context, knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error) {
		return knowledgeenrichment.ProviderResult{
			Provider: "dashscope", Model: "ocr",
			Elements: []knowledge.DocumentElement{{
				ElementType: knowledge.ElementOCRText, ContentText: ocrText,
			}},
		}, nil
	})
	store := &memoryStore{source: content}
	executor, err := NewExecutor(store, parser, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 32 * 1024,
		ChunkOptions:    knowledge.TextChunkOptions{MaxRunes: 256, OverlapRunes: 16},
		VisualConfig:    knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 1},
		VisualProcessor: processor,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(
		context.Background(), executorTask(content, "application/test"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) != 1 || result.Chunks[0].ElementIndex == nil || *result.Chunks[0].ElementIndex != 0 {
		t.Fatalf("chunks = %+v", result.Chunks)
	}
	var artifact elementArtifact
	if err := json.Unmarshal(store.artifact, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 5 || len(artifact.Elements) != 2 ||
		artifact.ElementMerge.SuppressedCount != 1 ||
		artifact.ElementMerge.Decisions[1].DuplicateOfIndex == nil ||
		*artifact.ElementMerge.Decisions[1].DuplicateOfIndex != 0 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestExecutorRejectsVisualOnlyDocumentWhenProcessorIsDisabled(t *testing.T) {
	content := []byte("source")
	parser := parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{
			ParserVersion: "image-parser-v1",
			VisualAssets: []knowledgeparser.VisualAsset{{
				Index: 0, Kind: knowledgeparser.VisualAssetDocumentPage, PageNumber: intPointer(1),
				SourcePath: "pages/1", MediaType: "application/pdf", SizeBytes: int64(len(content)),
				SHA256: knowledge.SHA256Hex(string(content)),
			}},
			Metadata: json.RawMessage(`{"visualAssetCount":1}`),
		}, nil
	})
	executor, err := NewExecutor(&memoryStore{source: content}, parser, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 16 * 1024,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(
		context.Background(), executorTask(content, "application/pdf"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if !errors.Is(err, knowledgeworker.ErrPermanentInput) {
		t.Fatalf("Execute error = %v", err)
	}
}

func TestExecutorClassifiesUnsupportedVisualInputAsPermanent(t *testing.T) {
	content := []byte("source")
	parser := parserFunc(func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error) {
		return knowledgeparser.Result{
			ParserVersion: "pdf-parser-v1",
			VisualAssets: []knowledgeparser.VisualAsset{{
				Index: 0, Kind: knowledgeparser.VisualAssetDocumentPage, PageNumber: intPointer(1),
				SourcePath: "pages/1", MediaType: "application/pdf", SizeBytes: int64(len(content)),
				SHA256: knowledge.SHA256Hex(string(content)),
			}},
		}, nil
	})
	executor, err := NewExecutor(&memoryStore{source: content}, parser, Config{
		MaxSourceBytes: 1024, MaxArtifactBytes: 16 * 1024,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: 2, MinPixels: 4096},
		VisualProcessor: processorFunc(func(context.Context, knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error) {
			return knowledgeenrichment.ProviderResult{}, errors.Join(
				knowledgeenrichment.ErrUnsupportedInput, errors.New("file_url is unsupported"),
			)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(
		context.Background(), executorTask(content, "application/pdf"),
		func(context.Context, knowledgeworker.CheckpointUpdate) error { return nil },
	)
	if !errors.Is(err, knowledgeworker.ErrPermanentInput) || !errors.Is(err, knowledgeenrichment.ErrUnsupportedInput) {
		t.Fatalf("Execute error = %v", err)
	}
}

func executorTask(content []byte, mediaType string) knowledgeworker.Task {
	return knowledgeworker.Task{
		ID: uuid.New(), DocumentVersionID: uuid.New(), DocumentID: uuid.New(), CreatedBy: uuid.New(),
		PipelineVersion: "ingestion-v1",
		Source: objectstore.ObjectRef{
			Bucket: objectstore.BucketKnowledgeSources, ObjectKey: "knowledge-source/object", ETag: "etag",
			SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex(string(content)),
			MediaType: mediaType, OriginalName: "manual.md",
		},
	}
}

type memoryStore struct {
	source   []byte
	artifact []byte
}

type parserFunc func(context.Context, knowledgeparser.Input) (knowledgeparser.Result, error)

type processorFunc func(context.Context, knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error)

type recordingEmbedder struct {
	mu         sync.Mutex
	inputTypes []knowledge.EmbeddingInputType
}

func (e *recordingEmbedder) Embed(_ context.Context, request knowledge.EmbeddingRequest) (knowledge.EmbeddingResult, error) {
	e.mu.Lock()
	e.inputTypes = append(e.inputTypes, request.InputType)
	e.mu.Unlock()
	vectors := make([][]float32, len(request.Texts))
	for index, text := range request.Texts {
		vector := make([]float32, 1024)
		vector[len([]rune(text))%len(vector)] = 1
		vectors[index] = vector
	}
	return knowledge.EmbeddingResult{
		Vectors: vectors, Usage: knowledge.EmbeddingUsage{TotalTokens: len(request.Texts)},
	}, nil
}

func (e *recordingEmbedder) inputTypeCount(target knowledge.EmbeddingInputType) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, inputType := range e.inputTypes {
		if inputType == target {
			count++
		}
	}
	return count
}

func intPointer(value int) *int { return &value }

func (f parserFunc) Parse(ctx context.Context, input knowledgeparser.Input) (knowledgeparser.Result, error) {
	return f(ctx, input)
}

func (f processorFunc) Process(ctx context.Context, request knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error) {
	return f(ctx, request)
}

func (s *memoryStore) Put(_ context.Context, input objectstore.PutInput) (objectstore.ObjectRef, error) {
	content, err := io.ReadAll(input.Content)
	if err != nil {
		return objectstore.ObjectRef{}, err
	}
	s.artifact = content
	return objectstore.ObjectRef{
		Bucket: input.Bucket, ObjectKey: input.ObjectKey, ETag: "artifact-etag",
		SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex(string(content)),
		MediaType: input.MediaType, OriginalName: input.OriginalName,
	}, nil
}

func (s *memoryStore) Get(_ context.Context, ref objectstore.ObjectRef) (objectstore.ReadResult, error) {
	return objectstore.ReadResult{
		Content: io.NopCloser(bytes.NewReader(s.source)), SizeBytes: int64(len(s.source)),
		ETag: ref.ETag, MediaType: ref.MediaType,
	}, nil
}

func (*memoryStore) Remove(context.Context, objectstore.ObjectRef) error { return nil }
func (*memoryStore) Close() error                                        { return nil }
