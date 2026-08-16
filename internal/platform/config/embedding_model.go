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
	// 进程级配额治理：RPM/TPM 是单个进程的平滑预算；同一进程的所有
	// 消费者共享一个 limiter/client，不能各自获得完整额度。这些运维参数
	// 不参与 Profile fingerprint。
	RPM              int `toml:"rpm"`
	TPM              int `toml:"tpm"`
	MaxAttempts      int `toml:"maxAttempts"`
	BackoffMaxMillis int `toml:"backoffMaxMillis"`
}

// 进程级 Embedding 预算常量。Compose 中恰好四个服务消费 Embedding
// （backend、conversation-worker、diagnosis-worker、knowledge-worker）。
// 900 RPM / 600000 TPM 是仓库采用的保守运行安全边界，不是账号额度声明；
// 默认 4 × 200 RPM = 800、4 × 150000 TPM = 600000 保持在该边界内。
// 本切片不引入 Redis 分布式限流：进程级预算，水平扩容需重新分配额度。
const (
	DefaultEmbeddingRPM              = 200
	DefaultEmbeddingTPM              = 150_000
	DefaultEmbeddingMaxAttempts      = 3
	DefaultEmbeddingBackoffMaxMillis = 10_000
	MaxEmbeddingRPM                  = 900
	MaxEmbeddingTPM                  = 600_000
)

// EffectiveRPM 返回进程级 RPM 预算；未配置（0）时使用默认值。
func (c EmbeddingModelConfig) EffectiveRPM() int {
	if c.RPM < 1 {
		return DefaultEmbeddingRPM
	}
	return c.RPM
}

// EffectiveTPM 返回进程级估算 TPM 预算；未配置（0）时使用默认值。
func (c EmbeddingModelConfig) EffectiveTPM() int {
	if c.TPM < 1 {
		return DefaultEmbeddingTPM
	}
	return c.TPM
}

// EffectiveMaxAttempts 返回包含首次调用在内的最大尝试次数；未配置（0）
// 时使用默认值。
func (c EmbeddingModelConfig) EffectiveMaxAttempts() int {
	if c.MaxAttempts < 1 {
		return DefaultEmbeddingMaxAttempts
	}
	return c.MaxAttempts
}

// EffectiveBackoffMaxMillis 返回有界指数退避上限；未配置（0）时使用默认值。
func (c EmbeddingModelConfig) EffectiveBackoffMaxMillis() int {
	if c.BackoffMaxMillis < 1 {
		return DefaultEmbeddingBackoffMaxMillis
	}
	return c.BackoffMaxMillis
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
	if c.RPM != 0 && (c.RPM < 1 || c.RPM > MaxEmbeddingRPM) {
		return errors.New("models.embedding rpm must be between 1 and 900 when configured")
	}
	if c.TPM != 0 && (c.TPM < 1_000 || c.TPM > MaxEmbeddingTPM) {
		return errors.New("models.embedding tpm must be between 1000 and 600000 when configured")
	}
	if c.MaxAttempts != 0 && (c.MaxAttempts < 1 || c.MaxAttempts > 8) {
		return errors.New("models.embedding maxAttempts must be between 1 and 8 when configured")
	}
	if c.BackoffMaxMillis != 0 && (c.BackoffMaxMillis < 1_000 || c.BackoffMaxMillis > 120_000) {
		return errors.New("models.embedding backoffMaxMillis must be between 1000 and 120000 when configured")
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
