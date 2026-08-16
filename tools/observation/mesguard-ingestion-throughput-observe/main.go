// Command mesguard-ingestion-throughput-observe runs a bounded worker-core
// ingestion pair over pinned real documents. RabbitMQ delivery is deliberately
// excluded until the worker-core bottlenecks have been measured independently.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	platformminio "github.com/chitandabb/GoAgent/internal/platform/minio"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

type commandOptions struct {
	corpusPath                    string
	sourceRoot                    string
	outputPath                    string
	maxDocuments                  int
	documentIDs                   []string
	repetitions                   int
	experimentDocumentConcurrency int
	timeout                       time.Duration
	validateOnly                  bool
	auditOnly                     bool
	estimateOnly                  bool
	databaseAblation              bool
	documentConcurrencyAblation   bool
	executeProvider               bool
	auditOutputPath               string
	embeddingPriceCNYPerMillion   float64
	maxProviderCostCNY            float64
	providerRPM                   int
	providerTPM                   int
}

const defaultThroughputOutputPath = "output/evaluation/rag-ingestion-throughput-v1.observations.jsonl"

type corpusManifest struct {
	DatasetVersion string           `json:"datasetVersion"`
	Documents      []corpusDocument `json:"documents"`
}

type corpusDocument struct {
	DocumentID  string `json:"documentId"`
	Title       string `json:"title"`
	Publisher   string `json:"publisher"`
	SourceURL   string `json:"sourceUrl"`
	DownloadURL string `json:"downloadUrl"`
	UsageBasis  string `json:"usageBasis"`
	FileName    string `json:"fileName"`
	MediaType   string `json:"mediaType"`
	FormatClass string `json:"formatClass"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
	PageCount   int    `json:"pageCount"`
}

const (
	formatClassNativePDF  = "native-pdf"
	formatClassScannedPDF = "scanned-pdf"
	formatClassDOCX       = "docx"
	formatClassXLSX       = "xlsx"
	formatClassPPTX       = "pptx"
	formatClassPNG        = "png"
	formatClassJPEG       = "jpeg"
	formatClassText       = "text"
)

type loadedDocument struct {
	definition corpusDocument
	content    []byte
}

type variantConfig struct {
	variant                knowledgeingestion.ThroughputVariant
	documentConcurrency    int
	embeddingBatchSize     int
	embeddingMaxConcurrent int
	chunkWriteBatchSize    int
}

type queuedDocument struct {
	definition corpusDocument
	source     objectstore.ObjectRef
	documentID uuid.UUID
	versionID  uuid.UUID
	taskID     uuid.UUID
	message    knowledgeworker.IncomingMessage
	outcome    knowledgeworker.Outcome
	facts      ingestionFacts
}

type ingestionFacts struct {
	TaskStatus           string
	ParserMetadata       string `gorm:"column:parser_metadata"`
	ChunkCount           int    `gorm:"column:chunk_count"`
	ArtifactBucket       string `gorm:"column:artifact_bucket"`
	ArtifactKey          string `gorm:"column:artifact_key"`
	ArtifactVersion      string `gorm:"column:artifact_version"`
	ArtifactETag         string `gorm:"column:artifact_etag"`
	ArtifactSize         int64  `gorm:"column:artifact_size"`
	ArtifactSHA256       string `gorm:"column:artifact_sha256"`
	ArtifactMediaType    string `gorm:"column:artifact_media_type"`
	ArtifactOriginalName string `gorm:"column:artifact_original_name"`
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-ingestion-throughput-observe")
	defer platformlogger.Sync(log)
	ctx := context.Background()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("knowledge ingestion throughput observation failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	manifest, err := readCorpus(options.corpusPath)
	if err != nil {
		return err
	}
	definitions, err := selectCorpusDocuments(manifest, options.maxDocuments, options.documentIDs)
	if err != nil {
		return err
	}
	documents, err := loadDocuments(definitions, options.sourceRoot)
	if err != nil {
		return err
	}
	if options.validateOnly {
		fmt.Printf("dataset=%s documents=%d bytes=%d corpus_fingerprint=%s\n",
			manifest.DatasetVersion, len(documents), totalSourceBytes(documents), corpusFingerprint(manifest.DatasetVersion, documents))
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if options.estimateOnly {
		return estimateProviderRequests(ctx, cfg, manifest.DatasetVersion, documents, options)
	}
	if options.auditOnly {
		return runCorpusAudit(ctx, cfg.Knowledge, manifest.DatasetVersion, documents, options.auditOutputPath)
	}
	if options.databaseAblation {
		if options.outputPath == defaultThroughputOutputPath {
			options.outputPath = "output/evaluation/rag-ingestion-db-ablation-v1.observations.jsonl"
		}
		return runDatabaseAblation(ctx, cfg, manifest.DatasetVersion, documents, options, log)
	}
	if options.documentConcurrencyAblation && options.outputPath == defaultThroughputOutputPath {
		options.outputPath = "output/evaluation/rag-ingestion-document-concurrency-v1.observations.jsonl"
	}
	if !options.executeProvider {
		return errors.New("provider execution is disabled; review the corpus and add -execute-provider")
	}
	if !cfg.MinIO.Enabled || !cfg.Models.Embedding.Enabled {
		return errors.New("throughput observation requires MinIO and the embedding model")
	}
	providerPlan, err := estimateProviderPlan(ctx, cfg, documents, options)
	if err != nil {
		return err
	}
	printProviderPlan(manifest.DatasetVersion, providerPlan, options, false)
	if providerPlan.EstimatedCostCNY > options.maxProviderCostCNY {
		return fmt.Errorf(
			"provider preflight blocked: estimated cost %.4f CNY exceeds budget %.4f CNY; reduce the corpus or explicitly raise -max-provider-cost-cny",
			providerPlan.EstimatedCostCNY, options.maxProviderCostCNY,
		)
	}
	runCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	db, closeDB, err := platformpostgres.Open(runCtx, cfg.Postgres, log.Named("postgres"))
	if err != nil {
		return err
	}
	defer closeDB()
	actorID := uuid.New()
	username := "ingestion-eval-" + strings.ReplaceAll(actorID.String(), "-", "")
	if err := db.WithContext(runCtx).Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, must_change_password)
VALUES (?, ?, 'Ingestion Throughput Evaluation', 'evaluation-only-not-a-login-secret', 'admin', false)`,
		actorID, username,
	).Error; err != nil {
		return fmt.Errorf("create throughput evaluation actor: %w", err)
	}
	defer func() {
		_ = db.WithContext(context.WithoutCancel(runCtx)).Exec("DELETE FROM users WHERE id = ?", actorID).Error
	}()
	store, err := platformminio.Open(runCtx, cfg.MinIO)
	if err != nil {
		return err
	}
	providerEmbeddingConfig := providerEvaluationEmbeddingConfig(
		cfg.Models.Embedding, options.providerRPM, options.providerTPM,
	)
	embedder, err := platformembedding.NewClient(providerEmbeddingConfig, nil)
	if err != nil {
		return err
	}
	providerCtx, cancelProvider := context.WithCancel(runCtx)
	defer cancelProvider()
	guardedProvider, err := newGuardedEmbedder(
		embedder,
		providerTokenBudget(options.maxProviderCostCNY, options.embeddingPriceCNYPerMillion),
		cancelProvider,
	)
	if err != nil {
		return err
	}
	profile, err := cfg.Models.Embedding.Profile()
	if err != nil {
		return err
	}
	if err := platformpostgres.NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(runCtx, profile); err != nil {
		return err
	}
	parser, err := buildParser(cfg.Knowledge)
	if err != nil {
		return err
	}
	environmentFingerprint := buildEnvironmentFingerprint(cfg, profile)
	selectedCorpusFingerprint := corpusFingerprint(manifest.DatasetVersion, documents)
	baseline := variantConfig{
		variant: knowledgeingestion.ThroughputBaseline, documentConcurrency: 1,
		embeddingBatchSize: 1, embeddingMaxConcurrent: 1, chunkWriteBatchSize: 1,
	}
	if options.documentConcurrencyAblation {
		baseline.embeddingBatchSize = cfg.Models.Embedding.BatchSize
		baseline.embeddingMaxConcurrent = cfg.Models.Embedding.MaxConcurrent
		baseline.chunkWriteBatchSize = cfg.Knowledge.ChunkWriteBatchSize
	}
	experiment := variantConfig{
		variant:                knowledgeingestion.ThroughputExperiment,
		documentConcurrency:    options.experimentDocumentConcurrency,
		embeddingBatchSize:     cfg.Models.Embedding.BatchSize,
		embeddingMaxConcurrent: cfg.Models.Embedding.MaxConcurrent,
		chunkWriteBatchSize:    cfg.Knowledge.ChunkWriteBatchSize,
	}
	if baseline == experiment {
		return errors.New("baseline and experiment configurations are identical")
	}
	observations := make([]knowledgeingestion.ThroughputObservation, 0, options.repetitions*2)
	for repetition := 1; repetition <= options.repetitions; repetition++ {
		variants := []variantConfig{baseline, experiment}
		if repetition%2 == 0 {
			slices.Reverse(variants)
		}
		for _, variant := range variants {
			observation, err := runVariant(
				providerCtx, db, store, parser, guardedProvider, profile, cfg, manifest.DatasetVersion,
				selectedCorpusFingerprint, environmentFingerprint, documents, actorID, repetition, variant,
			)
			if guardErr := guardedProvider.Err(); guardErr != nil {
				return fmt.Errorf("run repetition %d variant %s: %w", repetition, variant.variant, guardErr)
			}
			if err != nil {
				return fmt.Errorf("run repetition %d variant %s: %w", repetition, variant.variant, err)
			}
			if err := observation.Validate(); err != nil {
				return err
			}
			observations = append(observations, observation)
			log.Info("knowledge ingestion throughput variant completed",
				zap.Int("repetition", repetition), zap.String("variant", string(variant.variant)),
				zap.Int64("duration_ms", observation.DurationMillis), zap.Int("chunks", observation.Chunks),
				zap.Int("embedding_requests", observation.EmbeddingRequests), zap.Int("failed", observation.FailedDocuments))
		}
	}
	slices.SortFunc(observations, func(left, right knowledgeingestion.ThroughputObservation) int {
		if left.Repetition != right.Repetition {
			return left.Repetition - right.Repetition
		}
		return strings.Compare(string(left.Variant), string(right.Variant))
	})
	if err := writeObservations(options.outputPath, observations); err != nil {
		return err
	}
	summary, err := knowledgeingestion.EvaluateThroughput(observations, 40)
	if err != nil {
		return err
	}
	fmt.Printf("dataset=%s pairs=%d eligible=%t integrity=%t median_throughput_increase=%.2f%% total_embedding_tokens=%d\n",
		summary.DatasetVersion, summary.Pairs, summary.AcceptanceEligible, summary.IntegrityPreserved,
		summary.MedianThroughputIncreasePercent,
		summary.Baseline.TotalEmbeddingTokens+summary.Experiment.TotalEmbeddingTokens)
	return nil
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-ingestion-throughput-observe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := commandOptions{}
	var documentIDs string
	flags.StringVar(&options.corpusPath, "corpus", "testdata/rag-ingestion-throughput-v1.corpus.json", "pinned corpus manifest")
	flags.StringVar(&options.sourceRoot, "source-root", "output/evaluation/layout-routing-corpus", "local corpus directory")
	flags.StringVar(&options.outputPath, "output", defaultThroughputOutputPath, "observation JSONL")
	flags.IntVar(&options.maxDocuments, "max-documents", 1, "maximum documents selected from the corpus")
	flags.StringVar(&documentIDs, "document-ids", "", "comma-separated document IDs; overrides max-documents")
	flags.IntVar(&options.repetitions, "repetitions", 1, "paired cold repetitions")
	flags.IntVar(&options.experimentDocumentConcurrency, "experiment-document-concurrency", 2, "bounded experiment worker concurrency")
	flags.DurationVar(&options.timeout, "timeout", 15*time.Minute, "whole command timeout")
	flags.BoolVar(&options.validateOnly, "validate-only", false, "validate corpus files without infrastructure or provider calls")
	flags.BoolVar(&options.auditOnly, "audit-only", false, "parse and classify the corpus without infrastructure or provider calls")
	flags.BoolVar(&options.estimateOnly, "estimate-only", false, "parse locally and estimate embedding request counts")
	flags.BoolVar(&options.databaseAblation, "database-ablation", false, "measure PostgreSQL staging with deterministic local vectors")
	flags.BoolVar(&options.documentConcurrencyAblation, "document-concurrency-ablation", false, "compare document concurrency while keeping batching identical")
	flags.BoolVar(&options.executeProvider, "execute-provider", false, "allow real embedding requests")
	flags.StringVar(&options.auditOutputPath, "audit-output", "output/evaluation/rag-ingestion-corpus-audit.json", "corpus audit JSON output")
	flags.Float64Var(&options.embeddingPriceCNYPerMillion, "embedding-price-cny-per-million", defaultEmbeddingPriceCNYPerMillion, "embedding input price used by the preflight cost estimate")
	flags.Float64Var(&options.maxProviderCostCNY, "max-provider-cost-cny", defaultMaxProviderCostCNY, "hard estimated cost budget for the whole provider run")
	flags.IntVar(&options.providerRPM, "provider-rpm", defaultProviderRPM, "smoothed provider request limit per minute")
	flags.IntVar(&options.providerTPM, "provider-tpm", defaultProviderTPM, "smoothed estimated provider token limit per minute")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return commandOptions{}, errors.New("usage: mesguard-ingestion-throughput-observe [-validate-only|-audit-only|-estimate-only|-database-ablation|-execute-provider] [-max-documents n] [-repetitions n]")
	}
	if options.maxDocuments < 1 || options.maxDocuments > 40 || options.repetitions < 1 || options.repetitions > 5 ||
		options.experimentDocumentConcurrency < 1 || options.experimentDocumentConcurrency > 8 ||
		options.timeout < time.Minute || options.timeout > 2*time.Hour ||
		options.embeddingPriceCNYPerMillion <= 0 || options.embeddingPriceCNYPerMillion > 100 ||
		options.maxProviderCostCNY <= 0 || options.maxProviderCostCNY > 100 ||
		options.providerRPM < 1 || options.providerRPM > 100_000 ||
		options.providerTPM < 1_000 || options.providerTPM > 100_000_000 ||
		strings.TrimSpace(options.auditOutputPath) == "" ||
		boolCount(options.validateOnly, options.auditOnly, options.estimateOnly, options.databaseAblation, options.executeProvider) > 1 {
		return commandOptions{}, errors.New("throughput observation options are outside safety bounds")
	}
	if options.documentConcurrencyAblation &&
		(!(options.executeProvider || options.estimateOnly) || options.experimentDocumentConcurrency < 2) {
		return commandOptions{}, errors.New("document concurrency ablation requires provider execution or estimate-only and experiment concurrency of at least two")
	}
	parsedDocumentIDs, err := parseDocumentIDs(documentIDs)
	if err != nil {
		return commandOptions{}, err
	}
	options.documentIDs = parsedDocumentIDs
	return options, nil
}

func parseDocumentIDs(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 40 {
		return nil, errors.New("document-ids exceeds the 40-document safety bound")
	}
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" || len([]rune(id)) > 128 {
			return nil, errors.New("document-ids contains an invalid document ID")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("document ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func estimateProviderRequests(
	ctx context.Context,
	cfg config.Config,
	datasetVersion string,
	documents []loadedDocument,
	options commandOptions,
) error {
	plan, err := estimateProviderPlan(ctx, cfg, documents, options)
	if err != nil {
		return err
	}
	printProviderPlan(datasetVersion, plan, options, true)
	return nil
}

func chunksForProviderEstimate(
	parsed knowledgeparser.Result,
	options knowledge.TextChunkOptions,
) ([]knowledge.ChunkDraft, error) {
	if len(parsed.Elements) == 0 {
		return nil, nil
	}
	prepared, err := knowledgeingestion.PrepareSearchableElements(parsed.Elements)
	if err != nil {
		return nil, err
	}
	return knowledge.ChunkElements(prepared.Elements, options)
}

func readCorpus(path string) (corpusManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return corpusManifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest corpusManifest
	if err := decoder.Decode(&manifest); err != nil {
		return corpusManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return corpusManifest{}, errors.New("corpus contains multiple JSON values")
	}
	if strings.TrimSpace(manifest.DatasetVersion) == "" || len(manifest.Documents) == 0 || len(manifest.Documents) > 100 {
		return corpusManifest{}, errors.New("corpus identity is invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Documents))
	for _, document := range manifest.Documents {
		if strings.TrimSpace(document.DocumentID) == "" || strings.TrimSpace(document.Title) == "" ||
			strings.TrimSpace(document.Publisher) == "" || !validCorpusURL(document.SourceURL) ||
			!validCorpusURL(document.DownloadURL) || strings.TrimSpace(document.UsageBasis) == "" ||
			len([]rune(document.UsageBasis)) > 1000 || filepath.Base(document.FileName) != document.FileName ||
			strings.TrimSpace(document.MediaType) == "" || !validFormatClass(document.FormatClass) ||
			document.SizeBytes < 1 || document.PageCount < 0 ||
			!validSHA256(document.SHA256) {
			return corpusManifest{}, fmt.Errorf("corpus document %q is invalid", document.DocumentID)
		}
		if _, exists := seen[document.DocumentID]; exists {
			return corpusManifest{}, fmt.Errorf("corpus document %q is duplicated", document.DocumentID)
		}
		seen[document.DocumentID] = struct{}{}
	}
	return manifest, nil
}

func validCorpusURL(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func selectCorpusDocuments(
	manifest corpusManifest,
	maximum int,
	requestedIDs []string,
) ([]corpusDocument, error) {
	if len(requestedIDs) == 0 {
		count := min(maximum, len(manifest.Documents))
		return append([]corpusDocument(nil), manifest.Documents[:count]...), nil
	}
	byID := make(map[string]corpusDocument, len(manifest.Documents))
	for _, document := range manifest.Documents {
		byID[document.DocumentID] = document
	}
	selected := make([]corpusDocument, 0, len(requestedIDs))
	for _, id := range requestedIDs {
		document, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("corpus document %q does not exist", id)
		}
		selected = append(selected, document)
	}
	return selected, nil
}

func loadDocuments(definitions []corpusDocument, root string) ([]loadedDocument, error) {
	documents := make([]loadedDocument, 0, len(definitions))
	for _, definition := range definitions {
		contents, err := os.ReadFile(filepath.Join(root, definition.FileName))
		if err != nil {
			return nil, fmt.Errorf("read corpus document %s: %w", definition.DocumentID, err)
		}
		digest := sha256.Sum256(contents)
		if int64(len(contents)) != definition.SizeBytes || hex.EncodeToString(digest[:]) != definition.SHA256 {
			return nil, fmt.Errorf("corpus document %s size or sha256 changed", definition.DocumentID)
		}
		documents = append(documents, loadedDocument{definition: definition, content: contents})
	}
	return documents, nil
}

func buildParser(cfg config.KnowledgeConfig) (*knowledgeparser.Router, error) {
	limits := knowledgeparser.Limits{
		MaxDocumentUnits: cfg.ParserMaxDocumentUnits, MaxArchiveEntries: cfg.ParserMaxArchiveEntries,
		MaxExpandedBytes: cfg.ParserMaxExpandedBytes, MaxXMLBytes: cfg.ParserMaxXMLBytes,
		MaxExtractedRunes: cfg.ParserMaxExtractedRunes, MaxSpreadsheetRows: cfg.ParserMaxSpreadsheetRows,
		MaxSpreadsheetColumns: cfg.ParserMaxSpreadsheetColumns, MaxVisualAssets: cfg.ParserMaxVisualAssets,
		MaxVisualAssetBytes: cfg.ParserMaxVisualAssetBytes, MaxTotalVisualBytes: cfg.ParserMaxTotalVisualBytes,
	}
	pdfParser, err := knowledgeparser.NewPDFParser(limits)
	if err != nil {
		return nil, err
	}
	ooxmlParser, err := knowledgeparser.NewOOXMLParser(limits)
	if err != nil {
		return nil, err
	}
	imageParser, err := knowledgeparser.NewImageParser(limits)
	if err != nil {
		return nil, err
	}
	return knowledgeparser.NewRouter(knowledgeparser.TextParser{}, pdfParser, ooxmlParser, imageParser)
}

func runVariant(
	ctx context.Context,
	db *gorm.DB,
	store objectstore.Store,
	parser knowledgeingestion.Parser,
	embedder knowledge.Embedder,
	profile knowledge.EmbeddingProfile,
	cfg config.Config,
	datasetVersion, corpusHash, environmentHash string,
	documents []loadedDocument,
	actorID uuid.UUID,
	repetition int,
	variant variantConfig,
) (observation knowledgeingestion.ThroughputObservation, err error) {
	repository, err := platformpostgres.NewKnowledgeWorkerRepositoryWithBatchSize(db, variant.chunkWriteBatchSize)
	if err != nil {
		return observation, err
	}
	executor, err := knowledgeingestion.NewExecutor(store, parser, knowledgeingestion.Config{
		MaxSourceBytes: cfg.Knowledge.MaxUploadBytes, MaxArtifactBytes: cfg.MinIO.MaxObjectBytes,
		ChunkOptions: knowledge.TextChunkOptions{MaxRunes: cfg.Knowledge.ChunkMaxRunes, OverlapRunes: cfg.Knowledge.ChunkOverlapRunes},
		VisualConfig: knowledgeenrichment.Config{MaxEnrichments: cfg.Knowledge.MaxVisualEnrichments, MinPixels: cfg.Knowledge.MinVisualPixels},
		Embedding: &knowledgeingestion.EmbeddingConfig{
			Profile: profile, Embedder: embedder, BatchSize: variant.embeddingBatchSize,
			MaxConcurrent: variant.embeddingMaxConcurrent,
		},
	})
	if err != nil {
		return observation, err
	}
	worker, err := knowledgeworker.NewWorker(repository, executor, knowledgeworker.Config{
		WorkerID:      "ingestion-eval-" + uuid.NewString(),
		LeaseDuration: time.Duration(cfg.RabbitMQ.WorkerLeaseMillis) * time.Millisecond,
		RenewInterval: time.Duration(cfg.RabbitMQ.WorkerRenewIntervalMillis) * time.Millisecond,
	})
	if err != nil {
		return observation, err
	}
	queued := make([]queuedDocument, len(documents))
	defer func() {
		err = errors.Join(err, cleanupRun(context.WithoutCancel(ctx), db, store, queued))
	}()
	startedAt := time.Now()
	queueStartedAt := time.Now()
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(variant.documentConcurrency)
	for index := range documents {
		index := index
		group.Go(func() error {
			item, queueErr := queueDocument(
				groupCtx, db, store, documents[index], cfg, datasetVersion, actorID, repetition, variant.variant,
			)
			if queueErr == nil {
				queued[index] = item
			}
			return queueErr
		})
	}
	if err := group.Wait(); err != nil {
		return observation, err
	}
	queueDuration := time.Since(queueStartedAt)
	processStartedAt := time.Now()
	group, groupCtx = errgroup.WithContext(ctx)
	group.SetLimit(variant.documentConcurrency)
	for index := range queued {
		index := index
		group.Go(func() error {
			queued[index].outcome = worker.Process(groupCtx, queued[index].message)
			facts, factsErr := readIngestionFacts(groupCtx, db, queued[index].taskID)
			if factsErr == nil {
				queued[index].facts = facts
			}
			return factsErr
		})
	}
	if err := group.Wait(); err != nil {
		return observation, err
	}
	processDuration := time.Since(processStartedAt)
	duration := time.Since(startedAt)
	observation = buildObservation(
		datasetVersion, corpusHash, environmentHash, repetition, variant, queued,
		duration, queueDuration, processDuration,
	)
	return observation, nil
}

func queueDocument(
	ctx context.Context,
	db *gorm.DB,
	store objectstore.Store,
	document loadedDocument,
	cfg config.Config,
	datasetVersion string,
	actorID uuid.UUID,
	repetition int,
	variant knowledgeingestion.ThroughputVariant,
) (queuedDocument, error) {
	createdAt := time.Now().UTC()
	documentID, versionID, taskID := uuid.New(), uuid.New(), uuid.New()
	outboxID, correlationID := uuid.New(), uuid.New()
	objectKey, err := objectstore.NewObjectKey(objectstore.BucketKnowledgeSources, versionID, createdAt)
	if err != nil {
		return queuedDocument{}, err
	}
	ref, err := store.Put(ctx, objectstore.PutInput{
		Bucket: objectstore.BucketKnowledgeSources, ObjectKey: objectKey,
		Content: bytes.NewReader(document.content), SizeBytes: document.definition.SizeBytes,
		MediaType: document.definition.MediaType, OriginalName: document.definition.FileName,
	})
	if err != nil {
		return queuedDocument{}, err
	}
	idempotencyKey := uuid.NewString()
	requestFingerprint := knowledge.SHA256Hex(fmt.Sprintf(
		"%s/%s/%d/%s", datasetVersion, document.definition.DocumentID, repetition, variant,
	))
	queueInput := knowledge.QueueVersionInput{
		VersionID: versionID, TaskID: taskID, OutboxEventID: outboxID, CorrelationID: correlationID,
		DocumentID: documentID, CreatedBy: actorID, Source: ref,
		PipelineVersion: cfg.Knowledge.PipelineVersion, MaxAttempts: cfg.Knowledge.MaxAttempts,
		IdempotencyKey: idempotencyKey, RequestFingerprint: requestFingerprint,
		NewDocument: &knowledge.CreateDocumentInput{
			ID: documentID, Scope: knowledge.ScopeGlobal,
			Title: fmt.Sprintf("[throughput:%s] %s", variant, document.definition.Title), CreatedBy: actorID,
		},
		CreatedAt: createdAt,
	}
	var queued knowledge.QueueVersionResult
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var queueErr error
		queued, queueErr = platformpostgres.NewKnowledgeRepository(tx).QueueVersion(ctx, queueInput)
		if queueErr != nil {
			return queueErr
		}
		result := tx.Exec(
			"DELETE FROM outbox_events WHERE id = ? AND aggregate_id = ? AND event_type = 'knowledge.ingest'",
			outboxID, taskID,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("throughput evaluation outbox event was not isolated")
		}
		return nil
	})
	if err != nil {
		_ = store.Remove(context.WithoutCancel(ctx), ref)
		return queuedDocument{}, fmt.Errorf("queue isolated throughput document: %w", err)
	}
	message, err := buildWorkerMessage(outboxID, correlationID, queued.Task.ID, queued.Version.ID, createdAt)
	if err != nil {
		return queuedDocument{}, err
	}
	return queuedDocument{
		definition: document.definition, source: ref, documentID: documentID,
		versionID: versionID, taskID: taskID, message: message,
	}, nil
}

func buildWorkerMessage(
	messageID, correlationID, taskID, versionID uuid.UUID,
	occurredAt time.Time,
) (knowledgeworker.IncomingMessage, error) {
	body, err := json.Marshal(map[string]any{
		"messageId": messageID.String(), "messageType": knowledgeworker.MessageType,
		"schemaVersion": knowledgeworker.SchemaVersion, "occurredAt": occurredAt.UTC().Format(time.RFC3339Nano),
		"correlationId": correlationID.String(), "causationId": nil,
		"payload": map[string]any{"taskId": taskID.String(), "documentVersionId": versionID.String()},
	})
	if err != nil {
		return knowledgeworker.IncomingMessage{}, err
	}
	return knowledgeworker.IncomingMessage{
		ContentType: "application/json", MessageID: messageID.String(), CorrelationID: correlationID.String(),
		Type: knowledgeworker.MessageType, Body: body,
	}, nil
}

func readIngestionFacts(ctx context.Context, db *gorm.DB, taskID uuid.UUID) (ingestionFacts, error) {
	var facts ingestionFacts
	err := db.WithContext(ctx).Raw(`
SELECT task.status AS task_status,
       version.parser_metadata::text AS parser_metadata,
       (SELECT COUNT(*) FROM knowledge_chunks chunk WHERE chunk.document_version_id = version.id) AS chunk_count,
       COALESCE(version.element_artifact_bucket, '') AS artifact_bucket,
       COALESCE(version.element_artifact_object_key, '') AS artifact_key,
       COALESCE(version.element_artifact_object_version, '') AS artifact_version,
       COALESCE(version.element_artifact_etag, '') AS artifact_etag,
       COALESCE(version.element_artifact_size_bytes, 0) AS artifact_size,
       COALESCE(version.element_artifact_sha256, '') AS artifact_sha256,
       CASE WHEN version.element_artifact_object_key IS NULL THEN '' ELSE 'application/json' END AS artifact_media_type,
       CASE WHEN version.element_artifact_object_key IS NULL THEN '' ELSE version.source_original_name || '.elements.json' END AS artifact_original_name
FROM knowledge_ingestion_tasks task
JOIN knowledge_document_versions version ON version.id = task.document_version_id
WHERE task.id = ?`, taskID).Scan(&facts).Error
	return facts, err
}

func buildObservation(
	datasetVersion, corpusHash, environmentHash string,
	repetition int,
	variant variantConfig,
	documents []queuedDocument,
	duration, queueDuration, processDuration time.Duration,
) knowledgeingestion.ThroughputObservation {
	observation := knowledgeingestion.ThroughputObservation{
		DatasetVersion: datasetVersion, RunID: "ingestion-" + uuid.NewString(), Repetition: repetition,
		Variant: variant.variant, CorpusFingerprint: corpusHash, EnvironmentFingerprint: environmentHash,
		Documents: len(documents), FormatCount: formatCount(documents),
		DurationMillis:      max(1, duration.Milliseconds()),
		QueueDurationMillis: queueDuration.Milliseconds(), ProcessDurationMillis: processDuration.Milliseconds(),
		DocumentConcurrency: variant.documentConcurrency, EmbeddingBatchSize: variant.embeddingBatchSize,
		EmbeddingMaxConcurrent: variant.embeddingMaxConcurrent, ChunkWriteBatchSize: variant.chunkWriteBatchSize,
	}
	for _, document := range documents {
		observation.SourceBytes += document.definition.SizeBytes
		observation.Pages += document.definition.PageCount
		metadata := map[string]any{}
		_ = json.Unmarshal([]byte(document.facts.ParserMetadata), &metadata)
		elements := metadataInt(metadata, "searchableElementCount")
		embeddingTokens := metadataInt(metadata, "embeddingTokens")
		observation.Elements += elements
		observation.Chunks += document.facts.ChunkCount
		observation.EmbeddingTokens += embeddingTokens
		observation.DocumentResults = append(observation.DocumentResults, knowledgeingestion.ThroughputDocumentObservation{
			DocumentID: document.definition.DocumentID, FormatClass: document.definition.FormatClass,
			TaskStatus: document.facts.TaskStatus, OutcomeAction: string(document.outcome.Action),
			OutcomeReason: document.outcome.Reason, Elements: elements,
			Chunks: document.facts.ChunkCount, EmbeddingTokens: embeddingTokens,
		})
		if document.facts.ChunkCount > 0 {
			observation.EmbeddingRequests += batches(document.facts.ChunkCount, variant.embeddingBatchSize)
			observation.ChunkInsertBatches += batches(document.facts.ChunkCount, variant.chunkWriteBatchSize)
			observation.EmbeddingInsertBatches += batches(document.facts.ChunkCount, variant.chunkWriteBatchSize)
		}
		switch knowledge.IngestionTaskStatus(document.facts.TaskStatus) {
		case knowledge.IngestionSucceeded:
			observation.SucceededDocuments++
		case knowledge.IngestionPartialSucceeded:
			observation.PartialDocuments++
			observation.PartialDocumentIDs = append(observation.PartialDocumentIDs, document.definition.DocumentID)
		default:
			observation.FailedDocuments++
			observation.FailedDocumentIDs = append(observation.FailedDocumentIDs, document.definition.DocumentID)
		}
	}
	slices.Sort(observation.PartialDocumentIDs)
	slices.Sort(observation.FailedDocumentIDs)
	slices.SortFunc(observation.DocumentResults, func(left, right knowledgeingestion.ThroughputDocumentObservation) int {
		return strings.Compare(left.DocumentID, right.DocumentID)
	})
	return observation
}

func cleanupRun(ctx context.Context, db *gorm.DB, store objectstore.Store, documents []queuedDocument) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cleanupErr error
	for _, document := range documents {
		if document.source.ObjectKey != "" {
			cleanupErr = errors.Join(cleanupErr, store.Remove(cleanupCtx, document.source))
		}
		if document.facts.ArtifactKey != "" {
			cleanupErr = errors.Join(cleanupErr, store.Remove(cleanupCtx, objectstore.ObjectRef{
				Bucket: objectstore.Bucket(document.facts.ArtifactBucket), ObjectKey: document.facts.ArtifactKey,
				VersionID: document.facts.ArtifactVersion, ETag: document.facts.ArtifactETag,
				SizeBytes: document.facts.ArtifactSize, SHA256: document.facts.ArtifactSHA256,
				MediaType: document.facts.ArtifactMediaType, OriginalName: document.facts.ArtifactOriginalName,
			}))
		}
	}
	for _, document := range documents {
		if document.taskID == uuid.Nil {
			continue
		}
		cleanupErr = errors.Join(cleanupErr, db.WithContext(cleanupCtx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("DELETE FROM outbox_events WHERE aggregate_id = ?", document.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_ingestion_events WHERE task_id = ?", document.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_chunks WHERE document_version_id = ?", document.versionID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_ingestion_tasks WHERE id = ?", document.taskID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM knowledge_document_versions WHERE id = ?", document.versionID).Error; err != nil {
				return err
			}
			return tx.Exec("DELETE FROM knowledge_documents WHERE id = ?", document.documentID).Error
		}))
	}
	return cleanupErr
}

func writeObservations(path string, observations []knowledgeingestion.ThroughputObservation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ingestion-observations-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = temp.Close()
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}

func buildEnvironmentFingerprint(cfg config.Config, profile knowledge.EmbeddingProfile) string {
	value := fmt.Sprintf(
		"%s|%s|%s|cpu=%d|postgres=%s:%d/%s|minio=%s|embedding=%s|chunk=%d/%d|pipeline=%s",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), cfg.Postgres.Host, cfg.Postgres.Port,
		cfg.Postgres.Database, cfg.MinIO.Endpoint, profile.Fingerprint, cfg.Knowledge.ChunkMaxRunes,
		cfg.Knowledge.ChunkOverlapRunes, cfg.Knowledge.PipelineVersion,
	)
	return knowledge.SHA256Hex(value)
}

func corpusFingerprint(datasetVersion string, documents []loadedDocument) string {
	parts := []string{datasetVersion}
	for _, document := range documents {
		parts = append(parts, document.definition.DocumentID, document.definition.FormatClass, document.definition.SHA256)
	}
	return knowledge.SHA256Hex(strings.Join(parts, "|"))
}

func metadataInt(metadata map[string]any, key string) int {
	value, _ := metadata[key].(float64)
	if value < 0 || value > float64(^uint(0)>>1) {
		return 0
	}
	return int(value)
}

func batches(items, batchSize int) int {
	if items <= 0 {
		return 0
	}
	return (items + batchSize - 1) / batchSize
}

func totalSourceBytes(documents []loadedDocument) int64 {
	var total int64
	for _, document := range documents {
		total += document.definition.SizeBytes
	}
	return total
}

func formatCount(documents []queuedDocument) int {
	formats := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		formats[document.definition.FormatClass] = struct{}{}
	}
	return len(formats)
}

func validFormatClass(value string) bool {
	switch value {
	case formatClassNativePDF, formatClassScannedPDF, formatClassDOCX, formatClassXLSX,
		formatClassPPTX, formatClassPNG, formatClassJPEG, formatClassText:
		return true
	default:
		return false
	}
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
