package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/objectstore"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/migration"
	platformminio "github.com/chitandabb/GoAgent/internal/platform/minio"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"

	rediscli "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type runtimeDependencies struct {
	db               *gorm.DB
	dbClose          func() error
	redis            *rediscli.Client
	objectStore      objectstore.Store
	objectStoreError error
	sqlServer        *sql.DB
	sqlServerError   error
}

type dependencyOpeners struct {
	postgres       func(context.Context, config.PostgresConfig, *zap.Logger) (*gorm.DB, func() error, error)
	unwrapPostgres func(*gorm.DB) (*sql.DB, error)
	checkMigration func(context.Context, *sql.DB) error
	redis          func(context.Context, config.RedisConfig) (*rediscli.Client, error)
	minio          func(context.Context, config.MinIOConfig) (objectstore.Store, error)
	sqlServer      func(context.Context, config.SQLServerConfig) (*sql.DB, error)
	pingSQLServer  func(context.Context, *sql.DB) error
}

type dependencySelection struct {
	Redis     bool
	MinIO     bool
	SQLServer bool
}

var allRuntimeDependencies = dependencySelection{
	Redis: true, MinIO: true, SQLServer: true,
}

func defaultDependencyOpeners() dependencyOpeners {
	return dependencyOpeners{
		postgres: platformpostgres.Open, checkMigration: migration.CheckCurrent,
		redis: platformredis.Open, sqlServer: platformsqlserver.Open,
		minio: func(ctx context.Context, cfg config.MinIOConfig) (objectstore.Store, error) {
			return platformminio.Open(ctx, cfg)
		},
		unwrapPostgres: func(db *gorm.DB) (*sql.DB, error) { return db.DB() },
		pingSQLServer:  func(ctx context.Context, db *sql.DB) error { return db.PingContext(ctx) },
	}
}

func openRuntimeDependencies(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
	openers dependencyOpeners,
) (*runtimeDependencies, error) {
	return openSelectedRuntimeDependencies(ctx, cfg, log, openers, allRuntimeDependencies)
}

func openSelectedRuntimeDependencies(
	ctx context.Context,
	cfg config.Config,
	log *zap.Logger,
	openers dependencyOpeners,
	selection dependencySelection,
) (*runtimeDependencies, error) {
	db, closeDB, err := openers.postgres(ctx, cfg.Postgres, log.Named("postgres"))
	if err != nil {
		return nil, err
	}
	deps := &runtimeDependencies{db: db, dbClose: closeDB}
	sqlDB, err := openers.unwrapPostgres(db)
	if err != nil {
		_ = deps.close()
		return nil, fmt.Errorf("get postgres sql db: %w", err)
	}
	if err := openers.checkMigration(ctx, sqlDB); err != nil {
		_ = deps.close()
		return nil, fmt.Errorf("check database migration version: %w", err)
	}

	if selection.Redis {
		deps.redis, err = openers.redis(ctx, cfg.Redis)
		if err != nil {
			log.Warn("Redis unavailable; continuing in degraded mode", zap.Error(err))
			deps.redis = nil
		}
	}
	if selection.MinIO && cfg.MinIO.Enabled {
		deps.objectStore, err = openers.minio(ctx, cfg.MinIO)
		if err != nil {
			deps.objectStoreError = err
			if deps.objectStore == nil {
				log.Warn("MinIO unavailable; attachment and knowledge uploads are degraded", zap.Error(err))
			} else {
				log.Warn("MinIO initialization failed; uploads will retry on demand", zap.Error(err))
			}
		}
	}
	if selection.SQLServer && cfg.SQLServer.Enabled {
		deps.sqlServer, err = openers.sqlServer(ctx, cfg.SQLServer)
		if err != nil {
			deps.sqlServerError = err
			deps.sqlServer = nil
			log.Warn("ERP SQL Server unavailable; ticket APIs will return 503", zap.Error(err))
		} else {
			pingCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.SQLServer.QueryTimeoutMillis)*time.Millisecond)
			pingErr := openers.pingSQLServer(pingCtx, deps.sqlServer)
			cancel()
			if pingErr != nil {
				deps.sqlServerError = pingErr
				log.Warn("ERP SQL Server ping failed; connection pool will retry on requests", zap.Error(pingErr))
			}
		}
	}
	return deps, nil
}

func (d *runtimeDependencies) close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.sqlServer != nil {
		errs = append(errs, d.sqlServer.Close())
	}
	if d.objectStore != nil {
		errs = append(errs, d.objectStore.Close())
	}
	if d.redis != nil {
		errs = append(errs, d.redis.Close())
	}
	if d.dbClose != nil {
		errs = append(errs, d.dbClose())
	}
	return errors.Join(errs...)
}
