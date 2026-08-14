package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformembedding "github.com/chitandabb/GoAgent/internal/platform/dashscopeembedding"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func wireSemanticAnswerCache(
	ctx context.Context,
	service *conversation.Service,
	db *gorm.DB,
	appConfig config.Config,
	log *zap.Logger,
) error {
	cfg := appConfig.SemanticAnswerCache
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
	conversationConfig := conversation.SemanticAnswerCacheConfig{
		TTL:            time.Duration(cfg.TTLSeconds) * time.Second,
		LookupTimeout:  time.Duration(cfg.LookupTimeoutMillis) * time.Millisecond,
		WriteTimeout:   time.Duration(cfg.WriteTimeoutMillis) * time.Millisecond,
		MaxAnswerBytes: cfg.MaxAnswerBytes,
		MaxCitations:   cfg.MaxCitations,
	}
	if cfg.SemanticEnabled {
		profile, profileErr := appConfig.Models.Embedding.Profile()
		if profileErr != nil {
			return fmt.Errorf("build semantic answer cache embedding profile: %w", profileErr)
		}
		if profile.Fingerprint != cfg.SemanticProfileFingerprint {
			return fmt.Errorf("semantic answer cache threshold profile does not match active embedding profile")
		}
		client, clientErr := platformembedding.NewClient(appConfig.Models.Embedding, nil)
		if clientErr != nil {
			return fmt.Errorf("build semantic answer cache embedder: %w", clientErr)
		}
		if profileErr = platformpostgres.NewKnowledgeWorkerRepository(db).EnsureEmbeddingProfile(
			ctx, profile,
		); profileErr != nil {
			return fmt.Errorf("activate semantic answer cache embedding profile: %w", profileErr)
		}
		conversationConfig.Semantic = &conversation.SemanticAnswerCacheSemanticConfig{
			Embedder: client, Profile: profile, MinimumSimilarity: cfg.SemanticMinimumSimilarity,
			CandidateLimit:   cfg.SemanticCandidateLimit,
			EmbeddingTimeout: time.Duration(cfg.SemanticEmbeddingTimeoutMillis) * time.Millisecond,
		}
	}
	_, err = service.WithSemanticAnswerCache(
		repository,
		conversationConfig,
		observer,
	)
	if err != nil {
		return fmt.Errorf("wire semantic answer cache: %w", err)
	}
	return nil
}
