package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"GopherAI/internal/bootstrap"
	"GopherAI/internal/platform/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load MESGuard config: %v", err)
	}
	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build MESGuard app: %v", err)
	}
	if err := app.Run(ctx); err != nil {
		log.Fatalf("run MESGuard app: %v", err)
	}
}
