package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/conversationmemoryworker"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformrabbitmq "github.com/chitandabb/GoAgent/internal/platform/rabbitmq"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type MemoryWorkerApp struct {
	consumer *platformrabbitmq.MemoryConsumer
	deps     *runtimeDependencies
	workerID string
	log      *zap.Logger
}

func NewMemoryWorkerApp(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
) (*MemoryWorkerApp, error) {
	if log == nil {
		return nil, errors.New("memory worker logger is nil")
	}
	if !cfg.RabbitMQ.Enabled || !cfg.Agent.ContextMemory.AsyncCompactionEnabled {
		return nil, errors.New("memory worker requires RabbitMQ and async context compaction")
	}
	deps, err := openSelectedRuntimeDependencies(
		ctx, cfg, log, defaultDependencyOpeners(), dependencySelection{},
	)
	if err != nil {
		return nil, err
	}
	closeDependencies := func() { _ = deps.close() }
	memoryService, err := BuildConversationMemoryService(ctx, deps.db, cfg)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build conversation memory service: %w", err)
	}
	activationGate, err := buildConversationMemoryActivationGate(cfg)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	executor, err := conversationmemoryworker.NewServiceExecutor(memoryService, activationGate)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	retryDelay, err := conversationmemoryworker.NewExponentialRetryDelay(
		cfg.Agent.ContextMemory.RetryJitterRatio,
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	workerID := "memory-worker-" + uuid.NewString()
	worker, err := conversationmemoryworker.NewWorker(
		platformpostgres.NewConversationMemoryJobRepository(deps.db), executor,
		conversationmemoryworker.Config{
			WorkerID:      workerID,
			LeaseDuration: time.Duration(cfg.RabbitMQ.WorkerLeaseMillis) * time.Millisecond,
			RenewInterval: time.Duration(cfg.RabbitMQ.WorkerRenewIntervalMillis) * time.Millisecond,
			RetryDelay:    retryDelay,
		},
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	consumer, err := platformrabbitmq.OpenMemoryConsumer(
		cfg.RabbitMQ, worker, workerID, log.Named("rabbitmq_consumer"),
	)
	if err != nil {
		closeDependencies()
		return nil, err
	}
	return &MemoryWorkerApp{consumer: consumer, deps: deps, workerID: workerID, log: log}, nil
}

func (a *MemoryWorkerApp) Run(ctx context.Context) error {
	if a == nil || a.consumer == nil {
		return errors.New("memory worker app is unavailable")
	}
	a.log.Info("conversation memory worker started", zap.String("worker_id", a.workerID))
	err := a.consumer.Run(ctx)
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		a.log.Info("conversation memory worker stopped", zap.String("worker_id", a.workerID))
		return nil
	}
	return err
}

func (a *MemoryWorkerApp) Close() error {
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

func buildConversationMemoryActivationGate(cfg config.Config) (conversationmemory.ActivationGate, error) {
	tokenBudget, err := buildConversationTokenBudgetRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return mesagent.NewConversationMemoryActivationGate(mesagent.ConversationContextPreflightConfig{
		Enabled: true, SummaryTailEnabled: true, Planner: tokenBudget.Planner,
		ModelProfile:            tokenBudget.Profile,
		MemoryMaxRatio:          cfg.Agent.ContextMemory.MemoryMaxRatio,
		SummaryMaxRatio:         cfg.Agent.ContextMemory.SummaryMaxRatio,
		SummaryPromptMaxEntries: cfg.Agent.ContextMemory.Summary.EffectivePromptMaxEntries(),
		TailMaxRatio:            cfg.Agent.ContextMemory.TailMaxRatio,
		SoftThresholdRatio:      cfg.Agent.ContextMemory.SoftThresholdRatio,
		HardThresholdRatio:      cfg.Agent.ContextMemory.HardThresholdRatio,
	})
}
