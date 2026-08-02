// Package logger 创建 MESGuard 统一使用的结构化日志器。
//
// 该包只负责日志基础设施，不提供全局变量。Logger 由 main 创建后通过构造函数
// 显式传递，作用类似 Spring 容器中由配置类创建并注入的 Logger Bean。
package logger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type contextKey struct{}

// NewBootstrap 创建不依赖 TOML 的启动日志器。
// 它只负责记录“配置无法加载”或“正式 Logger 无法创建”这类最早期错误。
func NewBootstrap() *zap.Logger {
	return NewBootstrapFor("mesguard-api")
}

// NewBootstrapFor 为独立运行角色创建带正确 service 字段的启动日志器。
func NewBootstrapFor(service string) *zap.Logger {
	encoder, _ := newEncoder("console")
	return zap.New(
		zapcore.NewCore(encoder, zapcore.Lock(os.Stderr), zap.DebugLevel),
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
		zap.Fields(
			zap.String("service", service),
			zap.String("phase", "bootstrap"),
		),
	)
}

// New 根据类型化配置创建 Logger。
// 控制台是容器和本地开发的主要输出；文件输出开启后使用 Lumberjack 自动轮转。
func New(cfg config.LogConfig) (*zap.Logger, func() error, error) {
	return NewFor(cfg, "mesguard-api")
}

// NewFor 为 API、Relay、Worker 等独立运行角色创建带正确 service 字段的正式 Logger。
func NewFor(cfg config.LogConfig, service string) (*zap.Logger, func() error, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, nil, errors.New("logger service is required")
	}
	level, err := zapcore.ParseLevel(strings.ToLower(strings.TrimSpace(cfg.Level)))
	if err != nil {
		return nil, nil, fmt.Errorf("parse log level %q: %w", cfg.Level, err)
	}

	consoleEncoder, err := newEncoder(cfg.Format)
	if err != nil {
		return nil, nil, err
	}
	cores := []zapcore.Core{
		zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), level),
	}
	var closers []io.Closer

	if cfg.EnableFile {
		fileCore, closer, err := newFileCore(cfg, level)
		if err != nil {
			return nil, nil, err
		}
		cores = append(cores, fileCore)
		closers = append(closers, closer)
	}

	log := zap.New(
		zapcore.NewTee(cores...),
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
		zap.ErrorOutput(zapcore.Lock(os.Stderr)),
		zap.Fields(
			zap.String("service", service),
			zap.String("environment", cfg.Environment),
		),
	)
	closeLog := func() error {
		Sync(log)
		var closeErrs []error
		for _, closer := range closers {
			closeErrs = append(closeErrs, closer.Close())
		}
		return errors.Join(closeErrs...)
	}
	return log, closeLog, nil
}

// IntoContext 把已经附加 requestId 等字段的 Logger 放入标准 context。
// 后续 Service 只接收 context.Context，也能取得当前请求对应的 Logger。
func IntoContext(ctx context.Context, log *zap.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, log)
}

// FromContext 返回当前请求的 Logger；请求上下文没有 Logger 时返回 fallback。
func FromContext(ctx context.Context, fallback *zap.Logger) *zap.Logger {
	if ctx != nil {
		if log, ok := ctx.Value(contextKey{}).(*zap.Logger); ok && log != nil {
			return log
		}
	}
	if fallback != nil {
		return fallback
	}
	return zap.NewNop()
}

// Sync 尽力刷新 Logger。部分终端不支持 fsync，因此关闭阶段不把该错误视为服务失败。
func Sync(log *zap.Logger) {
	if log != nil {
		_ = log.Sync()
	}
}

func newEncoder(format string) (zapcore.Encoder, error) {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeDuration = zapcore.StringDurationEncoder

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return zapcore.NewJSONEncoder(encoderConfig), nil
	case "console":
		encoderConfig = zap.NewDevelopmentEncoderConfig()
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		encoderConfig.EncodeDuration = zapcore.StringDurationEncoder
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		return zapcore.NewConsoleEncoder(encoderConfig), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q: use console or json", format)
	}
}

func newFileCore(cfg config.LogConfig, level zapcore.Level) (zapcore.Core, io.Closer, error) {
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log directory %q: %w", cfg.OutputDir, err)
	}

	writer := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.OutputDir, "mesguard.log"),
		MaxSize:    cfg.MaxSize,
		MaxAge:     cfg.MaxAge,
		MaxBackups: cfg.MaxBackups,
		LocalTime:  true,
		Compress:   cfg.Compress,
	}
	encoder, _ := newEncoder("json")
	return zapcore.NewCore(encoder, zapcore.AddSync(writer), level), writer, nil
}
