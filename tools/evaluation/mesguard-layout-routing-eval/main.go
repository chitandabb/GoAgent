// Command mesguard-layout-routing-eval evaluates local ONNX page routing on a fixed public corpus.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/onnxlayout"
	"github.com/chitandabb/GoAgent/internal/platform/pdfiumrenderer"
)

const evaluatorVersion = "layout-routing-eval-v2"

type commandOptions struct {
	configPath                          string
	corpusPath                          string
	casesPath                           string
	corpusRoot                          string
	modelPath                           string
	manifestPath                        string
	runtimePath                         string
	outputPath                          string
	summaryPath                         string
	minimumIoU                          float64
	renderDPI                           int
	maxPixels                           int64
	intraOpThreads                      int
	interOpThreads                      int
	disableDecorativePictureArbitration bool
	timeout                             time.Duration
}

type parsedDocument struct {
	content []byte
	result  knowledgeparser.Result
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	options, err := parseFlags(args)
	if err != nil {
		return err
	}
	corpus, err := readJSON[knowledgelayout.RoutingCorpus](options.corpusPath)
	if err != nil {
		return err
	}
	cases, err := readJSONL[knowledgelayout.RoutingEvaluationCase](options.casesPath)
	if err != nil {
		return err
	}
	if err := corpus.ValidateCases(cases); err != nil {
		return err
	}
	verificationStartedAt := time.Now()
	documents, err := verifyCorpusFiles(options.corpusRoot, corpus)
	if err != nil {
		return err
	}
	verificationMillis := elapsedMillis(verificationStartedAt)
	knowledgeConfig, err := loadKnowledgeConfig(options)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	observations, summary, err := executeEvaluation(ctx, options, knowledgeConfig, corpus, documents, cases)
	if err != nil {
		return err
	}
	summary.CorpusVerificationMillis = verificationMillis
	if err := writeEvaluationFiles(options.outputPath, options.summaryPath, observations, summary); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"summary page_class_macro_f1=%.4f route_macro_f1=%.4f actionable_route_macro_f1=%.4f high_value_miss=%.4f cloud_region_avoidance=%.4f p95_ms=%.2f\n",
		summary.PageClassMacroF1, summary.RouteMacroF1, summary.ActionableRouteMacroF1,
		summary.HighValueVisualMissRate,
		summary.CloudBoundRegionAvoidanceRate, summary.P95PageDurationMillis,
	)
	return nil
}

func parseFlags(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-layout-routing-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	flags.StringVar(&options.configPath, "config", "config/mesguard.toml", "MESGuard TOML configuration")
	flags.StringVar(&options.corpusPath, "corpus", "testdata/layout-routing-public-v1.corpus.json", "public corpus manifest")
	flags.StringVar(&options.casesPath, "cases", "testdata/layout-routing-public-v1.jsonl", "page annotations")
	flags.StringVar(&options.corpusRoot, "root", "output/evaluation/layout-routing-corpus", "downloaded corpus root")
	flags.StringVar(&options.modelPath, "model", "output/models/pp-doclayout-m/pp-doclayout-m.onnx", "ONNX model path")
	flags.StringVar(&options.manifestPath, "manifest", "config/models/pp-doclayout-m.json", "model manifest path")
	flags.StringVar(&options.runtimePath, "runtime", "output/runtime/onnxruntime-win-x64-1.28.0/lib/onnxruntime.dll", "ONNX Runtime library path")
	flags.StringVar(&options.outputPath, "output", "output/evaluation/layout-routing-public-v1.observations.jsonl", "observation JSONL output")
	flags.StringVar(&options.summaryPath, "summary", "output/evaluation/layout-routing-public-v1.summary.json", "summary JSON output")
	flags.Float64Var(&options.minimumIoU, "minimum-iou", 0.3, "minimum IoU for a high-value visual match")
	flags.IntVar(&options.renderDPI, "render-dpi", 0, "override configured render DPI; zero keeps configuration")
	flags.Int64Var(&options.maxPixels, "max-raster-pixels", 0, "override configured raster pixel limit; zero keeps configuration")
	flags.IntVar(&options.intraOpThreads, "intra-op-threads", 0, "override ONNX intra-op threads; zero keeps configuration")
	flags.IntVar(&options.interOpThreads, "inter-op-threads", 0, "override ONNX inter-op threads; zero keeps configuration")
	flags.BoolVar(&options.disableDecorativePictureArbitration, "disable-decorative-picture-arbitration", false, "disable cross-label decorative/picture duplicate suppression for A/B evaluation")
	flags.DurationVar(&options.timeout, "timeout", 10*time.Minute, "total evaluation timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-layout-routing-eval [-config path] [-corpus path] [-cases path] [-root path] [-model path] [-manifest path] [-runtime path] [-output path] [-summary path] [-minimum-iou ratio] [-render-dpi value] [-max-raster-pixels value] [-timeout duration]")
	}
	if options.minimumIoU <= 0 || options.minimumIoU > 1 || options.timeout <= 0 ||
		(options.renderDPI != 0 && (options.renderDPI < 72 || options.renderDPI > 600)) ||
		(options.maxPixels != 0 && (options.maxPixels < 1 || options.maxPixels > 1_000_000_000)) ||
		(options.intraOpThreads != 0 && (options.intraOpThreads < 1 || options.intraOpThreads > 64)) ||
		(options.interOpThreads != 0 && (options.interOpThreads < 1 || options.interOpThreads > 64)) {
		return commandOptions{}, errors.New("evaluation overrides are invalid")
	}
	return options, nil
}

func loadKnowledgeConfig(options commandOptions) (config.KnowledgeConfig, error) {
	var decoded struct {
		Knowledge config.KnowledgeConfig `toml:"knowledge"`
	}
	if _, err := toml.DecodeFile(options.configPath, &decoded); err != nil {
		return config.KnowledgeConfig{}, fmt.Errorf("decode config %q: %w", options.configPath, err)
	}
	decoded.Knowledge.Layout.Enabled = true
	decoded.Knowledge.Layout.ModelPath = options.modelPath
	decoded.Knowledge.Layout.ManifestPath = options.manifestPath
	decoded.Knowledge.Layout.RuntimeLibraryPath = options.runtimePath
	if options.disableDecorativePictureArbitration {
		decoded.Knowledge.Layout.SuppressDecorativePictureDuplicates = false
	}
	if options.renderDPI != 0 {
		decoded.Knowledge.Layout.RenderDPI = options.renderDPI
	}
	if options.maxPixels != 0 {
		decoded.Knowledge.Layout.MaxRasterPixels = options.maxPixels
	}
	if options.intraOpThreads != 0 {
		decoded.Knowledge.Layout.IntraOpThreads = options.intraOpThreads
	}
	if options.interOpThreads != 0 {
		decoded.Knowledge.Layout.InterOpThreads = options.interOpThreads
	}
	if err := decoded.Knowledge.Validate(); err != nil {
		return config.KnowledgeConfig{}, err
	}
	return decoded.Knowledge, nil
}

func executeEvaluation(
	ctx context.Context,
	options commandOptions,
	knowledgeConfig config.KnowledgeConfig,
	corpus knowledgelayout.RoutingCorpus,
	documents map[string]knowledgelayout.RoutingCorpusDocument,
	cases []knowledgelayout.RoutingEvaluationCase,
) (observations []knowledgelayout.RoutingEvaluationObservation, summary knowledgelayout.RoutingEvaluationSummary, err error) {
	initializationStartedAt := time.Now()
	layout := knowledgeConfig.Layout
	router, err := onnxlayout.New(onnxlayout.Config{
		RuntimeLibraryPath: layout.RuntimeLibraryPath,
		ModelPath:          layout.ModelPath, ModelSHA256: layout.ModelSHA256,
		ManifestPath: layout.ManifestPath, Provider: layout.Provider,
		ModelName: layout.ModelName, ModelVersion: layout.ModelVersion,
		PreprocessVersion: layout.PreprocessVersion, PostprocessVersion: layout.PostprocessVersion,
		InputWidth: layout.InputWidth, InputHeight: layout.InputHeight,
		IntraOpThreads: layout.IntraOpThreads, InterOpThreads: layout.InterOpThreads,
		InferenceTimeout:   time.Duration(layout.InferenceTimeoutMillis) * time.Millisecond,
		MaxConcurrentPages: layout.MaxConcurrentPages, MaxRegions: layout.MaxRegions,
		MaxRasterPixels: layout.MaxRasterPixels, MaxRasterBytes: layout.MaxRasterBytes,
		SuppressDecorativePictureDuplicates:  layout.SuppressDecorativePictureDuplicates,
		DecorativePictureMinIoU:              layout.DecorativePictureMinIoU,
		DecorativePictureMaxAreaRatio:        layout.DecorativePictureMaxAreaRatio,
		DecorativePictureMinConfidenceMargin: layout.DecorativePictureMinConfidenceMargin,
	})
	if err != nil {
		return nil, summary, err
	}
	defer func() { err = errors.Join(err, router.Close()) }()
	renderer, err := pdfiumrenderer.OpenWASM(ctx, pdfiumrenderer.Config{
		RendererVersion: layout.RendererVersion, MaxSourceBytes: knowledgeConfig.MaxUploadBytes,
		MaxRasterPixels: layout.MaxRasterPixels, MaxRasterBytes: layout.MaxRasterBytes,
		MaxExtractedRunes: knowledgeConfig.ParserMaxExtractedRunes,
		MaxConcurrent:     layout.MaxConcurrentPages,
		AcquireTimeout:    time.Duration(layout.RendererAcquireMillis) * time.Millisecond,
		RenderTimeout:     time.Duration(layout.RendererTimeoutMillis) * time.Millisecond,
	})
	if err != nil {
		return nil, summary, err
	}
	defer func() { err = errors.Join(err, renderer.Close()) }()
	planner, err := knowledgelayout.NewRoutePlanner(knowledgelayout.PlannerConfig{
		MinNativeTextRunes:      layout.MinNativeTextRunes,
		MinNativePrintableRatio: layout.MinNativePrintableRatio,
		MinRegionConfidence:     layout.MinRegionConfidence, MaxRegions: layout.MaxRegions,
		MaxRasterPixels: layout.MaxRasterPixels, MaxRasterBytes: layout.MaxRasterBytes,
	}, router)
	if err != nil {
		return nil, summary, err
	}
	analyzer, err := knowledgelayout.NewPageAnalyzer(knowledgelayout.AnalyzerConfig{
		RenderDPI: layout.RenderDPI, MaxSourceBytes: knowledgeConfig.MaxUploadBytes,
		MaxRasterPixels: layout.MaxRasterPixels, MaxRasterBytes: layout.MaxRasterBytes,
	}, planner, renderer)
	if err != nil {
		return nil, summary, err
	}
	initializationMillis := elapsedMillis(initializationStartedAt)

	pdfParser, err := knowledgeparser.NewPDFParser(parserLimits(knowledgeConfig))
	if err != nil {
		return nil, summary, err
	}
	parsed := make(map[string]parsedDocument)
	observations = make([]knowledgelayout.RoutingEvaluationObservation, 0, len(cases))
	parserDurationMillis := 0.0
	for index, definition := range cases {
		if err := ctx.Err(); err != nil {
			return nil, summary, err
		}
		document := documents[definition.DocumentID]
		parsedDoc, exists := parsed[definition.DocumentID]
		if !exists {
			content, readErr := os.ReadFile(filepath.Join(options.corpusRoot, document.FileName))
			if readErr != nil {
				return nil, summary, readErr
			}
			parseStartedAt := time.Now()
			result, parseErr := pdfParser.Parse(ctx, knowledgeparser.Input{
				MediaType: document.MediaType, OriginalName: document.FileName, Content: content,
			})
			parserDurationMillis += elapsedMillis(parseStartedAt)
			if parseErr != nil {
				return nil, summary, fmt.Errorf("parse corpus document %q: %w", document.DocumentID, parseErr)
			}
			parsedDoc = parsedDocument{content: content, result: result}
			parsed[definition.DocumentID] = parsedDoc
		}
		page, pageErr := pageObservation(parsedDoc.result, definition.PageNumber)
		if pageErr != nil {
			return nil, summary, fmt.Errorf("case %q: %w", definition.CaseID, pageErr)
		}
		analysisRequest := knowledgelayout.AnalysisRequest{
			Source: knowledgelayout.DocumentSource{
				MediaType: document.MediaType, Content: parsedDoc.content, SHA256: document.SHA256,
			},
			Page: knowledgelayout.PageInput{
				PageNumber: page.PageNumber,
				NativeText: knowledgelayout.NativeTextSignals{
					RuneCount: page.NativeTextRunes, NonWhitespaceRunes: page.NonWhitespaceRunes,
					PrintableRatio: page.PrintableRatio, ExtractionComplete: page.ExtractionComplete,
				},
				VisualCandidateCount:  page.VisualCandidateCount,
				VisualCandidatesKnown: page.VisualCandidatesKnown,
			},
		}
		analysis, durationMillis, totalAllocated, peakHeap, analyzeErr := measureAnalysis(func() (knowledgelayout.AnalysisResult, error) {
			return analyzer.Analyze(ctx, analysisRequest)
		})
		if analyzeErr != nil {
			return nil, summary, fmt.Errorf("analyze case %q: %w", definition.CaseID, analyzeErr)
		}
		observation, observationErr := knowledgelayout.NewRoutingEvaluationObservation(
			definition, fmt.Sprintf("%s-%d-%d", definition.CaseID, time.Now().UTC().UnixNano(), index),
			analysis.Plan, analysis.Render, durationMillis, totalAllocated, peakHeap, options.minimumIoU,
		)
		if observationErr != nil {
			return nil, summary, observationErr
		}
		observations = append(observations, observation)
		fmt.Fprintf(os.Stdout, "%s class=%s routes=%v regions=%d duration_ms=%.2f\n",
			definition.CaseID, observation.PredictedPageClass, observation.PredictedRoutes,
			len(observation.Regions), observation.DurationMillis,
		)
	}
	summary, err = knowledgelayout.EvaluateRouting(cases, observations, evaluatorVersion, options.minimumIoU)
	if err != nil {
		return nil, summary, err
	}
	summary.RuntimeInitializationMillis = initializationMillis
	summary.ParserDurationMillis = parserDurationMillis
	summary.ParsedDocuments = len(parsed)
	summary.RequestedRenderDPI = layout.RenderDPI
	summary.MaxRasterPixels = layout.MaxRasterPixels
	summary.MaxRasterBytes = layout.MaxRasterBytes
	if summary.DatasetVersion != corpus.DatasetVersion {
		return nil, summary, errors.New("routing evaluation summary datasetVersion does not match corpus")
	}
	return observations, summary, nil
}

func parserLimits(cfg config.KnowledgeConfig) knowledgeparser.Limits {
	return knowledgeparser.Limits{
		MaxDocumentUnits: cfg.ParserMaxDocumentUnits, MaxArchiveEntries: cfg.ParserMaxArchiveEntries,
		MaxExpandedBytes: cfg.ParserMaxExpandedBytes, MaxXMLBytes: cfg.ParserMaxXMLBytes,
		MaxExtractedRunes: cfg.ParserMaxExtractedRunes, MaxSpreadsheetRows: cfg.ParserMaxSpreadsheetRows,
		MaxSpreadsheetColumns: cfg.ParserMaxSpreadsheetColumns, MaxVisualAssets: cfg.ParserMaxVisualAssets,
		MaxVisualAssetBytes: cfg.ParserMaxVisualAssetBytes, MaxTotalVisualBytes: cfg.ParserMaxTotalVisualBytes,
	}
}

func pageObservation(result knowledgeparser.Result, pageNumber int) (knowledgeparser.PageObservation, error) {
	for _, page := range result.Pages {
		if page.PageNumber == pageNumber {
			return page, nil
		}
	}
	return knowledgeparser.PageObservation{}, errors.New("annotated page is absent from parser observations")
}

func verifyCorpusFiles(
	root string,
	corpus knowledgelayout.RoutingCorpus,
) (map[string]knowledgelayout.RoutingCorpusDocument, error) {
	result := make(map[string]knowledgelayout.RoutingCorpusDocument, len(corpus.Documents))
	for _, document := range corpus.Documents {
		path := filepath.Join(root, document.FileName)
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open corpus file %q: %w", document.FileName, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, statErr
		}
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return nil, fmt.Errorf("hash corpus file %q: %w", document.FileName, err)
		}
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if info.Size() != document.SizeBytes || actualHash != document.SHA256 {
			return nil, fmt.Errorf("corpus file %q does not match its size/SHA-256 contract", document.FileName)
		}
		result[document.DocumentID] = document
	}
	return result, nil
}

func measureAnalysis(run func() (knowledgelayout.AnalysisResult, error)) (
	analysis knowledgelayout.AnalysisResult,
	durationMillis float64,
	totalAllocated uint64,
	peakHeap uint64,
	err error,
) {
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	peakHeap = before.HeapAlloc
	done := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				if current.HeapAlloc > peakHeap {
					peakHeap = current.HeapAlloc
				}
			case <-done:
				return
			}
		}
	}()
	startedAt := time.Now()
	analysis, err = run()
	durationMillis = elapsedMillis(startedAt)
	close(done)
	wait.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > peakHeap {
		peakHeap = after.HeapAlloc
	}
	if after.TotalAlloc >= before.TotalAlloc {
		totalAllocated = after.TotalAlloc - before.TotalAlloc
	}
	return analysis, durationMillis, totalAllocated, peakHeap, err
}

func readJSON[T any](path string) (T, error) {
	var value T
	contents, err := os.ReadFile(path)
	if err != nil {
		return value, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return value, fmt.Errorf("decode %s: %w", path, err)
	}
	return value, nil
}

func readJSONL[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var result []T
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		result = append(result, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s contains no records", path)
	}
	return result, nil
}

func writeEvaluationFiles(
	outputPath string,
	summaryPath string,
	observations []knowledgelayout.RoutingEvaluationObservation,
	summary knowledgelayout.RoutingEvaluationSummary,
) error {
	for _, path := range []string{outputPath, summaryPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	}
	outputTemporary := outputPath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	summaryTemporary := summaryPath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	defer os.Remove(outputTemporary)
	defer os.Remove(summaryTemporary)
	file, err := os.OpenFile(outputTemporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryTemporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	if err := replaceFile(outputTemporary, outputPath); err != nil {
		return err
	}
	return replaceFile(summaryTemporary, summaryPath)
}

func replaceFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}

func elapsedMillis(startedAt time.Time) float64 {
	return float64(time.Since(startedAt).Microseconds()) / 1000
}
