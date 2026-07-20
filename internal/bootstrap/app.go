package bootstrap

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	httptransport "github.com/chitandabb/GoAgent/internal/transport/http"

	rediscli "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type App struct {
	server       *stdhttp.Server
	db           *gorm.DB
	dbClose      func() error
	redis        *rediscli.Client
	logger       *zap.Logger
	shutdownWait time.Duration
}

// New 是项目的手动依赖装配入口，作用类似 Spring Boot 的 Bean 配置类。
// 这里负责创建基础设施客户端、Router 和 HTTP Server。
func New(ctx context.Context, cfg config.Config, log *zap.Logger) (*App, error) {
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, log.Named("postgres"))
	if err != nil {
		return nil, err
	}
	redis, err := platformredis.Open(ctx, cfg.Redis)
	if err != nil {
		_ = closeDB()
		return nil, err
	}

	app := &App{
		db:           db,
		dbClose:      closeDB,
		redis:        redis,
		logger:       log,
		shutdownWait: 10 * time.Second,
	}
	app.server = &stdhttp.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           httptransport.NewRouter(log.Named("http"), app.health),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return app, nil
}

// Run 启动 HTTP Server，并在进程收到退出信号后执行优雅关闭。
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("HTTP server started", zap.String("address", a.server.Addr))
	errs := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		_ = a.Close()
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
		a.logger.Info("shutdown signal received")
		return a.Close()
	}
}

// Close 按顺序关闭 HTTP、Redis 和 PostgreSQL 连接。
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownWait)
	defer cancel()
	shutdownErr := a.server.Shutdown(ctx)
	redisErr := a.redis.Close()
	dbErr := a.dbClose()
	err := errors.Join(shutdownErr, redisErr, dbErr)
	if err != nil {
		a.logger.Error("application shutdown failed", zap.Error(err))
		return err
	}
	a.logger.Info("application stopped")
	return nil
}

// health 检查当前 Web 壳依赖的 PostgreSQL 和 Redis 是否可用。
func (a *App) health(ctx context.Context) error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return err
	}
	return a.redis.Ping(ctx).Err()
}
