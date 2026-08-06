package config

import (
	"errors"
	"net/url"
	"strings"
)

// RerankModelConfig is an optional post-retrieval model. It is independent from
// both Embedding and ChatModel so it can be enabled and measured separately.
type RerankModelConfig struct {
	Enabled       bool   `toml:"enabled"`
	Provider      string `toml:"provider"`
	Endpoint      string `toml:"endpoint"`
	APIKeyEnv     string `toml:"apiKeyEnv"`
	Model         string `toml:"model"`
	MaxCandidates int    `toml:"maxCandidates"`
	TimeoutMillis int    `toml:"timeoutMillis"`
}

func (c RerankModelConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "dashscope" {
		return errors.New("models.rerank provider must be dashscope")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return errors.New("models.rerank endpoint must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" &&
		(endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("models.rerank endpoint must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("models.rerank apiKeyEnv is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) {
		return errors.New("models.rerank model is invalid")
	}
	if c.MaxCandidates < 1 || c.MaxCandidates > 50 {
		return errors.New("models.rerank maxCandidates must be between 1 and 50")
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 120_000 {
		return errors.New("models.rerank timeoutMillis must be between 1000 and 120000")
	}
	return nil
}

func (c RerankModelConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}
