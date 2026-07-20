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
	// 启动 Logger 不依赖配置文件，确保配置加载失败也能输出统一的结构化日志。
	bootstrapLogger := platformlogger.NewBootstrap()
	defer platformlogger.Sync(bootstrapLogger)

	// 统一监听 Ctrl+C 和容器终止信号，传递给 App 做优雅关闭。
	ctx, stop := signal.NotifyContext(
		context.Background(),
		// 来自操作系统的中断，例如在终端按 Ctrl+C。
		os.Interrupt,
		// Docker、Kubernetes、systemd 等进程管理器通常使用 SIGTERM 请求服务退出。
		syscall.SIGTERM,
	)
	defer stop()

	// 配置只加载一次，再由 bootstrap 显式传递给需要的组件。
	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Error("load application config failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}

	// Logger 是第一个被创建的基础设施依赖，后续组件都复用同一个实例。
	appLogger, closeLogger, err := platformlogger.New(cfg.Log)
	if err != nil {
		bootstrapLogger.Error("build application logger failed", zap.Error(err))
		platformlogger.Sync(bootstrapLogger)
		os.Exit(1)
	}
	defer func() { _ = closeLogger() }()
	// 把仍使用标准库 log 的第三方组件也统一转发到 Zap。
	restoreStdLog := zap.RedirectStdLog(appLogger.Named("stdlib"))
	defer restoreStdLog()

	// bootstrap.New 相当于手写的依赖注入容器。
	app, err := bootstrap.New(ctx, cfg, appLogger)
	if err != nil {
		appLogger.Error("build application failed", zap.Error(err))
		_ = closeLogger()
		os.Exit(1)
	}
	if err := app.Run(ctx); err != nil {
		appLogger.Error("run application failed", zap.Error(err))
		_ = closeLogger()
		os.Exit(1)
	}
}
