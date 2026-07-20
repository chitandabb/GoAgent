package bootstrap

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"time"

	postgresdiagnosis "github.com/chitandabb/GoAgent/internal/adapter/postgres/diagnosis"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/migrate"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	httptransport "github.com/chitandabb/GoAgent/internal/transport/http"

	rediscli "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type App struct {
	server       *stdhttp.Server
	db           *gorm.DB
	dbClose      func() error
	redis        *rediscli.Client
	shutdownWait time.Duration
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return nil, err
	}
	if err := migrate.Apply(ctx, db); err != nil {
		_ = closeDB()
		return nil, err
	}
	redis, err := platformredis.Open(ctx, cfg.Redis)
	if err != nil {
		_ = closeDB()
		return nil, err
	}

	runStore := postgresdiagnosis.NewRunStore(db)
	diagnosisService := diagnosis.NewService(runStore)
	app := &App{
		db:           db,
		dbClose:      closeDB,
		redis:        redis,
		shutdownWait: 10 * time.Second,
	}
	app.server = &stdhttp.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           httptransport.NewRouter(diagnosisService, app.health),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return app, nil
}

func (a *App) Run(ctx context.Context) error {
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
		return a.Close()
	}
}

func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownWait)
	defer cancel()
	shutdownErr := a.server.Shutdown(ctx)
	redisErr := a.redis.Close()
	dbErr := a.dbClose()
	return errors.Join(shutdownErr, redisErr, dbErr)
}

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
