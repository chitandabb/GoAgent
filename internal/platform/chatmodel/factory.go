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
	"github.com/eino-contrib/jsonschema"
)

type Capabilities struct {
	ToolCalling              bool
	ReasoningEffort          bool
	ThinkingMode             bool
	ReasoningContentRequired bool
	JSONOutput               bool
	JSONSchemaOutput         bool
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

type ResponseSchema struct {
	Name        string
	Description string
	Schema      *jsonschema.Schema
	Strict      bool
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

// NewProfileWithResponseSchema injects a domain-owned JSON Schema into a
// provider-neutral named model profile. The generic Factory never imports the
// domain that defines the schema.
func NewProfileWithResponseSchema(
	ctx context.Context,
	cfg config.ChatModelConfig,
	name string,
	schema ResponseSchema,
) (*Instance, error) {
	profile, err := cfg.Profile(name)
	if err != nil {
		return nil, err
	}
	return newInstance(ctx, name, profile, &schema)
}

// New resolves a provider Adapter and returns normalized model identity with the client.
func New(ctx context.Context, profileName string, profile config.ChatModelProfileConfig) (*Instance, error) {
	return newInstance(ctx, profileName, profile, nil)
}

func newInstance(
	ctx context.Context,
	profileName string,
	profile config.ChatModelProfileConfig,
	responseSchema *ResponseSchema,
) (*Instance, error) {
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
	switch strings.ToLower(strings.TrimSpace(profile.ResponseFormat)) {
	case "json_object":
		if !adapter.capabilities(profile).JSONOutput {
			return nil, fmt.Errorf("chat model profile %q: provider %s does not support json_object response format", profileName, provider)
		}
		clientConfig.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		}
	case "json_schema":
		if !adapter.capabilities(profile).JSONSchemaOutput {
			return nil, fmt.Errorf("chat model profile %q: provider %s does not support json_schema response format", profileName, provider)
		}
		if responseSchema == nil || responseSchema.Schema == nil ||
			strings.TrimSpace(responseSchema.Name) != strings.TrimSpace(profile.ResponseSchema) {
			return nil, fmt.Errorf("chat model profile %q: configured response schema is unavailable", profileName)
		}
		clientConfig.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name: strings.TrimSpace(responseSchema.Name), Description: strings.TrimSpace(responseSchema.Description),
				JSONSchema: responseSchema.Schema, Strict: responseSchema.Strict,
			},
		}
	}
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
	return Capabilities{
		ToolCalling: true, ReasoningEffort: true,
		JSONOutput: true, JSONSchemaOutput: true,
	}
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
		ReasoningContentRequired: strings.EqualFold(strings.TrimSpace(profile.ThinkingMode), "enabled"), JSONOutput: true,
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
	return Capabilities{ToolCalling: true, ThinkingMode: true, JSONOutput: true, JSONSchemaOutput: true}
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
