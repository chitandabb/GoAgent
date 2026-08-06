package config

import (
	"errors"
	"net/url"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

// EmbeddingModelConfig is independent from the chat model because vector
// identity and dimensions are persisted as an index compatibility contract.
type EmbeddingModelConfig struct {
	Enabled           bool   `toml:"enabled"`
	ProfileKey        string `toml:"profileKey"`
	Provider          string `toml:"provider"`
	Endpoint          string `toml:"endpoint"`
	APIKeyEnv         string `toml:"apiKeyEnv"`
	Model             string `toml:"model"`
	Dimensions        int    `toml:"dimensions"`
	DistanceMetric    string `toml:"distanceMetric"`
	QueryInputType    string `toml:"queryInputType"`
	DocumentInputType string `toml:"documentInputType"`
	Normalize         bool   `toml:"normalize"`
	ConfigVersion     string `toml:"configVersion"`
	BatchSize         int    `toml:"batchSize"`
	MaxConcurrent     int    `toml:"maxConcurrent"`
	TimeoutMillis     int    `toml:"timeoutMillis"`
}

func (c EmbeddingModelConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "dashscope" {
		return errors.New("models.embedding provider must be dashscope")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return errors.New("models.embedding endpoint must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" &&
		(endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("models.embedding endpoint must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("models.embedding apiKeyEnv is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) ||
		!modelName.MatchString(strings.TrimSpace(c.ProfileKey)) ||
		!modelName.MatchString(strings.TrimSpace(c.ConfigVersion)) {
		return errors.New("models.embedding model, profileKey, or configVersion is invalid")
	}
	if c.Dimensions != 1024 {
		return errors.New("models.embedding dimensions must be 1024 for the current pgvector schema")
	}
	if strings.ToLower(strings.TrimSpace(c.DistanceMetric)) != "cosine" {
		return errors.New("models.embedding distanceMetric must be cosine")
	}
	if strings.ToLower(strings.TrimSpace(c.QueryInputType)) != string(knowledge.EmbeddingInputQuery) ||
		strings.ToLower(strings.TrimSpace(c.DocumentInputType)) != string(knowledge.EmbeddingInputDocument) {
		return errors.New("models.embedding must distinguish query and document input types")
	}
	if !c.Normalize {
		return errors.New("models.embedding normalize must be true for the cosine profile")
	}
	if c.BatchSize < 1 || c.BatchSize > 10 {
		return errors.New("models.embedding batchSize must be between 1 and 10")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 8 {
		return errors.New("models.embedding maxConcurrent must be between 1 and 8")
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 120_000 {
		return errors.New("models.embedding timeoutMillis must be between 1000 and 120000")
	}
	_, err = c.Profile()
	return err
}

func (c EmbeddingModelConfig) Profile() (knowledge.EmbeddingProfile, error) {
	return knowledge.NewEmbeddingProfile(
		strings.TrimSpace(c.ProfileKey), strings.ToLower(strings.TrimSpace(c.Provider)), strings.TrimSpace(c.Model),
		c.Dimensions, strings.ToLower(strings.TrimSpace(c.DistanceMetric)),
		knowledge.EmbeddingInputType(strings.ToLower(strings.TrimSpace(c.QueryInputType))),
		knowledge.EmbeddingInputType(strings.ToLower(strings.TrimSpace(c.DocumentInputType))),
		c.Normalize, strings.TrimSpace(c.ConfigVersion),
	)
}

func (c EmbeddingModelConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}
