package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeingestion"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgeworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type KnowledgeWorkerApp struct {
	consumer *platformrabbitmq.KnowledgeConsumer
	deps     *runtimeDependencies
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
	closeDependencies := func() { _ = deps.close() }
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
	parser, err := knowledgeparser.NewRouter(knowledgeparser.TextParser{}, pdfParser, ooxmlParser)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	executor, err := knowledgeingestion.NewExecutor(deps.objectStore, parser, knowledgeingestion.Config{
		MaxSourceBytes: cfg.Knowledge.MaxUploadBytes, MaxArtifactBytes: cfg.MinIO.MaxObjectBytes,
		ChunkOptions: knowledge.TextChunkOptions{
			MaxRunes: cfg.Knowledge.ChunkMaxRunes, OverlapRunes: cfg.Knowledge.ChunkOverlapRunes,
		},
	})
	if err != nil {
		closeDependencies()
		return nil, err
	}
	workerID := "knowledge-worker-" + uuid.NewString()
	worker, err := knowledgeworker.NewWorker(
		platformpostgres.NewKnowledgeWorkerRepository(deps.db), executor,
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
		consumer: consumer, deps: deps, workerID: workerID, log: log,
	}, nil
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
	var consumerErr, dependencyErr error
	if a.consumer != nil {
		consumerErr = a.consumer.Close()
	}
	if a.deps != nil {
		dependencyErr = a.deps.close()
	}
	return errors.Join(consumerErr, dependencyErr)
}
