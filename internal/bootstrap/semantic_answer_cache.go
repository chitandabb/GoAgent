package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	platformredisstack "github.com/chitandabb/GoAgent/internal/platform/redisstack"
	"github.com/chitandabb/GoAgent/internal/resilience"
	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"go.uber.org/zap"
)

func wireSemanticAnswerCache(
	ctx context.Context,
	service *conversation.Service,
	deps *runtimeDependencies,
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
	var repository semanticcache.Provider
	var err error
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "postgres":
		repository, err = platformpostgres.NewSemanticAnswerCacheRepositoryWithConfig(
			deps.db, platformpostgres.SemanticAnswerCacheRepositoryConfig{
				MaxRecords: cfg.MaxRecords, TTLJitterRatio: cfg.TTLJitterRatio,
			},
		)
	case "redis-stack":
		password, passwordErr := cfg.RedisStack.Password()
		if passwordErr != nil {
			return fmt.Errorf("load semantic answer cache redis stack password: %w", passwordErr)
		}
		deps.semanticCacheRedis, err = platformredis.Open(ctx, config.RedisConfig{
			Host: cfg.RedisStack.Host, Port: cfg.RedisStack.Port,
			Password: password, Database: cfg.RedisStack.Database,
		})
		if err != nil {
			log.Warn("Redis Stack semantic answer cache unavailable; continuing without answer cache", zap.Error(err))
			return nil
		}
		authority, authorityErr := platformpostgres.NewSemanticAnswerCacheAuthority(deps.db)
		if authorityErr != nil {
			return fmt.Errorf("build semantic answer cache authority: %w", authorityErr)
		}
		repository, err = platformredisstack.NewSemanticAnswerCache(
			ctx, deps.semanticCacheRedis, authority, platformredisstack.Config{
				IndexName: cfg.RedisStack.IndexName, KeyPrefix: cfg.RedisStack.KeyPrefix,
				MaxRecords: cfg.MaxRecords, TTLJitterRatio: cfg.TTLJitterRatio,
			},
		)
		if err != nil {
			_ = deps.semanticCacheRedis.Close()
			deps.semanticCacheRedis = nil
			log.Warn("Redis Stack semantic answer cache initialization failed; continuing without answer cache", zap.Error(err))
			return nil
		}
	default:
		return fmt.Errorf("unsupported semantic answer cache provider %q", cfg.Provider)
	}
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
		client, clientErr := deps.sharedEmbeddingClient(appConfig)
		if clientErr != nil {
			return fmt.Errorf("build semantic answer cache embedder: %w", clientErr)
		}
		if profileErr = platformpostgres.NewKnowledgeWorkerRepository(deps.db).EnsureEmbeddingProfile(
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
