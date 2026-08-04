package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"

	"go.uber.org/zap"
)

func main() {
	bootstrapLogger := platformlogger.NewBootstrapFor("mesguard-knowledge-worker")
	defer platformlogger.Sync(bootstrapLogger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Error("load knowledge worker config failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}
	log, closeLogger, err := platformlogger.NewFor(cfg.Log, "mesguard-knowledge-worker")
	if err != nil {
		bootstrapLogger.Error("build knowledge worker logger failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}
	defer func() { _ = closeLogger() }()

	app, err := bootstrap.NewKnowledgeWorkerApp(ctx, cfg, log)
	if err != nil {
		log.Error("build knowledge worker failed", zap.Error(err))
		_ = closeLogger()
		os.Exit(1)
	}
	defer func() {
		if err := app.Close(); err != nil {
			log.Error("close knowledge worker failed", zap.Error(err))
		}
	}()
	if err := app.Run(ctx); err != nil {
		log.Error("run knowledge worker failed", zap.Error(err))
		_ = app.Close()
		_ = closeLogger()
		os.Exit(1)
	}
}
