package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/chitandabb/GoAgent/internal/bootstrap"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func main() {
	// 统一监听 Ctrl+C 和容器终止信号，传递给 App 做优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 配置只加载一次，再由 bootstrap 显式传递给需要的组件。
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load MESGuard config: %v", err)
	}
	// bootstrap.New 相当于手写的依赖注入容器。
	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		log.Fatalf("build MESGuard app: %v", err)
	}
	if err := app.Run(ctx); err != nil {
		log.Fatalf("run MESGuard app: %v", err)
	}
}
