package bootstrap

import (
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func wireSemanticAnswerCache(
	service *conversation.Service,
	db *gorm.DB,
	cfg config.SemanticAnswerCacheConfig,
	log *zap.Logger,
) error {
	if !cfg.Enabled {
		return nil
	}
	observer := resilience.ObserverFunc(func(event resilience.DegradationEvent) {
		log.Warn("semantic answer cache degraded",
			zap.String("operation", event.Operation), zap.String("policy", string(event.Policy)),
			zap.String("fallback", event.Fallback), zap.String("reason_code", event.ReasonCode),
			zap.String("run_id", event.RunID), zap.Int64("duration_millis", event.DurationMillis),
		)
	})
	repository, err := platformpostgres.NewSemanticAnswerCacheRepositoryWithConfig(
		db, platformpostgres.SemanticAnswerCacheRepositoryConfig{
			MaxRecords: cfg.MaxRecords, TTLJitterRatio: cfg.TTLJitterRatio,
		},
	)
	if err != nil {
		return fmt.Errorf("build semantic answer cache repository: %w", err)
	}
	_, err = service.WithSemanticAnswerCache(
		repository,
		conversation.SemanticAnswerCacheConfig{
			TTL:            time.Duration(cfg.TTLSeconds) * time.Second,
			LookupTimeout:  time.Duration(cfg.LookupTimeoutMillis) * time.Millisecond,
			WriteTimeout:   time.Duration(cfg.WriteTimeoutMillis) * time.Millisecond,
			MaxAnswerBytes: cfg.MaxAnswerBytes,
			MaxCitations:   cfg.MaxCitations,
		},
		observer,
	)
	if err != nil {
		return fmt.Errorf("wire semantic answer cache: %w", err)
	}
	return nil
}
