package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdhttp "net/http"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	httptransport "github.com/chitandabb/GoAgent/internal/transport/http"

	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type App struct {
	server       *stdhttp.Server
	db           *gorm.DB
	dbClose      func() error
	redis        *rediscli.Client
	sqlServer    *sql.DB
	logger       *zap.Logger
	shutdownWait time.Duration
}

// New 是项目的手动依赖装配入口，作用类似 Spring Boot 的 Bean 配置类。
// 这里负责创建基础设施客户端、Router 和 HTTP Server。
func New(ctx context.Context, cfg config.Config, log *zap.Logger) (*App, error) {
	deps, err := openRuntimeDependencies(ctx, cfg, log, defaultDependencyOpeners())
	if err != nil {
		return nil, err
	}
	closeDependencies := func() { _ = deps.close() }

	userRepository := platformpostgres.NewUserRepository(deps.db)
	sessionRepository := platformpostgres.NewSessionRepository(deps.db)
	passwordHasher, err := auth.NewArgon2idHasher(auth.DefaultArgon2idParams())
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build password hasher: %w", err)
	}
	sessionPolicy, err := auth.NewSessionPolicy(
		time.Duration(cfg.Auth.SessionIdleMinutes)*time.Minute,
		time.Duration(cfg.Auth.SessionAbsoluteMinutes)*time.Minute,
	)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build session policy: %w", err)
	}
	loginService, err := auth.NewLoginService(
		userRepository,
		sessionRepository,
		passwordHasher,
		auth.NewTokenGenerator(),
		sessionPolicy,
	)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build login service: %w", err)
	}
	sessionService, err := auth.NewSessionService(userRepository, sessionRepository, sessionPolicy)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build session service: %w", err)
	}
	authRoutes, err := httptransport.NewAuthRoutes(
		loginService,
		sessionService,
		httptransport.CookieSettings{
			Domain: cfg.Auth.CookieDomain,
			Secure: cfg.Auth.CookieSecure,
		},
		cfg.Auth.AllowedOrigins,
	)
	if err != nil {
		closeDependencies()
		return nil, fmt.Errorf("build auth routes: %w", err)
	}

	registrars := []httptransport.RouteRegistrar{authRoutes}
	if cfg.SQLServer.Enabled {
		dataSourceID, parseErr := uuid.Parse(cfg.SQLServer.ID)
		if parseErr != nil {
			closeDependencies()
			return nil, fmt.Errorf("parse SQL Server data source id: %w", parseErr)
		}
		externalCaseRepository := platformpostgres.NewExternalCaseRepository(deps.db)
		if err := externalCaseRepository.EnsureCaseSource(
			ctx, dataSourceID, cfg.SQLServer.Code, cfg.SQLServer.Name, cfg.SQLServer.Environment,
		); err != nil {
			closeDependencies()
			return nil, fmt.Errorf("sync ERP case source: %w", err)
		}
		var reader externalcase.Reader
		if deps.sqlServer == nil {
			reader = externalcase.NewUnavailableReader(deps.sqlServerError)
		} else {
			reader, err = platformsqlserver.NewExternalCaseReader(deps.sqlServer, cfg.SQLServer, log.Named("sqlserver"))
			if err != nil {
				closeDependencies()
				return nil, fmt.Errorf("build ERP external case reader: %w", err)
			}
		}
		externalCaseService, err := externalcase.NewService(externalcase.DataSource{
			ID: dataSourceID, Code: cfg.SQLServer.Code, Name: cfg.SQLServer.Name,
			Type: "sqlserver", Role: "case_source", Environment: cfg.SQLServer.Environment,
			SafetyMode: "read_only", Status: "active",
		}, reader, externalCaseRepository)
		if err != nil {
			closeDependencies()
			return nil, fmt.Errorf("build external case service: %w", err)
		}
		externalCaseRoutes, err := httptransport.NewExternalCaseRoutes(
			externalCaseService, authRoutes.RequireAuthentication(),
		)
		if err != nil {
			closeDependencies()
			return nil, fmt.Errorf("build external case routes: %w", err)
		}
		registrars = append(registrars, externalCaseRoutes)
	}

	app := &App{
		db:           deps.db,
		dbClose:      deps.dbClose,
		redis:        deps.redis,
		sqlServer:    deps.sqlServer,
		logger:       log,
		shutdownWait: 10 * time.Second,
	}
	app.server = &stdhttp.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           httptransport.NewRouter(log.Named("http"), app.health, registrars...),
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
	var redisErr, sqlServerErr error
	if a.redis != nil {
		redisErr = a.redis.Close()
	}
	if a.sqlServer != nil {
		sqlServerErr = a.sqlServer.Close()
	}
	dbErr := a.dbClose()
	err := errors.Join(shutdownErr, redisErr, sqlServerErr, dbErr)
	if err != nil {
		a.logger.Error("application shutdown failed", zap.Error(err))
		return err
	}
	a.logger.Info("application stopped")
	return nil
}

// health 只检查关键事实库。Redis 和外部业务依赖故障不能触发进程重启风暴。
func (a *App) health(ctx context.Context) error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
