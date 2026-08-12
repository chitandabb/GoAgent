package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/memorycompactor"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformredis "github.com/chitandabb/GoAgent/internal/platform/redis"
	"github.com/cloudwego/eino/components/model"

	"github.com/google/uuid"
	rediscli "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type conversationMemoryModelFactory func(
	context.Context,
	config.ChatModelConfig,
	string,
) (*chatmodel.Instance, error)

// BuildConversationMemoryService composes the shared Shadow and Active Summary
// implementation. Constructing the service has no side effects; callers must
// explicitly generate a candidate or CAS-activate it for a Conversation turn.
func BuildConversationMemoryService(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
) (*conversationmemory.Service, error) {
	return buildConversationMemoryService(ctx, db, cfg, newConversationMemoryModelProfile)
}

// BuildConversationMemoryServiceWithModel is the production compactor
// assembly with an injected model and repository. It is used by the bounded
// offline Pilot so the real Summary prompt, validator, retry, and incremental
// merge path can run without writing evaluation snapshots to PostgreSQL.
func BuildConversationMemoryServiceWithModel(
	ctx context.Context,
	cfg config.Config,
	modelOverride model.ToolCallingChatModel,
	repository conversationmemory.ActivationRepository,
) (*conversationmemory.Service, conversationmemory.SummaryProvenance, error) {
	if repository == nil || modelOverride == nil {
		return nil, conversationmemory.SummaryProvenance{}, errors.New("conversation memory Pilot dependencies are unavailable")
	}
	summaryConfig := cfg.Agent.ContextMemory.Summary
	if err := summaryConfig.Validate(); err != nil {
		return nil, conversationmemory.SummaryProvenance{}, err
	}
	profileName := strings.TrimSpace(cfg.Models.Chat.ConversationMemoryProfileName)
	profile, err := cfg.Models.Chat.ConversationMemoryProfile()
	if err != nil {
		return nil, conversationmemory.SummaryProvenance{}, fmt.Errorf("resolve conversation memory model profile: %w", err)
	}
	if err := validateConversationMemoryProfile(profile); err != nil {
		return nil, conversationmemory.SummaryProvenance{}, err
	}
	prompt, err := summaryConfig.LoadPrompt()
	if err != nil {
		return nil, conversationmemory.SummaryProvenance{}, err
	}
	compactor, err := memorycompactor.New(memorycompactor.Config{
		Generator: modelOverride, Prompt: prompt, PromptVersion: summaryConfig.PromptVersion,
		Timeout: time.Duration(profile.TimeoutMillis) * time.Millisecond, MaxOutputBytes: summaryConfig.MaxPayloadBytes,
	})
	if err != nil {
		return nil, conversationmemory.SummaryProvenance{}, err
	}
	provenance := conversationmemory.SummaryProvenance{
		ModelProfile: profileName, ModelProvider: strings.ToLower(strings.TrimSpace(profile.Provider)),
		ModelID: strings.TrimSpace(profile.Model), PromptVersion: summaryConfig.PromptVersion,
	}
	service, err := conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: repository, Compactor: compactor, SchemaVersion: conversationmemory.CurrentSchemaVersion,
		MaxPayloadBytes: summaryConfig.MaxPayloadBytes, Provenance: provenance,
		MaxAttempts: summaryConfig.MaxAttempts, RetryBaseDelay: time.Duration(summaryConfig.RetryBaseDelayMillis) * time.Millisecond,
	})
	if err != nil {
		return nil, conversationmemory.SummaryProvenance{}, err
	}
	return service, provenance, nil
}

// BuildCachedConversationMemoryService adds the degradable Redis read-through
// adapter used by the Conversation Worker. A nil Redis client is valid when
// cache is configured: reads fall back to PostgreSQL and emit degraded
// observations instead of making Redis startup-critical.
func BuildCachedConversationMemoryService(
	ctx context.Context,
	db *gorm.DB,
	redisClient *rediscli.Client,
	cfg config.Config,
	log *zap.Logger,
) (*conversationmemory.Service, error) {
	cacheExpected := cfg.Agent.ContextMemory.MemoryCacheEnabled
	var cache conversationmemory.SnapshotCache
	if cacheExpected && redisClient != nil {
		ttl, err := cfg.Agent.ContextMemory.MemoryCacheDuration()
		if err != nil {
			return nil, err
		}
		cache, err = platformredis.NewConversationMemoryCache(redisClient, platformredis.ConversationMemoryCacheConfig{
			TTL: ttl, JitterRatio: cfg.Agent.ContextMemory.MemoryCacheJitterRatio,
			OperationTimeout: time.Duration(cfg.Agent.ContextMemory.MemoryCacheTimeoutMillis) * time.Millisecond,
		})
		if err != nil {
			return nil, fmt.Errorf("build conversation memory Redis cache: %w", err)
		}
	}
	var observer conversationmemory.CacheObserver
	if log != nil {
		observer = conversationMemoryCacheLogger{log: log.Named("redis_cache")}
	}
	return buildConversationMemoryServiceWithCache(
		ctx, db, cfg, newConversationMemoryModelProfile, cache, cacheExpected, observer,
	)
}

func newConversationMemoryModelProfile(
	ctx context.Context,
	cfg config.ChatModelConfig,
	profileName string,
) (*chatmodel.Instance, error) {
	return chatmodel.NewProfileWithResponseSchema(ctx, cfg, profileName, chatmodel.ResponseSchema{
		Name:        conversationmemory.ResponseSchemaName,
		Description: "MESGuard structured conversation memory snapshot",
		Schema:      conversationmemory.PayloadJSONSchema(), Strict: true,
	})
}

func buildConversationMemoryService(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	modelFactory conversationMemoryModelFactory,
) (*conversationmemory.Service, error) {
	return buildConversationMemoryServiceWithCache(ctx, db, cfg, modelFactory, nil, false, nil)
}

func buildConversationMemoryServiceWithCache(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	modelFactory conversationMemoryModelFactory,
	cache conversationmemory.SnapshotCache,
	cacheExpected bool,
	cacheObserver conversationmemory.CacheObserver,
) (*conversationmemory.Service, error) {
	if db == nil || modelFactory == nil {
		return nil, errors.New("conversation memory dependencies are unavailable")
	}
	summaryConfig := cfg.Agent.ContextMemory.Summary
	if err := summaryConfig.Validate(); err != nil {
		return nil, err
	}
	if !summaryConfig.Enabled {
		return nil, errors.New("conversation memory Summary is disabled")
	}
	profileName := strings.TrimSpace(cfg.Models.Chat.ConversationMemoryProfileName)
	profile, err := cfg.Models.Chat.ConversationMemoryProfile()
	if err != nil {
		return nil, fmt.Errorf("resolve conversation memory model profile: %w", err)
	}
	if err := validateConversationMemoryProfile(profile); err != nil {
		return nil, err
	}
	prompt, err := summaryConfig.LoadPrompt()
	if err != nil {
		return nil, err
	}
	instance, err := modelFactory(ctx, cfg.Models.Chat, profileName)
	if err != nil {
		return nil, fmt.Errorf("create conversation memory model: %w", err)
	}
	if instance == nil || instance.Model == nil || instance.Identity.Profile != profileName ||
		strings.TrimSpace(instance.Identity.Provider) == "" || strings.TrimSpace(instance.Identity.ModelID) == "" {
		return nil, errors.New("conversation memory model identity is invalid")
	}
	compactor, err := memorycompactor.New(memorycompactor.Config{
		Generator: instance.Model, Prompt: prompt, PromptVersion: summaryConfig.PromptVersion,
		Timeout:        time.Duration(profile.TimeoutMillis) * time.Millisecond,
		MaxOutputBytes: summaryConfig.MaxPayloadBytes,
	})
	if err != nil {
		return nil, err
	}
	return conversationmemory.NewService(conversationmemory.ServiceConfig{
		Repository: platformpostgres.NewConversationMemoryRepository(db), Compactor: compactor,
		SchemaVersion: conversationmemory.CurrentSchemaVersion, MaxPayloadBytes: summaryConfig.MaxPayloadBytes,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: profileName, ModelProvider: instance.Identity.Provider,
			ModelID: instance.Identity.ModelID, PromptVersion: summaryConfig.PromptVersion,
		},
		MaxAttempts:    summaryConfig.MaxAttempts,
		RetryBaseDelay: time.Duration(summaryConfig.RetryBaseDelayMillis) * time.Millisecond,
		Cache:          cache, CacheExpected: cacheExpected, CacheObserver: cacheObserver,
	})
}

func validateConversationMemoryProfile(profile config.ChatModelProfileConfig) error {
	if strings.ToLower(strings.TrimSpace(profile.ResponseFormat)) != "json_schema" {
		return errors.New("conversation memory model must use json_schema response format")
	}
	if strings.TrimSpace(profile.ResponseSchema) != conversationmemory.ResponseSchemaName {
		return fmt.Errorf("conversation memory model must use response schema %q", conversationmemory.ResponseSchemaName)
	}
	return nil
}

type conversationMemoryCacheLogger struct {
	log *zap.Logger
}

func (o conversationMemoryCacheLogger) Observe(
	_ context.Context,
	observation conversationmemory.CacheObservation,
) {
	if o.log == nil || observation.Validate() != nil {
		return
	}
	fields := []zap.Field{
		zap.String("operation", string(observation.Operation)),
		zap.String("status", string(observation.Status)),
		zap.String("conversation_id", observation.ConversationID.String()),
		zap.Int64("duration_micros", observation.Duration.Microseconds()),
	}
	if observation.SnapshotID != uuid.Nil {
		fields = append(fields, zap.String("snapshot_id", observation.SnapshotID.String()))
	}
	if observation.Reason != "" {
		fields = append(fields, zap.String("degraded_reason", string(observation.Reason)))
	}
	if observation.Status == conversationmemory.CacheStatusDegraded {
		o.log.Warn("conversation memory cache degraded; PostgreSQL remains authoritative", fields...)
		return
	}
	o.log.Debug("conversation memory cache observed", fields...)
}
