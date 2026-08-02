package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chitandabb/GoAgent/internal/messaging"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/chitandabb/GoAgent/internal/platform/rabbitmq"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

func main() {
	bootstrapLogger := platformlogger.NewBootstrapFor("mesguard-outbox-relay")
	defer platformlogger.Sync(bootstrapLogger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Error("load outbox relay config failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}
	log, closeLogger, err := platformlogger.NewFor(cfg.Log, "mesguard-outbox-relay")
	if err != nil {
		bootstrapLogger.Error("build outbox relay logger failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}
	defer func() { _ = closeLogger() }()

	db, closePostgres, err := postgres.Open(ctx, cfg.Postgres, log)
	if err != nil {
		log.Error("open outbox relay postgres failed", zap.Error(err))
		_ = closeLogger()
		os.Exit(1)
	}
	defer func() { _ = closePostgres() }()
	publisher, err := rabbitmq.OpenPublisher(cfg.RabbitMQ)
	if err != nil {
		log.Error("open outbox relay rabbitmq publisher failed", zap.Error(err))
		_ = closePostgres()
		_ = closeLogger()
		os.Exit(1)
	}
	defer func() { _ = publisher.Close() }()

	owner := "relay-" + uuid.NewString()
	relay, err := messaging.NewOutboxRelay(
		postgres.NewOutboxEventRepository(db), publisher,
		messaging.RelayConfig{
			Owner: owner, BatchSize: cfg.RabbitMQ.RelayBatchSize,
			LeaseDuration:  time.Duration(cfg.RabbitMQ.RelayLeaseMillis) * time.Millisecond,
			PublishTimeout: time.Duration(cfg.RabbitMQ.PublishConfirmTimeoutMillis) * time.Millisecond,
		},
	)
	if err != nil {
		log.Error("build outbox relay failed", zap.Error(err))
		_ = publisher.Close()
		_ = closePostgres()
		_ = closeLogger()
		os.Exit(1)
	}
	log.Info("outbox relay started", zap.String("owner", owner))
	run(ctx, relay, time.Duration(cfg.RabbitMQ.RelayPollIntervalMillis)*time.Millisecond, log)
	log.Info("outbox relay stopped", zap.String("owner", owner))
}

func run(ctx context.Context, relay *messaging.OutboxRelay, pollInterval time.Duration, log *zap.Logger) {
	for {
		stats, err := relay.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("outbox relay iteration failed", zap.Error(err))
		} else if stats.Claimed > 0 {
			log.Info("outbox relay iteration completed",
				zap.Int("claimed", stats.Claimed), zap.Int("published", stats.Published),
				zap.Int("failed", stats.Failed), zap.Int("stale", stats.Stale),
			)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}
