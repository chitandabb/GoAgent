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
	return buildConversationMemoryService(ctx, db, cfg, chatmodel.NewProfile)
}

func buildConversationMemoryService(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	modelFactory conversationMemoryModelFactory,
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
	})
}
