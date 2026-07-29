package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	rediscli "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestOpenRuntimeDependenciesFailsForCriticalPostgres(t *testing.T) {
	want := errors.New("postgres unavailable")
	openers := stubOpeners()
	openers.postgres = func(context.Context, config.PostgresConfig, *zap.Logger) (*gorm.DB, func() error, error) {
		return nil, nil, want
	}

	_, err := openRuntimeDependencies(context.Background(), config.Config{}, zap.NewNop(), openers)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want postgres error", err)
	}
}

func TestOpenRuntimeDependenciesClosesPostgresWhenMigrationCheckFails(t *testing.T) {
	closed := false
	want := errors.New("schema behind")
	openers := stubOpeners()
	openers.postgres = func(context.Context, config.PostgresConfig, *zap.Logger) (*gorm.DB, func() error, error) {
		return &gorm.DB{}, func() error { closed = true; return nil }, nil
	}
	openers.checkMigration = func(context.Context, *sql.DB) error { return want }

	_, err := openRuntimeDependencies(context.Background(), config.Config{}, zap.NewNop(), openers)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want migration error", err)
	}
	if !closed {
		t.Fatal("postgres close was not called")
	}
}

func TestOpenRuntimeDependenciesKeepsOptionalFailuresDegraded(t *testing.T) {
	redisErr := errors.New("redis unavailable")
	sqlServerErr := errors.New("sqlserver unavailable")
	openers := stubOpeners()
	openers.redis = func(context.Context, config.RedisConfig) (*rediscli.Client, error) {
		return nil, redisErr
	}
	openers.sqlServer = func(context.Context, config.SQLServerConfig) (*sql.DB, error) {
		return nil, sqlServerErr
	}
	cfg := config.Config{SQLServer: config.SQLServerConfig{Enabled: true}}

	deps, err := openRuntimeDependencies(context.Background(), cfg, zap.NewNop(), openers)
	if err != nil {
		t.Fatalf("open dependencies: %v", err)
	}
	if deps.redis != nil || deps.sqlServer != nil {
		t.Fatal("optional failed dependencies must remain nil")
	}
	if !errors.Is(deps.sqlServerError, sqlServerErr) {
		t.Fatalf("sqlServerError = %v", deps.sqlServerError)
	}
}

func TestOpenRuntimeDependenciesKeepsSQLPoolAfterPingFailure(t *testing.T) {
	pingErr := errors.New("sqlserver starting")
	pool := &sql.DB{}
	openers := stubOpeners()
	openers.sqlServer = func(context.Context, config.SQLServerConfig) (*sql.DB, error) { return pool, nil }
	openers.pingSQLServer = func(context.Context, *sql.DB) error { return pingErr }
	cfg := config.Config{SQLServer: config.SQLServerConfig{Enabled: true, QueryTimeoutMillis: 10}}

	deps, err := openRuntimeDependencies(context.Background(), cfg, zap.NewNop(), openers)
	if err != nil {
		t.Fatalf("open dependencies: %v", err)
	}
	if deps.sqlServer != pool {
		t.Fatal("SQL Server pool was discarded after a transient ping failure")
	}
	if !errors.Is(deps.sqlServerError, pingErr) {
		t.Fatalf("sqlServerError = %v", deps.sqlServerError)
	}
}

func stubOpeners() dependencyOpeners {
	return dependencyOpeners{
		postgres: func(context.Context, config.PostgresConfig, *zap.Logger) (*gorm.DB, func() error, error) {
			return &gorm.DB{}, func() error { return nil }, nil
		},
		unwrapPostgres: func(*gorm.DB) (*sql.DB, error) { return &sql.DB{}, nil },
		checkMigration: func(context.Context, *sql.DB) error { return nil },
		redis:          func(context.Context, config.RedisConfig) (*rediscli.Client, error) { return nil, nil },
		sqlServer:      func(context.Context, config.SQLServerConfig) (*sql.DB, error) { return nil, nil },
		pingSQLServer:  func(context.Context, *sql.DB) error { return nil },
	}
}
