// Package chatmodel creates provider-aware Eino chat model clients from named profiles.
package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

type Capabilities struct {
	ToolCalling              bool
	ReasoningEffort          bool
	ThinkingMode             bool
	ReasoningContentRequired bool
}

type Identity struct {
	Profile         string
	Provider        string
	ModelID         string
	ReasoningEffort string
	ThinkingMode    string
	Capabilities    Capabilities
}

type Instance struct {
	Model    model.ToolCallingChatModel
	Identity Identity
}

type providerAdapter interface {
	validate(config.ChatModelProfileConfig) error
	capabilities(config.ChatModelProfileConfig) Capabilities
	configure(*openai.ChatModelConfig, config.ChatModelProfileConfig)
}

// NewActive builds only the selected profile and therefore reads only its API key.
func NewActive(ctx context.Context, cfg config.ChatModelConfig) (*Instance, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("chat model is disabled")
	}
	profile, err := cfg.ActiveProfile()
	if err != nil {
		return nil, err
	}
	return New(ctx, cfg.ActiveProfileName, profile)
}

// NewProfile builds a named profile without requiring it to be the active Agent model.
func NewProfile(ctx context.Context, cfg config.ChatModelConfig, name string) (*Instance, error) {
	profile, err := cfg.Profile(name)
	if err != nil {
		return nil, err
	}
	return New(ctx, name, profile)
}

// New resolves a provider Adapter and returns normalized model identity with the client.
func New(ctx context.Context, profileName string, profile config.ChatModelProfileConfig) (*Instance, error) {
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("chat model profile %q: %w", profileName, err)
	}
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	adapter, err := resolveAdapter(provider)
	if err != nil {
		return nil, err
	}
	if err := adapter.validate(profile); err != nil {
		return nil, fmt.Errorf("chat model profile %q: %w", profileName, err)
	}
	apiKey, err := profile.APIKey()
	if err != nil {
		return nil, fmt.Errorf("chat model profile %q: %w", profileName, err)
	}
	maxOutputTokens := profile.MaxOutputTokens
	clientConfig := &openai.ChatModelConfig{
		APIKey: apiKey, BaseURL: strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/"),
		Model: strings.TrimSpace(profile.Model), Timeout: time.Duration(profile.TimeoutMillis) * time.Millisecond,
		MaxTokens: &maxOutputTokens, Temperature: profile.Temperature,
	}
	adapter.configure(clientConfig, profile)
	chatModel, err := openai.NewChatModel(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create %s chat model: %w", provider, err)
	}
	return &Instance{
		Model: chatModel,
		Identity: Identity{
			Profile: strings.TrimSpace(profileName), Provider: provider, ModelID: strings.TrimSpace(profile.Model),
			ReasoningEffort: strings.ToLower(strings.TrimSpace(profile.ReasoningEffort)),
			ThinkingMode:    strings.ToLower(strings.TrimSpace(profile.ThinkingMode)),
			Capabilities:    adapter.capabilities(profile),
		},
	}, nil
}

func resolveAdapter(provider string) (providerAdapter, error) {
	switch provider {
	case "stepfun":
		return stepFunAdapter{}, nil
	case "deepseek":
		return deepSeekAdapter{}, nil
	case "dashscope":
		return dashScopeAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported chat model provider %q", provider)
	}
}

type stepFunAdapter struct{}

func (stepFunAdapter) validate(profile config.ChatModelProfileConfig) error {
	effort := strings.ToLower(strings.TrimSpace(profile.ReasoningEffort))
	if effort == "" {
		return errors.New("stepfun reasoningEffort is required")
	}
	if effort != "low" && effort != "medium" && effort != "high" {
		return errors.New("stepfun reasoningEffort must be low, medium, or high")
	}
	if strings.TrimSpace(profile.ThinkingMode) != "" {
		return errors.New("stepfun does not support thinkingMode")
	}
	return nil
}

func (stepFunAdapter) capabilities(config.ChatModelProfileConfig) Capabilities {
	return Capabilities{ToolCalling: true, ReasoningEffort: true}
}

func (stepFunAdapter) configure(target *openai.ChatModelConfig, profile config.ChatModelProfileConfig) {
	target.ReasoningEffort = reasoningEffort(profile.ReasoningEffort)
}

type deepSeekAdapter struct{}

func (deepSeekAdapter) validate(profile config.ChatModelProfileConfig) error {
	thinking := strings.ToLower(strings.TrimSpace(profile.ThinkingMode))
	if thinking == "" {
		return errors.New("deepseek thinkingMode must be explicit")
	}
	effort := strings.ToLower(strings.TrimSpace(profile.ReasoningEffort))
	if thinking == "disabled" && effort != "" {
		return errors.New("deepseek reasoningEffort requires thinkingMode enabled")
	}
	if effort == "medium" {
		return errors.New("deepseek reasoningEffort does not support medium")
	}
	return nil
}

func (deepSeekAdapter) capabilities(profile config.ChatModelProfileConfig) Capabilities {
	return Capabilities{
		ToolCalling: true, ReasoningEffort: true, ThinkingMode: true,
		ReasoningContentRequired: strings.EqualFold(strings.TrimSpace(profile.ThinkingMode), "enabled"),
	}
}

func (deepSeekAdapter) configure(target *openai.ChatModelConfig, profile config.ChatModelProfileConfig) {
	target.ExtraFields = map[string]any{
		"thinking": map[string]any{"type": strings.ToLower(strings.TrimSpace(profile.ThinkingMode))},
	}
	if effort := strings.ToLower(strings.TrimSpace(profile.ReasoningEffort)); effort != "" {
		target.ReasoningEffort = openai.ReasoningEffortLevel(effort)
	}
}

type dashScopeAdapter struct{}

func (dashScopeAdapter) validate(profile config.ChatModelProfileConfig) error {
	if strings.TrimSpace(profile.ReasoningEffort) != "" {
		return errors.New("dashscope does not support reasoningEffort")
	}
	if strings.TrimSpace(profile.ThinkingMode) == "" {
		return errors.New("dashscope thinkingMode must be explicit")
	}
	return nil
}

func (dashScopeAdapter) capabilities(config.ChatModelProfileConfig) Capabilities {
	return Capabilities{ToolCalling: true, ThinkingMode: true}
}

func (dashScopeAdapter) configure(target *openai.ChatModelConfig, profile config.ChatModelProfileConfig) {
	target.ExtraFields = map[string]any{
		"enable_thinking": strings.EqualFold(strings.TrimSpace(profile.ThinkingMode), "enabled"),
	}
}

func reasoningEffort(value string) openai.ReasoningEffortLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return openai.ReasoningEffortLevelLow
	case "high":
		return openai.ReasoningEffortLevelHigh
	default:
		return openai.ReasoningEffortLevelMedium
	}
}
