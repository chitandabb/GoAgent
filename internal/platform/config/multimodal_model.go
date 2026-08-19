package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	maxMultimodalPromptBytes = 16 * 1024
	maxQwenOCROutputTokens   = 4096
)

type MultimodalModelConfig struct {
	Enabled         bool   `toml:"enabled"`
	Provider        string `toml:"provider"`
	BaseURL         string `toml:"baseURL"`
	APIKeyEnv       string `toml:"apiKeyEnv"`
	Model           string `toml:"model"`
	PromptFile      string `toml:"promptFile"`
	PromptVersion   string `toml:"promptVersion"`
	ReasoningEffort string `toml:"reasoningEffort"`
	ResponseFormat  string `toml:"responseFormat"`
	TimeoutMillis   int    `toml:"timeoutMillis"`
	MaxOutputTokens int    `toml:"maxOutputTokens"`
}

func (c MultimodalModelConfig) Validate(owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("multimodal model configuration owner is required")
	}
	if !c.Enabled {
		return nil
	}
	if !modelName.MatchString(strings.TrimSpace(c.Provider)) {
		return fmt.Errorf("%s provider is invalid", owner)
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return fmt.Errorf("%s baseURL must be an absolute URL", owner)
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" &&
		(endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return fmt.Errorf("%s baseURL must use HTTPS unless it points to localhost", owner)
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return fmt.Errorf("%s apiKeyEnv is invalid", owner)
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) {
		return fmt.Errorf("%s model is invalid", owner)
	}
	promptFile := strings.TrimSpace(c.PromptFile)
	if promptFile == "" || len(promptFile) > 512 {
		return fmt.Errorf("%s promptFile must be between 1 and 512 characters", owner)
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return fmt.Errorf("%s promptVersion is invalid", owner)
	}
	if effort := strings.ToLower(strings.TrimSpace(c.ReasoningEffort)); effort != "" && effort != "low" && effort != "medium" && effort != "high" {
		return fmt.Errorf("%s reasoningEffort must be low, medium, high, or empty", owner)
	}
	format := strings.ToLower(strings.TrimSpace(c.ResponseFormat))
	if format != "" && format != "json_object" && format != "text" {
		return fmt.Errorf("%s responseFormat must be json_object, text, or empty", owner)
	}
	if format == "text" && owner != "models.ocr" {
		return fmt.Errorf("%s responseFormat text is only supported for models.ocr", owner)
	}
	if (owner == "models.vision" || owner == "models.table") && format != "json_object" {
		return fmt.Errorf("%s responseFormat must be json_object", owner)
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 300_000 {
		return fmt.Errorf("%s timeoutMillis must be between 1000 and 300000", owner)
	}
	if c.MaxOutputTokens < 128 || c.MaxOutputTokens > 16_384 {
		return fmt.Errorf("%s maxOutputTokens must be between 128 and 16384", owner)
	}
	if owner == "models.ocr" && strings.EqualFold(strings.TrimSpace(c.Provider), "dashscope") &&
		strings.EqualFold(strings.TrimSpace(c.Model), "qwen-vl-ocr-latest") && c.MaxOutputTokens > maxQwenOCROutputTokens {
		return fmt.Errorf("%s maxOutputTokens must not exceed %d for qwen-vl-ocr-latest", owner, maxQwenOCROutputTokens)
	}
	return nil
}

func (c MultimodalModelConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}

func (c MultimodalModelConfig) LoadPrompt(owner string) (string, error) {
	if !c.Enabled {
		return "", fmt.Errorf("%s is disabled", strings.TrimSpace(owner))
	}
	if err := c.Validate(owner); err != nil {
		return "", err
	}
	return loadPromptFile(owner, "prompt", c.PromptFile, maxMultimodalPromptBytes)
}
