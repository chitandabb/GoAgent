package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"
	platformvisualmodel "github.com/chitandabb/GoAgent/internal/platform/visualmodel"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type KnowledgeWorkerApp struct {
	consumer *platformrabbitmq.KnowledgeConsumer
	deps     *runtimeDependencies
	layout   *knowledgeLayoutRuntime
	workerID string
	log      *zap.Logger
}

func NewKnowledgeWorkerApp(ctx context.Context, cfg config.Config, log *zap.Logger) (*KnowledgeWorkerApp, error) {
	if log == nil {
		return nil, errors.New("knowledge worker logger is nil")
	}
	if !cfg.RabbitMQ.Enabled || !cfg.MinIO.Enabled {
		return nil, errors.New("knowledge worker requires RabbitMQ and MinIO")
	}
	deps, err := openSelectedRuntimeDependencies(
		ctx,
		cfg,
		log,
		defaultDependencyOpeners(),
		dependencySelection{MinIO: true},
	)
	if err != nil {
		return nil, err
	}
	var layoutRuntime *knowledgeLayoutRuntime
	closeDependencies := func() {
		if layoutRuntime != nil {
			_ = layoutRuntime.Close()
		}
		_ = deps.close()
	}
	if deps.objectStore == nil {
		closeDependencies()
		return nil, fmt.Errorf("knowledge worker object store unavailable: %w", deps.objectStoreError)
	}

	parserLimits := knowledgeparser.Limits{
		MaxDocumentUnits:      cfg.Knowledge.ParserMaxDocumentUnits,
		MaxArchiveEntries:     cfg.Knowledge.ParserMaxArchiveEntries,
		MaxExpandedBytes:      cfg.Knowledge.ParserMaxExpandedBytes,
		MaxXMLBytes:           cfg.Knowledge.ParserMaxXMLBytes,
		MaxExtractedRunes:     cfg.Knowledge.ParserMaxExtractedRunes,
		MaxSpreadsheetRows:    cfg.Knowledge.ParserMaxSpreadsheetRows,
		MaxSpreadsheetColumns: cfg.Knowledge.ParserMaxSpreadsheetColumns,
		MaxVisualAssets:       cfg.Knowledge.ParserMaxVisualAssets,
		MaxVisualAssetBytes:   cfg.Knowledge.ParserMaxVisualAssetBytes,
		MaxTotalVisualBytes:   cfg.Knowledge.ParserMaxTotalVisualBytes,
	}
	pdfParser, err := knowledgeparser.NewPDFParser(parserLimits)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	ooxmlParser, err := knowledgeparser.NewOOXMLParser(parserLimits)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	imageParser, err := knowledgeparser.NewImageParser(parserLimits)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{}, pdfParser, ooxmlParser, imageParser)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	visualProcessor, err := buildVisualProcessor(ctx, cfg)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	layoutRuntime, err = openKnowledgeLayoutRuntime(ctx, cfg.Knowledge)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	var layoutStage *knowledgeingestion.LayoutStage
	if layoutRuntime != nil {
		layoutStage = layoutRuntime.stage
	}
	repository, err := platformpostgres.NewKnowledgeWorkerRepositoryWithBatchSize(
		deps.db, cfg.Knowledge.ChunkWriteBatchSize,
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	var embeddingConfig *knowledgeingestion.EmbeddingConfig
	if cfg.Models.Embedding.Enabled {
		embedder, err := platformembedding.NewClient(cfg.Models.Embedding, nil)
		if err != nil {
			closeDependencies()
			return nil, err
		}
		profile, err := cfg.Models.Embedding.Profile()
		if err != nil {
			closeDependencies()
			return nil, err
		}
		if err := repository.EnsureEmbeddingProfile(ctx, profile); err != nil {
			closeDependencies()
			return nil, err
		}
		embeddingConfig = &knowledgeingestion.EmbeddingConfig{
			Profile: profile, Embedder: embedder, BatchSize: cfg.Models.Embedding.BatchSize,
			MaxConcurrent: cfg.Models.Embedding.MaxConcurrent,
		}
	}
	executor, err := knowledgeingestion.NewExecutor(deps.objectStore, parser, knowledgeingestion.Config{
		MaxSourceBytes: cfg.Knowledge.MaxUploadBytes, MaxArtifactBytes: cfg.MinIO.MaxObjectBytes,
		ChunkOptions: knowledge.TextChunkOptions{
			MaxRunes: cfg.Knowledge.ChunkMaxRunes, OverlapRunes: cfg.Knowledge.ChunkOverlapRunes,
		},
		VisualConfig: knowledgeenrichment.Config{
			MaxEnrichments: cfg.Knowledge.MaxVisualEnrichments,
			MinPixels:      cfg.Knowledge.MinVisualPixels,
		},
		VisualProcessor: visualProcessor,
		LayoutStage:     layoutStage,
		Embedding:       embeddingConfig,
	})
	if err != nil {
		closeDependencies()
		return nil, err
	}
	workerID := "knowledge-worker-" + uuid.NewString()
	worker, err := knowledgeworker.NewWorker(
		repository, executor,
		knowledgeworker.Config{
			WorkerID:      workerID,
			LeaseDuration: time.Duration(cfg.RabbitMQ.WorkerLeaseMillis) * time.Millisecond,
			RenewInterval: time.Duration(cfg.RabbitMQ.WorkerRenewIntervalMillis) * time.Millisecond,
		},
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	consumer, err := platformrabbitmq.OpenKnowledgeConsumer(
		cfg.RabbitMQ, worker, workerID, log.Named("rabbitmq_consumer"),
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	return &KnowledgeWorkerApp{
		consumer: consumer, deps: deps, layout: layoutRuntime, workerID: workerID, log: log,
	}, nil
}

func buildVisualProcessor(ctx context.Context, cfg config.Config) (knowledgeenrichment.Processor, error) {
	var ocrEndpoint, visionEndpoint *platformvisualmodel.Endpoint
	if cfg.Models.OCR.Enabled {
		prompt, err := cfg.Models.OCR.LoadPrompt("models.ocr")
		if err != nil {
			return nil, err
		}
		generator, err := platformvisualmodel.NewDashScopeModel(ctx, cfg.Models.OCR, "models.ocr")
		if err != nil {
			return nil, err
		}
		ocrEndpoint = &platformvisualmodel.Endpoint{
			Generator: generator, Provider: cfg.Models.OCR.Provider, Model: cfg.Models.OCR.Model,
			Prompt: prompt, PromptVersion: cfg.Models.OCR.PromptVersion,
		}
	}
	if cfg.Models.Vision.Enabled {
		prompt, err := cfg.Models.Vision.LoadPrompt("models.vision")
		if err != nil {
			return nil, err
		}
		generator, err := platformvisualmodel.NewDashScopeModel(ctx, cfg.Models.Vision, "models.vision")
		if err != nil {
			return nil, err
		}
		visionEndpoint = &platformvisualmodel.Endpoint{
			Generator: generator, Provider: cfg.Models.Vision.Provider, Model: cfg.Models.Vision.Model,
			Prompt: prompt, PromptVersion: cfg.Models.Vision.PromptVersion,
		}
	}
	if ocrEndpoint == nil && visionEndpoint == nil {
		return nil, nil
	}
	return platformvisualmodel.NewProcessor(ocrEndpoint, visionEndpoint)
}

func (a *KnowledgeWorkerApp) Run(ctx context.Context) error {
	if a == nil || a.consumer == nil {
		return errors.New("knowledge worker app is unavailable")
	}
	a.log.Info("knowledge worker started", zap.String("worker_id", a.workerID))
	err := a.consumer.Run(ctx)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		a.log.Info("knowledge worker stopped", zap.String("worker_id", a.workerID))
		return nil
	}
	return err
}

func (a *KnowledgeWorkerApp) Close() error {
	if a == nil {
		return nil
	}
	var consumerErr, layoutErr, dependencyErr error
	if a.consumer != nil {
		consumerErr = a.consumer.Close()
	}
	if a.layout != nil {
		layoutErr = a.layout.Close()
	}
	if a.deps != nil {
		dependencyErr = a.deps.close()
	}
	return errors.Join(consumerErr, layoutErr, dependencyErr)
}
