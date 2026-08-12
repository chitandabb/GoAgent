package config

// ============================================================
// 配置层：负责从 .env 文件和 TOML 配置文件加载 MESGuard 的运行配置。
//
// 加载顺序：.env → 确定 TOML 路径 → 解码 TOML → 校验
// 同名环境变量优先级：系统环境变量 > .env 文件中的变量
// TOML 普通字段不会被任意环境变量自动覆盖；敏感值通过 passwordEnv 显式引用。
// ============================================================

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

// Config 是 MESGuard 的顶层配置结构，对应 config/mesguard.toml 文件的顶层键。
// 每个字段对应 TOML 中的一个配置块（通过 toml tag 映射）。
type Config struct {
	HTTP      HTTPConfig      `toml:"http"`      // [http] 配置块：API 服务器监听地址
	Auth      AuthConfig      `toml:"auth"`      // [auth] 配置块：Session、Cookie 与可信前端来源
	Agent     AgentConfig     `toml:"agent"`     // [agent] Skill 包加载位置
	Models    ModelsConfig    `toml:"models"`    // [models] 聊天、向量和重排模型
	Log       LogConfig       `toml:"log"`       // [log] 配置块：结构化日志和文件轮转
	Postgres  PostgresConfig  `toml:"postgres"`  // [postgres] 配置块：PostgreSQL 数据库连接配置
	Redis     RedisConfig     `toml:"redis"`     // [redis] 配置块：Redis 缓存连接配置
	RabbitMQ  RabbitMQConfig  `toml:"rabbitmq"`  // [rabbitmq] Outbox Relay 与 Worker 消息配置
	SQLServer SQLServerConfig `toml:"sqlserver"` // [sqlserver] 公司 ERP 工单库（可降级依赖）
	GitHubMCP GitHubMCPConfig `toml:"githubMCP"` // [githubMCP] 官方 GitHub MCP 只读代码调查
	WebSearch WebSearchConfig `toml:"webSearch"` // [webSearch] 公开技术资料的脱敏只读检索
	MinIO     MinIOConfig     `toml:"minio"`     // [minio] 附件与知识原文的可降级对象存储
	Knowledge KnowledgeConfig `toml:"knowledge"` // [knowledge] 文档入库流水线版本与恢复预算
}

// ModelsConfig 为不同模型职责保留独立配置，避免聊天模型和向量模型共享错误参数。
type ModelsConfig struct {
	Chat      ChatModelConfig       `toml:"chat"`
	Judge     JudgeModelConfig      `toml:"judge"`
	Embedding EmbeddingModelConfig  `toml:"embedding"`
	Rerank    RerankModelConfig     `toml:"rerank"`
	OCR       MultimodalModelConfig `toml:"ocr"`
	Vision    MultimodalModelConfig `toml:"vision"`
	Table     MultimodalModelConfig `toml:"table"`
}

// ChatModelConfig 通过命名 Profile 隔离不同模型职责和 Provider 参数。
type ChatModelConfig struct {
	Enabled                       bool                              `toml:"enabled"`
	ActiveProfileName             string                            `toml:"activeProfile"`
	ConversationMemoryProfileName string                            `toml:"conversationMemoryProfile"`
	Profiles                      map[string]ChatModelProfileConfig `toml:"profiles"`
}

type TokenizerStrategy string

const (
	TokenizerStrategyLocalExact            TokenizerStrategy = "local_exact"
	TokenizerStrategyLocalCalibrated       TokenizerStrategy = "local_calibrated"
	TokenizerStrategyConservativeHeuristic TokenizerStrategy = "conservative_heuristic"
)

func (s TokenizerStrategy) Valid() bool {
	switch TokenizerStrategy(strings.ToLower(strings.TrimSpace(string(s)))) {
	case TokenizerStrategyLocalExact,
		TokenizerStrategyLocalCalibrated,
		TokenizerStrategyConservativeHeuristic:
		return true
	default:
		return false
	}
}

type ToolExposureStrategy string

const (
	ToolExposureStrategyStaticFrozen   ToolExposureStrategy = "static_frozen"
	ToolExposureStrategyNativeDeferred ToolExposureStrategy = "native_deferred"
	ToolExposureStrategyEpochRebind    ToolExposureStrategy = "epoch_rebind"
	ToolExposureStrategyGateway        ToolExposureStrategy = "gateway"
)

func (s ToolExposureStrategy) Valid() bool {
	switch ToolExposureStrategy(strings.ToLower(strings.TrimSpace(string(s)))) {
	case ToolExposureStrategyStaticFrozen,
		ToolExposureStrategyNativeDeferred,
		ToolExposureStrategyEpochRebind,
		ToolExposureStrategyGateway:
		return true
	default:
		return false
	}
}

// ChatModelProfileConfig 是单个 OpenAI 兼容模型端点的静态配置。
// Provider 专有参数由 chatmodel Adapter 校验和映射。
type ChatModelProfileConfig struct {
	Provider                        string               `toml:"provider"`
	BaseURL                         string               `toml:"baseURL"`
	APIKeyEnv                       string               `toml:"apiKeyEnv"`
	Model                           string               `toml:"model"`
	ReasoningEffort                 string               `toml:"reasoningEffort"`
	ThinkingMode                    string               `toml:"thinkingMode"`
	ResponseFormat                  string               `toml:"responseFormat"`
	ResponseSchema                  string               `toml:"responseSchema"`
	Temperature                     *float32             `toml:"temperature"`
	TimeoutMillis                   int                  `toml:"timeoutMillis"`
	ContextWindowTokens             int                  `toml:"contextWindowTokens"`
	MaxOutputTokens                 int                  `toml:"maxOutputTokens"`
	PromptSafetyMarginTokens        int                  `toml:"promptSafetyMarginTokens"`
	PromptSafetyMarginRatio         float64              `toml:"promptSafetyMarginRatio"`
	TokenizerStrategy               TokenizerStrategy    `toml:"tokenizerStrategy"`
	ToolExposureStrategy            ToolExposureStrategy `toml:"toolExposureStrategy"`
	ProviderNativeCompactionEnabled bool                 `toml:"providerNativeCompactionEnabled"`
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (c ChatModelConfig) Validate() error {
	for name, profile := range c.Profiles {
		if !modelName.MatchString(strings.TrimSpace(name)) {
			return fmt.Errorf("models.chat profile name %q is invalid", name)
		}
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("models.chat profile %q: %w", name, err)
		}
		if c.Enabled {
			if err := profile.requireContextContract(); err != nil {
				return fmt.Errorf("models.chat profile %q: %w", name, err)
			}
		}
	}
	if !c.Enabled {
		return nil
	}
	if _, err := c.ActiveProfile(); err != nil {
		return err
	}
	if _, err := c.ConversationMemoryProfile(); err != nil {
		return err
	}
	return nil
}

func (c ChatModelConfig) ActiveProfile() (ChatModelProfileConfig, error) {
	name := strings.TrimSpace(c.ActiveProfileName)
	if !modelName.MatchString(name) {
		return ChatModelProfileConfig{}, errors.New("models.chat activeProfile is invalid")
	}
	return c.Profile(name)
}

func (c ChatModelConfig) ConversationMemoryProfile() (ChatModelProfileConfig, error) {
	name := strings.TrimSpace(c.ConversationMemoryProfileName)
	if !modelName.MatchString(name) {
		return ChatModelProfileConfig{}, errors.New("models.chat conversationMemoryProfile is invalid")
	}
	profile, err := c.Profile(name)
	if err != nil {
		return ChatModelProfileConfig{}, fmt.Errorf("models.chat conversationMemoryProfile: %w", err)
	}
	return profile, nil
}

func (c ChatModelConfig) Profile(name string) (ChatModelProfileConfig, error) {
	name = strings.TrimSpace(name)
	profile, ok := c.Profiles[name]
	if !ok {
		return ChatModelProfileConfig{}, fmt.Errorf("models.chat profile %q is not configured", name)
	}
	return profile, nil
}

func (c ChatModelProfileConfig) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "stepfun", "deepseek", "dashscope":
	default:
		return errors.New("provider must be stepfun, deepseek, or dashscope")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Host == "" {
		return errors.New("baseURL must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("baseURL must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("apiKeyEnv is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) {
		return errors.New("model is invalid")
	}
	if effort := strings.ToLower(strings.TrimSpace(c.ReasoningEffort)); effort != "" {
		switch effort {
		case "low", "medium", "high", "xhigh", "max":
		default:
			return errors.New("reasoningEffort must be low, medium, high, xhigh, or max when configured")
		}
	}
	if thinking := strings.ToLower(strings.TrimSpace(c.ThinkingMode)); thinking != "" && thinking != "enabled" && thinking != "disabled" {
		return errors.New("thinkingMode must be enabled or disabled when configured")
	}
	format := strings.ToLower(strings.TrimSpace(c.ResponseFormat))
	if format != "" && format != "text" && format != "json_object" && format != "json_schema" {
		return errors.New("responseFormat must be text, json_object, or json_schema when configured")
	}
	responseSchema := strings.TrimSpace(c.ResponseSchema)
	if format == "json_schema" && !modelName.MatchString(responseSchema) {
		return errors.New("responseSchema is required for json_schema responseFormat")
	}
	if format != "json_schema" && responseSchema != "" {
		return errors.New("responseSchema requires json_schema responseFormat")
	}
	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if c.TimeoutMillis <= 0 || c.TimeoutMillis > 300_000 {
		return errors.New("timeoutMillis must be between 1 and 300000")
	}
	if c.MaxOutputTokens <= 0 || c.MaxOutputTokens > 65_536 {
		return errors.New("maxOutputTokens must be between 1 and 65536")
	}
	if c.hasContextContract() {
		return c.validateContextContract()
	}
	return nil
}

func (c ChatModelProfileConfig) EffectivePromptSafetyMarginTokens() int {
	ratioMargin := int(math.Ceil(float64(c.ContextWindowTokens) * c.PromptSafetyMarginRatio))
	if ratioMargin > c.PromptSafetyMarginTokens {
		return ratioMargin
	}
	return c.PromptSafetyMarginTokens
}

// PromptProfileFingerprint identifies every configured profile field that can
// change model-visible behavior or the prompt-window contract. Secrets and the
// API key environment variable are intentionally excluded.
func (c ChatModelProfileConfig) PromptProfileFingerprint(profileName string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	profileName = strings.TrimSpace(profileName)
	if !modelName.MatchString(profileName) {
		return "", errors.New("chat model profile name is invalid")
	}
	encoded, err := json.Marshal(struct {
		Name                     string               `json:"name"`
		Provider                 string               `json:"provider"`
		BaseURL                  string               `json:"baseUrl"`
		Model                    string               `json:"model"`
		ReasoningEffort          string               `json:"reasoningEffort"`
		ThinkingMode             string               `json:"thinkingMode"`
		ResponseFormat           string               `json:"responseFormat"`
		ResponseSchema           string               `json:"responseSchema"`
		Temperature              *float32             `json:"temperature,omitempty"`
		TimeoutMillis            int                  `json:"timeoutMillis"`
		ContextWindowTokens      int                  `json:"contextWindowTokens"`
		MaxOutputTokens          int                  `json:"maxOutputTokens"`
		PromptSafetyMarginTokens int                  `json:"promptSafetyMarginTokens"`
		PromptSafetyMarginRatio  float64              `json:"promptSafetyMarginRatio"`
		TokenizerStrategy        string               `json:"tokenizerStrategy"`
		ToolExposureStrategy     ToolExposureStrategy `json:"toolExposureStrategy"`
		ProviderNativeCompaction bool                 `json:"providerNativeCompaction"`
	}{
		Name: profileName, Provider: strings.ToLower(strings.TrimSpace(c.Provider)),
		BaseURL: strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"), Model: strings.TrimSpace(c.Model),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(c.ReasoningEffort)),
		ThinkingMode:    strings.ToLower(strings.TrimSpace(c.ThinkingMode)),
		ResponseFormat:  strings.ToLower(strings.TrimSpace(c.ResponseFormat)), Temperature: c.Temperature,
		ResponseSchema: strings.TrimSpace(c.ResponseSchema),
		TimeoutMillis:  c.TimeoutMillis, ContextWindowTokens: c.ContextWindowTokens,
		MaxOutputTokens: c.MaxOutputTokens, PromptSafetyMarginTokens: c.PromptSafetyMarginTokens,
		PromptSafetyMarginRatio:  c.PromptSafetyMarginRatio,
		TokenizerStrategy:        strings.ToLower(strings.TrimSpace(string(c.TokenizerStrategy))),
		ToolExposureStrategy:     c.EffectiveToolExposureStrategy(),
		ProviderNativeCompaction: c.ProviderNativeCompactionEnabled,
	})
	if err != nil {
		return "", err
	}
	return contextgovernance.SHA256Hex(string(encoded)), nil
}

func (c ChatModelProfileConfig) EffectiveToolExposureStrategy() ToolExposureStrategy {
	strategy := ToolExposureStrategy(strings.ToLower(strings.TrimSpace(string(c.ToolExposureStrategy))))
	if strategy == "" {
		return ToolExposureStrategyStaticFrozen
	}
	return strategy
}

func (c ChatModelProfileConfig) hasContextContract() bool {
	return c.ContextWindowTokens != 0 || c.PromptSafetyMarginTokens != 0 ||
		c.PromptSafetyMarginRatio != 0 || strings.TrimSpace(string(c.TokenizerStrategy)) != "" ||
		strings.TrimSpace(string(c.ToolExposureStrategy)) != "" || c.ProviderNativeCompactionEnabled
}

// requireContextContract keeps direct role-specific Provider clients compatible while
// requiring every enabled named Chat profile to opt into the M3 context contract.
func (c ChatModelProfileConfig) requireContextContract() error {
	if !c.hasContextContract() {
		return errors.New("contextWindowTokens, prompt safety margin, and tokenizerStrategy are required")
	}
	return nil
}

func (c ChatModelProfileConfig) validateContextContract() error {
	if c.ContextWindowTokens < 1024 || c.ContextWindowTokens > 4_000_000 {
		return errors.New("contextWindowTokens must be between 1024 and 4000000")
	}
	if c.MaxOutputTokens >= c.ContextWindowTokens {
		return errors.New("maxOutputTokens must be less than contextWindowTokens")
	}
	if c.PromptSafetyMarginTokens < 0 || c.PromptSafetyMarginTokens >= c.ContextWindowTokens {
		return errors.New("promptSafetyMarginTokens must be non-negative and less than contextWindowTokens")
	}
	if math.IsNaN(c.PromptSafetyMarginRatio) || math.IsInf(c.PromptSafetyMarginRatio, 0) ||
		c.PromptSafetyMarginRatio < 0 || c.PromptSafetyMarginRatio > 0.5 {
		return errors.New("promptSafetyMarginRatio must be between 0 and 0.5")
	}
	if !c.TokenizerStrategy.Valid() {
		return errors.New("tokenizerStrategy must be local_exact, local_calibrated, or conservative_heuristic")
	}
	if !c.EffectiveToolExposureStrategy().Valid() {
		return errors.New("toolExposureStrategy must be static_frozen, native_deferred, epoch_rebind, or gateway")
	}
	margin := c.EffectivePromptSafetyMarginTokens()
	if margin == 0 {
		return errors.New("prompt safety margin must be positive")
	}
	if c.MaxOutputTokens+margin >= c.ContextWindowTokens {
		return errors.New("maxOutputTokens and prompt safety margin must leave input capacity")
	}
	return nil
}

func (c ChatModelProfileConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}

// JudgeModelConfig 定义离线 RAG 评测使用的独立 LLM Judge。
// Judge 不参与在线回答，也不能替代人工黄金事实和确定性引用校验。
type JudgeModelConfig struct {
	Enabled         bool   `toml:"enabled"`
	Provider        string `toml:"provider"`
	BaseURL         string `toml:"baseURL"`
	APIKeyEnv       string `toml:"apiKeyEnv"`
	Model           string `toml:"model"`
	PromptFile      string `toml:"promptFile"`
	PromptVersion   string `toml:"promptVersion"`
	TimeoutMillis   int    `toml:"timeoutMillis"`
	MaxOutputTokens int    `toml:"maxOutputTokens"`
}

func (c JudgeModelConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "dashscope" {
		return errors.New("models.judge provider must be dashscope")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return errors.New("models.judge baseURL must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("models.judge baseURL must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("models.judge apiKeyEnv is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) {
		return errors.New("models.judge model is invalid")
	}
	promptFile := strings.TrimSpace(c.PromptFile)
	if promptFile == "" || len(promptFile) > 512 {
		return errors.New("models.judge promptFile must be between 1 and 512 characters")
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return errors.New("models.judge promptVersion is invalid")
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 300_000 {
		return errors.New("models.judge timeoutMillis must be between 1000 and 300000")
	}
	if c.MaxOutputTokens < 256 || c.MaxOutputTokens > 16_384 {
		return errors.New("models.judge maxOutputTokens must be between 256 and 16384")
	}
	return nil
}

func (c JudgeModelConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}

// AgentConfig 声明 Skill/Prompt 文件位置、Prompt 发布标签和一次诊断的外层 Evidence Gate 总预算。
type AgentConfig struct {
	SkillsDirectory                           string              `toml:"skillsDirectory"`
	PromptVersion                             string              `toml:"promptVersion"`
	SystemPromptFile                          string              `toml:"systemPromptFile"`
	BaselinePromptFile                        string              `toml:"baselinePromptFile"`
	ReportContractFile                        string              `toml:"reportContractFile"`
	ConversationPromptVersion                 string              `toml:"conversationPromptVersion"`
	ConversationPromptFile                    string              `toml:"conversationPromptFile"`
	ConversationCitationRepairEnabled         bool                `toml:"conversationCitationRepairEnabled"`
	ConversationCitationRepairPromptVersion   string              `toml:"conversationCitationRepairPromptVersion"`
	ConversationCitationRepairPromptFile      string              `toml:"conversationCitationRepairPromptFile"`
	ConversationCitationRepairTimeoutMillis   int                 `toml:"conversationCitationRepairTimeoutMillis"`
	ConversationCitationRepairMaxOutputTokens int                 `toml:"conversationCitationRepairMaxOutputTokens"`
	ConversationMaxIterations                 int                 `toml:"conversationMaxIterations"`
	ConversationMaxContextRunes               int                 `toml:"conversationMaxContextRunes"`
	ConversationTimeoutMillis                 int                 `toml:"conversationTimeoutMillis"`
	MaxAgentRuns                              int                 `toml:"maxAgentRuns"`
	MaxToolCalls                              int                 `toml:"maxToolCalls"`
	MaxEvidenceItems                          int                 `toml:"maxEvidenceItems"`
	MaxTotalTokens                            int                 `toml:"maxTotalTokens"`
	TimeoutMillis                             int                 `toml:"timeoutMillis"`
	ContextMemory                             ContextMemoryConfig `toml:"contextMemory"`
}

// ContextMemoryConfig controls prompt observation and the staged activation of
// Token-aware conversation assembly. Continuous Tail depends on shadow
// preflight so every activated prompt still produces the same bounded manifest;
// the Rune selector remains an explicit rollback path.
type ContextMemoryConfig struct {
	ShadowPreflightEnabled      bool                            `toml:"shadowPreflightEnabled"`
	DiagnosisPreflightEnabled   bool                            `toml:"diagnosisPreflightEnabled"`
	ContinuousTailEnabled       bool                            `toml:"continuousTailEnabled"`
	SummaryTailEnabled          bool                            `toml:"summaryTailEnabled"`
	AsyncCompactionEnabled      bool                            `toml:"asyncCompactionEnabled"`
	AsyncMaxAttempts            int                             `toml:"asyncMaxAttempts"`
	RetryJitterRatio            float64                         `toml:"retryJitterRatio"`
	MemoryCacheEnabled          bool                            `toml:"memoryCacheEnabled"`
	MemoryCacheTTL              string                          `toml:"memoryCacheTTL"`
	MemoryCacheJitterRatio      float64                         `toml:"memoryCacheJitterRatio"`
	MemoryCacheTimeoutMillis    int                             `toml:"memoryCacheTimeoutMillis"`
	SourceRecoveryEnabled       bool                            `toml:"sourceRecoveryEnabled"`
	SourceRecoveryMaxMessages   int                             `toml:"sourceRecoveryMaxMessages"`
	SourceRecoveryMaxTokens     int                             `toml:"sourceRecoveryMaxTokens"`
	SourceRecoveryMaxCalls      int                             `toml:"sourceRecoveryMaxCalls"`
	MemoryMaxRatio              float64                         `toml:"memoryMaxRatio"`
	SummaryMaxRatio             float64                         `toml:"summaryMaxRatio"`
	TailMaxRatio                float64                         `toml:"tailMaxRatio"`
	PreflightTimeoutMillis      int                             `toml:"preflightTimeoutMillis"`
	SoftThresholdRatio          float64                         `toml:"softThresholdRatio"`
	HardThresholdRatio          float64                         `toml:"hardThresholdRatio"`
	ToolGrowthReserveTokens     int                             `toml:"toolGrowthReserveTokens"`
	SyncCompactionTimeoutMillis int                             `toml:"syncCompactionTimeoutMillis"`
	Summary                     ConversationMemorySummaryConfig `toml:"summary"`
}

// ConversationMemorySummaryConfig controls the independently callable Summary
// compactor shared by Shadow generation and synchronous Active preparation.
type ConversationMemorySummaryConfig struct {
	Enabled              bool   `toml:"enabled"`
	PromptFile           string `toml:"promptFile"`
	PromptVersion        string `toml:"promptVersion"`
	MaxPayloadBytes      int    `toml:"maxPayloadBytes"`
	MaxAttempts          int    `toml:"maxAttempts"`
	RetryBaseDelayMillis int    `toml:"retryBaseDelayMillis"`
}

func (c ContextMemoryConfig) MemoryCacheDuration() (time.Duration, error) {
	value := strings.TrimSpace(c.MemoryCacheTTL)
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < time.Minute || parsed > 7*24*time.Hour {
		return 0, errors.New("agent contextMemory memoryCacheTTL must be a duration between 1m and 168h")
	}
	return parsed, nil
}

func (c ConversationMemorySummaryConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	path := strings.TrimSpace(c.PromptFile)
	if path == "" || len(path) > 512 {
		return errors.New("agent contextMemory summary promptFile is required and must not exceed 512 characters")
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return errors.New("agent contextMemory summary promptVersion is invalid")
	}
	if c.MaxPayloadBytes < 1024 || c.MaxPayloadBytes > 1024*1024 {
		return errors.New("agent contextMemory summary maxPayloadBytes must be between 1024 and 1048576")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("agent contextMemory summary maxAttempts must be between 1 and 5")
	}
	if c.RetryBaseDelayMillis < 0 || c.RetryBaseDelayMillis > 60_000 {
		return errors.New("agent contextMemory summary retryBaseDelayMillis must be between 0 and 60000")
	}
	return nil
}

func (c ContextMemoryConfig) Validate() error {
	if c.ContinuousTailEnabled && !c.ShadowPreflightEnabled {
		return errors.New("agent contextMemory continuous Tail requires shadow preflight")
	}
	if c.SummaryTailEnabled && !c.ContinuousTailEnabled {
		return errors.New("agent contextMemory Summary + Tail requires continuous Tail")
	}
	if err := c.Summary.Validate(); err != nil {
		return err
	}
	if c.SummaryTailEnabled && !c.Summary.Enabled {
		return errors.New("agent contextMemory Summary + Tail requires the Summary model")
	}
	if c.SummaryTailEnabled && (c.SyncCompactionTimeoutMillis < 1000 || c.SyncCompactionTimeoutMillis > 300_000) {
		return errors.New("agent contextMemory syncCompactionTimeoutMillis must be between 1000 and 300000")
	}
	if c.AsyncCompactionEnabled {
		if !c.SummaryTailEnabled || !c.Summary.Enabled {
			return errors.New("agent contextMemory async compaction requires Summary + Tail")
		}
		if c.AsyncMaxAttempts < 1 || c.AsyncMaxAttempts > 10 {
			return errors.New("agent contextMemory asyncMaxAttempts must be between 1 and 10")
		}
		if math.IsNaN(c.RetryJitterRatio) || math.IsInf(c.RetryJitterRatio, 0) ||
			c.RetryJitterRatio < 0 || c.RetryJitterRatio > 0.50 {
			return errors.New("agent contextMemory retryJitterRatio must be between 0 and 0.50")
		}
	}
	if c.MemoryCacheEnabled {
		if !c.SummaryTailEnabled {
			return errors.New("agent contextMemory memory cache requires Summary + Tail")
		}
		if _, err := c.MemoryCacheDuration(); err != nil {
			return err
		}
		if math.IsNaN(c.MemoryCacheJitterRatio) || math.IsInf(c.MemoryCacheJitterRatio, 0) ||
			c.MemoryCacheJitterRatio < 0 || c.MemoryCacheJitterRatio > 0.50 {
			return errors.New("agent contextMemory memoryCacheJitterRatio must be between 0 and 0.50")
		}
		if c.MemoryCacheTimeoutMillis < 5 || c.MemoryCacheTimeoutMillis > 1_000 {
			return errors.New("agent contextMemory memoryCacheTimeoutMillis must be between 5 and 1000")
		}
	}
	if c.SourceRecoveryEnabled {
		if !c.SummaryTailEnabled || !c.Summary.Enabled {
			return errors.New("agent contextMemory source recovery requires Summary + Tail")
		}
		if c.SourceRecoveryMaxMessages < 1 || c.SourceRecoveryMaxMessages > 20 {
			return errors.New("agent contextMemory sourceRecoveryMaxMessages must be between 1 and 20")
		}
		if c.SourceRecoveryMaxTokens < 256 || c.SourceRecoveryMaxTokens > 8192 {
			return errors.New("agent contextMemory sourceRecoveryMaxTokens must be between 256 and 8192")
		}
		if c.SourceRecoveryMaxCalls < 1 || c.SourceRecoveryMaxCalls > 2 {
			return errors.New("agent contextMemory sourceRecoveryMaxCalls must be between 1 and 2")
		}
		if c.SourceRecoveryMaxTokens > c.ToolGrowthReserveTokens {
			return errors.New("agent contextMemory sourceRecoveryMaxTokens cannot exceed toolGrowthReserveTokens")
		}
	}
	if !c.ShadowPreflightEnabled && !c.DiagnosisPreflightEnabled {
		return nil
	}
	if c.ContinuousTailEnabled && !contextgovernance.ValidTailWindowRatio(c.TailMaxRatio) {
		return errors.New("agent contextMemory tailMaxRatio must satisfy 0 < tail <= 0.20")
	}
	if c.SummaryTailEnabled && (math.IsNaN(c.MemoryMaxRatio) || math.IsInf(c.MemoryMaxRatio, 0) ||
		math.IsNaN(c.SummaryMaxRatio) || math.IsInf(c.SummaryMaxRatio, 0) ||
		c.MemoryMaxRatio <= 0 || c.MemoryMaxRatio > contextgovernance.MaxTailWindowRatio ||
		c.SummaryMaxRatio <= 0 || c.SummaryMaxRatio > 0.05 ||
		c.SummaryMaxRatio+c.TailMaxRatio > c.MemoryMaxRatio+1e-12) {
		return errors.New("agent contextMemory ratios must satisfy Summary <= 0.05 and Summary + Tail <= Memory <= 0.20")
	}
	if c.PreflightTimeoutMillis < 5 || c.PreflightTimeoutMillis > 5_000 {
		return errors.New("agent contextMemory preflightTimeoutMillis must be between 5 and 5000")
	}
	if math.IsNaN(c.SoftThresholdRatio) || math.IsInf(c.SoftThresholdRatio, 0) ||
		math.IsNaN(c.HardThresholdRatio) || math.IsInf(c.HardThresholdRatio, 0) ||
		c.SoftThresholdRatio <= 0 || c.HardThresholdRatio <= c.SoftThresholdRatio ||
		c.HardThresholdRatio >= 1 {
		return errors.New("agent contextMemory thresholds must satisfy 0 < soft < hard < 1")
	}
	if c.ToolGrowthReserveTokens < 1 || c.ToolGrowthReserveTokens > 262_144 {
		return errors.New("agent contextMemory toolGrowthReserveTokens must be between 1 and 262144")
	}
	return nil
}

func (c AgentConfig) Validate() error {
	if strings.TrimSpace(c.SkillsDirectory) == "" {
		return errors.New("agent skillsDirectory is required")
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return errors.New("agent promptVersion is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.ConversationPromptVersion)) {
		return errors.New("agent conversationPromptVersion is invalid")
	}
	if c.ConversationCitationRepairEnabled &&
		!modelName.MatchString(strings.TrimSpace(c.ConversationCitationRepairPromptVersion)) {
		return errors.New("agent conversationCitationRepairPromptVersion is invalid")
	}
	for _, promptFile := range []struct {
		name string
		path string
	}{
		{name: "systemPromptFile", path: c.SystemPromptFile},
		{name: "baselinePromptFile", path: c.BaselinePromptFile},
		{name: "reportContractFile", path: c.ReportContractFile},
		{name: "conversationPromptFile", path: c.ConversationPromptFile},
	} {
		name := promptFile.name
		path := strings.TrimSpace(promptFile.path)
		if path == "" {
			return fmt.Errorf("agent %s is required", name)
		}
		if len(path) > 512 {
			return fmt.Errorf("agent %s must not exceed 512 characters", name)
		}
	}
	if c.ConversationCitationRepairEnabled {
		path := strings.TrimSpace(c.ConversationCitationRepairPromptFile)
		if path == "" || len(path) > 512 {
			return errors.New("agent conversationCitationRepairPromptFile is required and must not exceed 512 characters")
		}
		if c.ConversationCitationRepairTimeoutMillis < 1000 || c.ConversationCitationRepairTimeoutMillis > 60_000 {
			return errors.New("agent conversationCitationRepairTimeoutMillis must be between 1000 and 60000")
		}
		if c.ConversationCitationRepairMaxOutputTokens < 128 ||
			c.ConversationCitationRepairMaxOutputTokens > 2048 {
			return errors.New("agent conversationCitationRepairMaxOutputTokens must be between 128 and 2048")
		}
	}
	if c.MaxAgentRuns < 0 || c.MaxAgentRuns > 4 {
		return errors.New("agent maxAgentRuns must be between 1 and 4 when configured")
	}
	if c.MaxToolCalls < 0 || c.MaxToolCalls > 64 {
		return errors.New("agent maxToolCalls must be between 1 and 64 when configured")
	}
	if c.MaxEvidenceItems < 0 || c.MaxEvidenceItems > 128 {
		return errors.New("agent maxEvidenceItems must be between 1 and 128 when configured")
	}
	if c.MaxTotalTokens != 0 && (c.MaxTotalTokens < 1000 || c.MaxTotalTokens > 1_000_000) {
		return errors.New("agent maxTotalTokens must be between 1000 and 1000000 when configured")
	}
	if c.TimeoutMillis != 0 && (c.TimeoutMillis < 1000 || c.TimeoutMillis > 600_000) {
		return errors.New("agent timeoutMillis must be between 1000 and 600000 when configured")
	}
	if c.ConversationMaxIterations != 0 && (c.ConversationMaxIterations < 1 || c.ConversationMaxIterations > 16) {
		return errors.New("agent conversationMaxIterations must be between 1 and 16 when configured")
	}
	if c.ConversationMaxContextRunes != 0 &&
		(c.ConversationMaxContextRunes < 20_000 || c.ConversationMaxContextRunes > 200_000) {
		return errors.New("agent conversationMaxContextRunes must be between 20000 and 200000 when configured")
	}
	if c.ConversationTimeoutMillis != 0 &&
		(c.ConversationTimeoutMillis < 1000 || c.ConversationTimeoutMillis > 300_000) {
		return errors.New("agent conversationTimeoutMillis must be between 1000 and 300000 when configured")
	}
	if err := c.ContextMemory.Validate(); err != nil {
		return err
	}
	if c.ContextMemory.SummaryTailEnabled && c.ConversationTimeoutMillis > 0 &&
		c.ContextMemory.SyncCompactionTimeoutMillis >= c.ConversationTimeoutMillis {
		return errors.New("agent contextMemory sync compaction timeout must leave time for the conversation answer")
	}
	return nil
}

// GitHubMCPConfig 描述官方 GitHub MCP 的连接和只读能力边界。
// 具体 owner/repository/ref 由 GitHub Token/App 的实际权限和本次 Tool 参数决定，
// 不在应用配置中重复维护逐仓库 ACL。PAT 通过 tokenEnv 指向的环境变量读取，
// 不能写入 TOML 或日志。
type GitHubMCPConfig struct {
	Enabled       bool   `toml:"enabled"`
	Endpoint      string `toml:"endpoint"`
	TokenEnv      string `toml:"tokenEnv"`
	TimeoutMillis int    `toml:"timeoutMillis"`
}

var (
	environmentVariableName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)
	publicDomainName        = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

func (c GitHubMCPConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return errors.New("githubMCP endpoint must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("githubMCP endpoint must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.TokenEnv)) {
		return errors.New("githubMCP tokenEnv is invalid")
	}
	if c.TimeoutMillis <= 0 || c.TimeoutMillis > 120_000 {
		return errors.New("githubMCP timeoutMillis must be between 1 and 120000")
	}
	return nil
}

func (c GitHubMCPConfig) Token() (string, error) {
	return requiredEnv(c.TokenEnv)
}

// WebSearchProviderConfig 描述一个公开 Web Provider 的连接信息。
// API Key 只能通过 apiKeyEnv 引用，不得出现在 TOML、日志或 Tool 输出中。
type WebSearchProviderConfig struct {
	BaseURL   string `toml:"baseURL"`
	APIKeyEnv string `toml:"apiKeyEnv"`
}

// WebSearchConfig 描述公开网页检索的供应商连接与硬预算。
// Search 与 Content 可以独立选择 Provider，例如 searxng + direct。
type WebSearchConfig struct {
	Enabled          bool                               `toml:"enabled"`
	SearchProvider   string                             `toml:"searchProvider"`
	ContentProvider  string                             `toml:"contentProvider"`
	Providers        map[string]WebSearchProviderConfig `toml:"providers"`
	Provider         string                             `toml:"provider"` // 兼容旧版单 Provider 配置
	BaseURL          string                             `toml:"baseURL"`
	APIKeyEnv        string                             `toml:"apiKeyEnv"`
	TimeoutMillis    int                                `toml:"timeoutMillis"`
	MaxResults       int                                `toml:"maxResults"`
	MaxFetchedPages  int                                `toml:"maxFetchedPages"`
	MaxPageChars     int                                `toml:"maxPageChars"`
	MaxRounds        int                                `toml:"maxRounds"`
	MaxResponseBytes int64                              `toml:"maxResponseBytes"`
	OfficialDomains  []string                           `toml:"officialDomains"`
	TrustedDomains   []string                           `toml:"trustedDomains"`
	Redaction        WebSearchRedactionConfig           `toml:"redaction"`
}

type WebSearchRedactionConfig struct {
	MaxInputRunes     int    `toml:"maxInputRunes"`
	MaxOutputRunes    int    `toml:"maxOutputRunes"`
	MinOutputRunes    int    `toml:"minOutputRunes"`
	SensitiveTermsEnv string `toml:"sensitiveTermsEnv"`
}

func (c WebSearchRedactionConfig) Validate() error {
	if c.MaxInputRunes < 64 || c.MaxInputRunes > 4096 {
		return errors.New("webSearch redaction maxInputRunes must be between 64 and 4096")
	}
	if c.MaxOutputRunes < 32 || c.MaxOutputRunes > c.MaxInputRunes {
		return errors.New("webSearch redaction maxOutputRunes must be between 32 and maxInputRunes")
	}
	if c.MinOutputRunes < 4 || c.MinOutputRunes > c.MaxOutputRunes {
		return errors.New("webSearch redaction minOutputRunes must be between 4 and maxOutputRunes")
	}
	if c.SensitiveTermsEnv != "" && !environmentVariableName.MatchString(strings.TrimSpace(c.SensitiveTermsEnv)) {
		return errors.New("webSearch redaction sensitiveTermsEnv is invalid")
	}
	return nil
}

func (c WebSearchRedactionConfig) SensitiveTerms() ([]string, error) {
	if strings.TrimSpace(c.SensitiveTermsEnv) == "" {
		return nil, nil
	}
	value, err := requiredEnv(c.SensitiveTermsEnv)
	if err != nil {
		return nil, err
	}
	values := strings.FieldsFunc(value, func(current rune) bool {
		return current == ',' || current == '\r' || current == '\n'
	})
	result := make([]string, 0, len(values))
	for _, item := range values {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, nil
}

func (c WebSearchConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	searchProvider, contentProvider := c.EffectiveProviders()
	if err := validateWebProviderName(searchProvider, true); err != nil {
		return fmt.Errorf("webSearch searchProvider: %w", err)
	}
	if err := validateWebProviderName(contentProvider, false); err != nil {
		return fmt.Errorf("webSearch contentProvider: %w", err)
	}
	for _, provider := range []string{searchProvider, contentProvider} {
		providerConfig := c.ProviderConfig(provider)
		if provider == "direct" {
			if strings.TrimSpace(providerConfig.APIKeyEnv) != "" {
				return errors.New("webSearch direct provider must not configure apiKeyEnv")
			}
			continue
		}
		endpoint, err := url.Parse(strings.TrimSpace(providerConfig.BaseURL))
		if err != nil || endpoint.Host == "" {
			return fmt.Errorf("webSearch %s baseURL must be an absolute URL", provider)
		}
		if endpoint.Scheme != "https" && !isLocalWebProviderEndpoint(endpoint.Hostname()) {
			return fmt.Errorf("webSearch %s baseURL must use HTTPS unless it points to a local endpoint", provider)
		}
		if provider == "firecrawl" && !environmentVariableName.MatchString(strings.TrimSpace(providerConfig.APIKeyEnv)) {
			return errors.New("webSearch firecrawl apiKeyEnv is invalid")
		}
		if provider != "firecrawl" && strings.TrimSpace(providerConfig.APIKeyEnv) != "" && !environmentVariableName.MatchString(strings.TrimSpace(providerConfig.APIKeyEnv)) {
			return fmt.Errorf("webSearch %s apiKeyEnv is invalid", provider)
		}
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 120_000 {
		return errors.New("webSearch timeoutMillis must be between 1000 and 120000")
	}
	if c.MaxResults < 1 || c.MaxResults > 20 {
		return errors.New("webSearch maxResults must be between 1 and 20")
	}
	if c.MaxFetchedPages < 1 || c.MaxFetchedPages > c.MaxResults {
		return errors.New("webSearch maxFetchedPages must be between 1 and maxResults")
	}
	if c.MaxPageChars < 1_000 || c.MaxPageChars > 100_000 {
		return errors.New("webSearch maxPageChars must be between 1000 and 100000")
	}
	if c.MaxRounds < 1 || c.MaxRounds > 4 {
		return errors.New("webSearch maxRounds must be between 1 and 4")
	}
	if c.MaxResponseBytes < 64*1024 || c.MaxResponseBytes > 10*1024*1024 {
		return errors.New("webSearch maxResponseBytes must be between 65536 and 10485760")
	}
	if err := validateWebSearchDomains(c.OfficialDomains, c.TrustedDomains); err != nil {
		return err
	}
	if err := c.Redaction.Validate(); err != nil {
		return err
	}
	return nil
}

func validateWebSearchDomains(official, trusted []string) error {
	if len(official) > 128 || len(trusted) > 128 {
		return errors.New("webSearch source domain lists cannot exceed 128 entries")
	}
	seen := make(map[string]string, len(official)+len(trusted))
	for tier, values := range map[string][]string{"officialDomains": official, "trustedDomains": trusted} {
		for _, raw := range values {
			domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
			if !publicDomainName.MatchString(domain) || net.ParseIP(domain) != nil {
				return fmt.Errorf("webSearch %s contains invalid domain %q", tier, raw)
			}
			if previous, exists := seen[domain]; exists {
				return fmt.Errorf("webSearch domain %q is duplicated across %s and %s", domain, previous, tier)
			}
			seen[domain] = tier
		}
	}
	for _, officialDomain := range official {
		officialDomain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(officialDomain), "."))
		for _, trustedDomain := range trusted {
			trustedDomain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(trustedDomain), "."))
			if strings.HasSuffix(officialDomain, "."+trustedDomain) || strings.HasSuffix(trustedDomain, "."+officialDomain) {
				return fmt.Errorf("webSearch source tiers overlap at %q and %q", officialDomain, trustedDomain)
			}
		}
	}
	return nil
}

func (c WebSearchConfig) APIKey() (string, error) {
	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	if provider == "" {
		provider, _ = c.EffectiveProviders()
	}
	return c.APIKeyFor(provider)
}

// EffectiveProviders returns the split configuration and keeps old
// provider=firecrawl files working during migration.
func (c WebSearchConfig) EffectiveProviders() (string, string) {
	searchProvider := strings.ToLower(strings.TrimSpace(c.SearchProvider))
	contentProvider := strings.ToLower(strings.TrimSpace(c.ContentProvider))
	legacy := strings.ToLower(strings.TrimSpace(c.Provider))
	if searchProvider == "" {
		searchProvider = legacy
	}
	if contentProvider == "" {
		if legacy != "" {
			contentProvider = legacy
		} else {
			contentProvider = "direct"
		}
	}
	return searchProvider, contentProvider
}

func (c WebSearchConfig) ProviderConfig(name string) WebSearchProviderConfig {
	name = strings.ToLower(strings.TrimSpace(name))
	for configuredName, providerConfig := range c.Providers {
		if strings.ToLower(strings.TrimSpace(configuredName)) == name {
			return providerConfig
		}
	}
	return WebSearchProviderConfig{BaseURL: c.BaseURL, APIKeyEnv: c.APIKeyEnv}
}

func (c WebSearchConfig) APIKeyFor(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "direct" || provider == "searxng" {
		return "", nil
	}
	providerConfig := c.ProviderConfig(provider)
	if strings.TrimSpace(providerConfig.APIKeyEnv) == "" {
		return "", errors.New("webSearch provider apiKeyEnv is required")
	}
	return requiredEnv(providerConfig.APIKeyEnv)
}

func validateWebProviderName(provider string, search bool) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "firecrawl":
		return nil
	case "searxng":
		if !search {
			return errors.New("searxng is a search provider and cannot fetch page content")
		}
		return nil
	case "direct":
		if search {
			return errors.New("direct is a content provider and cannot search")
		}
		return nil
	default:
		return fmt.Errorf("unsupported provider %q", provider)
	}
}

func isLocalWebProviderEndpoint(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || !strings.Contains(hostname, ".")
}

// MinIOConfig 描述附件和知识原文使用的 S3 兼容对象存储。
// MinIO 是可降级依赖；数据库仍保存对象引用、哈希和版本事实。
type MinIOConfig struct {
	Enabled               bool   `toml:"enabled"`
	Endpoint              string `toml:"endpoint"`
	AccessKeyEnv          string `toml:"accessKeyEnv"`
	SecretKeyEnv          string `toml:"secretKeyEnv"`
	UseTLS                bool   `toml:"useTLS"`
	Region                string `toml:"region"`
	AttachmentBucket      string `toml:"attachmentBucket"`
	KnowledgeSourceBucket string `toml:"knowledgeSourceBucket"`
	AutoCreateBuckets     bool   `toml:"autoCreateBuckets"`
	TimeoutMillis         int    `toml:"timeoutMillis"`
	MaxObjectBytes        int64  `toml:"maxObjectBytes"`
}

var storageBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (c MinIOConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	endpoint := strings.TrimSpace(c.Endpoint)
	if strings.Contains(endpoint, "://") {
		return errors.New("minio endpoint must be host:port without a scheme")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(host) == "" || port == "" {
		return errors.New("minio endpoint must be a host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("minio endpoint port is invalid")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.AccessKeyEnv)) {
		return errors.New("minio accessKeyEnv is invalid")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.SecretKeyEnv)) {
		return errors.New("minio secretKeyEnv is invalid")
	}
	if strings.TrimSpace(c.Region) == "" || len([]rune(c.Region)) > 64 {
		return errors.New("minio region is required and must not exceed 64 characters")
	}
	for name, bucket := range map[string]string{
		"attachmentBucket":      c.AttachmentBucket,
		"knowledgeSourceBucket": c.KnowledgeSourceBucket,
	} {
		bucket = strings.TrimSpace(bucket)
		if !storageBucketName.MatchString(bucket) || strings.Contains(bucket, "..") ||
			strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") || net.ParseIP(bucket) != nil {
			return fmt.Errorf("minio %s is invalid", name)
		}
	}
	if strings.EqualFold(strings.TrimSpace(c.AttachmentBucket), strings.TrimSpace(c.KnowledgeSourceBucket)) {
		return errors.New("minio attachmentBucket and knowledgeSourceBucket must differ")
	}
	if c.TimeoutMillis < 1_000 || c.TimeoutMillis > 120_000 {
		return errors.New("minio timeoutMillis must be between 1000 and 120000")
	}
	const maxConfiguredObjectBytes = 50 * 1024 * 1024
	if c.MaxObjectBytes < 1 || c.MaxObjectBytes > maxConfiguredObjectBytes {
		return errors.New("minio maxObjectBytes must be between 1 and 52428800")
	}
	return nil
}

func (c MinIOConfig) AccessKey() (string, error) {
	return requiredEnv(c.AccessKeyEnv)
}

func (c MinIOConfig) SecretKey() (string, error) {
	return requiredEnv(c.SecretKeyEnv)
}

type KnowledgeRetrievalConfig struct {
	ContextExpansionEnabled bool                              `toml:"contextExpansionEnabled"`
	ContextWindow           int                               `toml:"contextWindow"`
	ContextMaxRunes         int                               `toml:"contextMaxRunes"`
	ContextCompression      KnowledgeContextCompressionConfig `toml:"contextCompression"`
	QueryRewrite            KnowledgeQueryRewriteConfig       `toml:"queryRewrite"`
}

func (c KnowledgeRetrievalConfig) Validate() error {
	if c.ContextExpansionEnabled {
		if c.ContextWindow < 1 || c.ContextWindow > 3 {
			return errors.New("knowledge retrieval contextWindow must be between 1 and 3")
		}
		if c.ContextMaxRunes < 128 || c.ContextMaxRunes > 8000 {
			return errors.New("knowledge retrieval contextMaxRunes must be between 128 and 8000")
		}
	}
	if c.ContextCompression.Enabled && !c.ContextExpansionEnabled {
		return errors.New("knowledge retrieval contextCompression requires context expansion")
	}
	if err := c.ContextCompression.Validate(); err != nil {
		return err
	}
	if err := c.QueryRewrite.Validate(); err != nil {
		return err
	}
	return nil
}

type KnowledgeContextCompressionConfig struct {
	Enabled   bool    `toml:"enabled"`
	MaxChunks int     `toml:"maxChunks"`
	MaxRunes  int     `toml:"maxRunes"`
	MinScore  float64 `toml:"minScore"`
}

func (c KnowledgeContextCompressionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxChunks < 1 || c.MaxChunks > 40 {
		return errors.New("knowledge retrieval contextCompression.maxChunks must be between 1 and 40")
	}
	if c.MaxRunes < 128 || c.MaxRunes > 32000 {
		return errors.New("knowledge retrieval contextCompression.maxRunes must be between 128 and 32000")
	}
	if math.IsNaN(c.MinScore) || math.IsInf(c.MinScore, 0) || c.MinScore < 0 || c.MinScore > 1 {
		return errors.New("knowledge retrieval contextCompression.minScore must be between 0 and 1")
	}
	return nil
}

type KnowledgeQueryRewriteConfig struct {
	Enabled        bool   `toml:"enabled"`
	ModelProfile   string `toml:"modelProfile"`
	PromptFile     string `toml:"promptFile"`
	PromptVersion  string `toml:"promptVersion"`
	TimeoutMillis  int    `toml:"timeoutMillis"`
	MaxSubqueries  int    `toml:"maxSubqueries"`
	MaxOutputRunes int    `toml:"maxOutputRunes"`
}

func (c KnowledgeQueryRewriteConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if !modelName.MatchString(strings.TrimSpace(c.ModelProfile)) {
		return errors.New("knowledge query rewrite modelProfile is invalid")
	}
	promptFile := strings.TrimSpace(c.PromptFile)
	if promptFile == "" || len(promptFile) > 512 {
		return errors.New("knowledge query rewrite promptFile must be between 1 and 512 characters")
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return errors.New("knowledge query rewrite promptVersion is invalid")
	}
	if c.TimeoutMillis < 1000 || c.TimeoutMillis > 30000 {
		return errors.New("knowledge query rewrite timeoutMillis must be between 1000 and 30000")
	}
	if c.MaxSubqueries < 0 || c.MaxSubqueries > 2 {
		return errors.New("knowledge query rewrite maxSubqueries must be between 0 and 2")
	}
	if c.MaxOutputRunes < 128 || c.MaxOutputRunes > 4096 {
		return errors.New("knowledge query rewrite maxOutputRunes must be between 128 and 4096")
	}
	return nil
}

func (c KnowledgeQueryRewriteConfig) LoadPrompt() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if !c.Enabled {
		return "", errors.New("knowledge query rewrite is disabled")
	}
	return loadPromptFile("knowledge.retrieval.queryRewrite", "prompt", c.PromptFile, 16*1024)
}

// KnowledgeConfig 固定知识入库任务的可追踪流水线版本和重试上限。
type KnowledgeConfig struct {
	PipelineVersion             string                   `toml:"pipelineVersion"`
	MaxAttempts                 int                      `toml:"maxAttempts"`
	MaxUploadBytes              int64                    `toml:"maxUploadBytes"`
	ChunkMaxRunes               int                      `toml:"chunkMaxRunes"`
	ChunkOverlapRunes           int                      `toml:"chunkOverlapRunes"`
	ChunkWriteBatchSize         int                      `toml:"chunkWriteBatchSize"`
	ParserMaxDocumentUnits      int                      `toml:"parserMaxDocumentUnits"`
	ParserMaxArchiveEntries     int                      `toml:"parserMaxArchiveEntries"`
	ParserMaxExpandedBytes      int64                    `toml:"parserMaxExpandedBytes"`
	ParserMaxXMLBytes           int64                    `toml:"parserMaxXMLBytes"`
	ParserMaxExtractedRunes     int                      `toml:"parserMaxExtractedRunes"`
	ParserMaxSpreadsheetRows    int                      `toml:"parserMaxSpreadsheetRows"`
	ParserMaxSpreadsheetColumns int                      `toml:"parserMaxSpreadsheetColumns"`
	ParserMaxVisualAssets       int                      `toml:"parserMaxVisualAssets"`
	ParserMaxVisualAssetBytes   int64                    `toml:"parserMaxVisualAssetBytes"`
	ParserMaxTotalVisualBytes   int64                    `toml:"parserMaxTotalVisualBytes"`
	MaxVisualEnrichments        int                      `toml:"maxVisualEnrichments"`
	MinVisualPixels             int64                    `toml:"minVisualPixels"`
	Layout                      KnowledgeLayoutConfig    `toml:"layout"`
	Retrieval                   KnowledgeRetrievalConfig `toml:"retrieval"`
}

func (c KnowledgeConfig) Validate() error {
	if !modelName.MatchString(strings.TrimSpace(c.PipelineVersion)) {
		return errors.New("knowledge pipelineVersion is invalid")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 10 {
		return errors.New("knowledge maxAttempts must be between 1 and 10")
	}
	const maxConfiguredUploadBytes = 50 * 1024 * 1024
	if c.MaxUploadBytes < 1 || c.MaxUploadBytes > maxConfiguredUploadBytes {
		return errors.New("knowledge maxUploadBytes must be between 1 and 52428800")
	}
	if c.ChunkMaxRunes < 128 || c.ChunkMaxRunes > 8000 {
		return errors.New("knowledge chunkMaxRunes must be between 128 and 8000")
	}
	if c.ChunkOverlapRunes < 0 || c.ChunkOverlapRunes >= c.ChunkMaxRunes/2 {
		return errors.New("knowledge chunkOverlapRunes must be non-negative and less than half chunkMaxRunes")
	}
	if c.ChunkWriteBatchSize < 1 || c.ChunkWriteBatchSize > 500 {
		return errors.New("knowledge chunkWriteBatchSize must be between 1 and 500")
	}
	if c.ParserMaxDocumentUnits < 1 || c.ParserMaxDocumentUnits > 5000 {
		return errors.New("knowledge parserMaxDocumentUnits must be between 1 and 5000")
	}
	if c.ParserMaxArchiveEntries < 1 || c.ParserMaxArchiveEntries > 20000 {
		return errors.New("knowledge parserMaxArchiveEntries must be between 1 and 20000")
	}
	if c.ParserMaxExpandedBytes < 1024*1024 || c.ParserMaxExpandedBytes > 1024*1024*1024 {
		return errors.New("knowledge parserMaxExpandedBytes must be between 1048576 and 1073741824")
	}
	if c.ParserMaxXMLBytes < 64*1024 || c.ParserMaxXMLBytes > c.ParserMaxExpandedBytes {
		return errors.New("knowledge parserMaxXMLBytes must be between 65536 and parserMaxExpandedBytes")
	}
	if c.ParserMaxExtractedRunes < 1000 || c.ParserMaxExtractedRunes > 10_000_000 {
		return errors.New("knowledge parserMaxExtractedRunes must be between 1000 and 10000000")
	}
	if c.ParserMaxSpreadsheetRows < 1 || c.ParserMaxSpreadsheetRows > 1_000_000 {
		return errors.New("knowledge parserMaxSpreadsheetRows must be between 1 and 1000000")
	}
	if c.ParserMaxSpreadsheetColumns < 1 || c.ParserMaxSpreadsheetColumns > 16384 {
		return errors.New("knowledge parserMaxSpreadsheetColumns must be between 1 and 16384")
	}
	if c.ParserMaxVisualAssets < 1 || c.ParserMaxVisualAssets > 10_000 {
		return errors.New("knowledge parserMaxVisualAssets must be between 1 and 10000")
	}
	if c.ParserMaxVisualAssetBytes < 1024 || c.ParserMaxVisualAssetBytes > c.ParserMaxExpandedBytes {
		return errors.New("knowledge parserMaxVisualAssetBytes must be between 1024 and parserMaxExpandedBytes")
	}
	if c.ParserMaxTotalVisualBytes < c.ParserMaxVisualAssetBytes ||
		c.ParserMaxTotalVisualBytes > c.ParserMaxExpandedBytes {
		return errors.New("knowledge parserMaxTotalVisualBytes must be between parserMaxVisualAssetBytes and parserMaxExpandedBytes")
	}
	if c.MaxVisualEnrichments < 1 || c.MaxVisualEnrichments > 100 {
		return errors.New("knowledge maxVisualEnrichments must be between 1 and 100")
	}
	if c.MinVisualPixels < 1 || c.MinVisualPixels > 100_000_000 {
		return errors.New("knowledge minVisualPixels must be between 1 and 100000000")
	}
	if err := c.Layout.Validate(); err != nil {
		return err
	}
	if err := c.Retrieval.Validate(); err != nil {
		return err
	}
	return nil
}

// RabbitMQConfig 固定 M1 诊断队列拓扑和 Relay 的有界批处理参数。
// AMQP URL 可能包含凭证，只能通过 URLEnv 指向的环境变量注入。
type RabbitMQConfig struct {
	Enabled                      bool   `toml:"enabled"`
	URLEnv                       string `toml:"urlEnv"`
	Exchange                     string `toml:"exchange"`
	DiagnosisQueue               string `toml:"diagnosisQueue"`
	DiagnosisRoutingKey          string `toml:"diagnosisRoutingKey"`
	ConversationQueue            string `toml:"conversationQueue"`
	ConversationRoutingKey       string `toml:"conversationRoutingKey"`
	MemoryCompactionQueue        string `toml:"memoryCompactionQueue"`
	MemoryCompactionRoutingKey   string `toml:"memoryCompactionRoutingKey"`
	KnowledgeIngestionQueue      string `toml:"knowledgeIngestionQueue"`
	KnowledgeIngestionRoutingKey string `toml:"knowledgeIngestionRoutingKey"`
	RelayBatchSize               int    `toml:"relayBatchSize"`
	RelayPollIntervalMillis      int    `toml:"relayPollIntervalMillis"`
	RelayLeaseMillis             int    `toml:"relayLeaseMillis"`
	PublishConfirmTimeoutMillis  int    `toml:"publishConfirmTimeoutMillis"`
	WorkerLeaseMillis            int    `toml:"workerLeaseMillis"`
	WorkerRenewIntervalMillis    int    `toml:"workerRenewIntervalMillis"`
	WorkerMaxAttempts            int    `toml:"workerMaxAttempts"`
}

var amqpEntityName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (c RabbitMQConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.URLEnv)) {
		return errors.New("rabbitmq urlEnv is invalid")
	}
	for name, value := range map[string]string{
		"exchange": c.Exchange, "diagnosisQueue": c.DiagnosisQueue,
		"diagnosisRoutingKey":          c.DiagnosisRoutingKey,
		"conversationQueue":            c.ConversationQueue,
		"conversationRoutingKey":       c.ConversationRoutingKey,
		"memoryCompactionQueue":        c.MemoryCompactionQueue,
		"memoryCompactionRoutingKey":   c.MemoryCompactionRoutingKey,
		"knowledgeIngestionQueue":      c.KnowledgeIngestionQueue,
		"knowledgeIngestionRoutingKey": c.KnowledgeIngestionRoutingKey,
	} {
		if !amqpEntityName.MatchString(strings.TrimSpace(value)) {
			return fmt.Errorf("rabbitmq %s is invalid", name)
		}
	}
	if c.RelayBatchSize < 1 || c.RelayBatchSize > 100 {
		return errors.New("rabbitmq relayBatchSize must be between 1 and 100")
	}
	if c.RelayPollIntervalMillis < 100 || c.RelayPollIntervalMillis > 60_000 {
		return errors.New("rabbitmq relayPollIntervalMillis must be between 100 and 60000")
	}
	if c.PublishConfirmTimeoutMillis < 100 || c.PublishConfirmTimeoutMillis > 60_000 {
		return errors.New("rabbitmq publishConfirmTimeoutMillis must be between 100 and 60000")
	}
	if c.RelayLeaseMillis <= c.RelayBatchSize*c.PublishConfirmTimeoutMillis || c.RelayLeaseMillis > 600_000 {
		return errors.New("rabbitmq relayLeaseMillis must exceed batchSize * publishConfirmTimeoutMillis and be at most 600000")
	}
	if c.WorkerLeaseMillis < 30_000 || c.WorkerLeaseMillis > 600_000 {
		return errors.New("rabbitmq workerLeaseMillis must be between 30000 and 600000")
	}
	if c.WorkerRenewIntervalMillis < 1000 || c.WorkerRenewIntervalMillis*2 >= c.WorkerLeaseMillis {
		return errors.New("rabbitmq workerRenewIntervalMillis must be at least 1000 and less than half workerLeaseMillis")
	}
	if c.WorkerMaxAttempts < 1 || c.WorkerMaxAttempts > 10 {
		return errors.New("rabbitmq workerMaxAttempts must be between 1 and 10")
	}
	return nil
}

func (c RabbitMQConfig) URL() (string, error) {
	raw, err := requiredEnv(c.URLEnv)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "amqp" && parsed.Scheme != "amqps") {
		return "", errors.New("rabbitmq URL must be an absolute amqp or amqps URL")
	}
	return raw, nil
}

// SQLServerConfig 定义公司 ERP 工单库的只读连接和安全映射。
// relation/fields 只接受标识符，不接受任意 SQL 片段。
type SQLServerConfig struct {
	Enabled                bool                         `toml:"enabled"`
	ID                     string                       `toml:"id"`
	Code                   string                       `toml:"code"`
	Name                   string                       `toml:"name"`
	Environment            string                       `toml:"environment"`
	Host                   string                       `toml:"host"`
	Port                   int                          `toml:"port"`
	User                   string                       `toml:"user"`
	Database               string                       `toml:"database"`
	PasswordEnv            string                       `toml:"passwordEnv"`
	Encrypt                string                       `toml:"encrypt"`
	TrustServerCertificate bool                         `toml:"trustServerCertificate"`
	MaxIdle                int                          `toml:"maxIdleConns"`
	MaxOpen                int                          `toml:"maxOpenConns"`
	QueryTimeoutMillis     int                          `toml:"queryTimeoutMillis"`
	MaxTextBytes           int                          `toml:"maxTextBytes"`
	MaxResultBytes         int                          `toml:"maxResultBytes"`
	CaseMapping            SQLServerCaseMapping         `toml:"caseMapping"`
	AttachmentMapping      SQLServerObjectMapping       `toml:"attachmentMapping"`
	Investigation          SQLServerInvestigationConfig `toml:"investigation"`
}

type SQLServerCaseMapping struct {
	Relation       string            `toml:"relation"`
	Fields         map[string]string `toml:"fields"`
	Attributes     map[string]string `toml:"attributes"`
	StatusValues   map[string]string `toml:"statusValues"`
	PriorityValues map[string]string `toml:"priorityValues"`
}

type SQLServerObjectMapping struct {
	Relation string            `toml:"relation"`
	Fields   map[string]string `toml:"fields"`
}

type SQLServerInvestigationConfig struct {
	AllowedSchemas       []string `toml:"allowedSchemas"`
	MaxQueryBytes        int      `toml:"maxQueryBytes"`
	MaxRows              int      `toml:"maxRows"`
	MaxResultBytes       int      `toml:"maxResultBytes"`
	MaxConcurrentQueries int      `toml:"maxConcurrentQueries"`
}

// Password 从显式声明的环境变量读取 SQL Server 密码。
func (c SQLServerConfig) Password() (string, error) {
	return requiredEnv(c.PasswordEnv)
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var jsonAttributeName = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)

func validateRelation(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) != 2 {
		return false
	}
	return sqlIdentifier.MatchString(parts[0]) && sqlIdentifier.MatchString(parts[1])
}

func validateMapping(name string, mapping SQLServerObjectMapping, required []string, allowed map[string]struct{}) error {
	if !validateRelation(strings.TrimSpace(mapping.Relation)) {
		return fmt.Errorf("sqlserver %s relation must be schema.object", name)
	}
	for canonical, source := range mapping.Fields {
		if _, ok := allowed[canonical]; !ok {
			return fmt.Errorf("sqlserver %s contains unsupported field %q", name, canonical)
		}
		if !sqlIdentifier.MatchString(strings.TrimSpace(source)) {
			return fmt.Errorf("sqlserver %s field %q has invalid source identifier", name, canonical)
		}
	}
	for _, field := range required {
		if strings.TrimSpace(mapping.Fields[field]) == "" {
			return fmt.Errorf("sqlserver %s field %q is required", name, field)
		}
	}
	return nil
}

func (c SQLServerConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return errors.New("sqlserver id must be a UUID")
	}
	if strings.TrimSpace(c.Code) == "" || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Environment) == "" {
		return errors.New("sqlserver code, name, and environment are required")
	}
	if strings.TrimSpace(c.Host) == "" || c.Port <= 0 || strings.TrimSpace(c.User) == "" ||
		strings.TrimSpace(c.Database) == "" || strings.TrimSpace(c.PasswordEnv) == "" {
		return errors.New("sqlserver host, port, user, database, and passwordEnv are required")
	}
	if c.MaxIdle < 0 || c.MaxOpen <= 0 || c.MaxIdle > c.MaxOpen {
		return errors.New("sqlserver connection pool limits are invalid")
	}
	if c.QueryTimeoutMillis <= 0 || c.MaxTextBytes <= 0 || c.MaxResultBytes <= 0 {
		return errors.New("sqlserver queryTimeoutMillis, maxTextBytes, and maxResultBytes must be positive")
	}
	if c.MaxTextBytes > c.MaxResultBytes {
		return errors.New("sqlserver maxTextBytes cannot exceed maxResultBytes")
	}
	switch strings.ToLower(strings.TrimSpace(c.Encrypt)) {
	case "disable", "false", "optional", "true", "mandatory", "strict":
	default:
		return errors.New("sqlserver encrypt mode is invalid")
	}
	allowedCaseFields := map[string]struct{}{
		"externalCaseKey": {}, "caseType": {}, "title": {}, "description": {},
		"category": {}, "module": {}, "status": {}, "priority": {},
		"occurredAt": {}, "reportedAt": {}, "sourceUpdatedAt": {},
		"customerCode": {}, "customerName": {}, "productCode": {},
		"productName": {}, "productVersion": {}, "workOrderNo": {},
		"workpieceNo": {}, "materialCode": {}, "batchNo": {}, "serialNo": {},
		"factoryCode": {}, "workshopCode": {}, "productionLineCode": {},
		"workstationCode": {}, "equipmentCode": {}, "sourceSystem": {},
		"deploymentEnvironment": {}, "businessDatabaseAlias": {},
	}
	caseMapping := SQLServerObjectMapping{Relation: c.CaseMapping.Relation, Fields: c.CaseMapping.Fields}
	if err := validateMapping("caseMapping", caseMapping,
		[]string{"externalCaseKey", "title", "description", "status", "reportedAt", "sourceUpdatedAt"},
		allowedCaseFields,
	); err != nil {
		return err
	}
	for attribute, source := range c.CaseMapping.Attributes {
		if !jsonAttributeName.MatchString(attribute) {
			return fmt.Errorf("sqlserver caseMapping attribute %q is invalid", attribute)
		}
		if _, reserved := allowedCaseFields[attribute]; reserved {
			return fmt.Errorf("sqlserver caseMapping attribute %q duplicates a standard field", attribute)
		}
		if !sqlIdentifier.MatchString(strings.TrimSpace(source)) {
			return fmt.Errorf("sqlserver caseMapping attribute %q has invalid source identifier", attribute)
		}
	}
	allowedAttachmentFields := map[string]struct{}{
		"externalCaseKey": {}, "externalAttachmentKey": {}, "fileName": {},
		"mediaType": {}, "sizeBytes": {}, "objectKey": {}, "contentHash": {},
		"sourceUpdatedAt": {},
	}
	if err := validateMapping("attachmentMapping", c.AttachmentMapping,
		[]string{"externalCaseKey", "externalAttachmentKey", "fileName", "mediaType", "sizeBytes", "objectKey", "contentHash", "sourceUpdatedAt"},
		allowedAttachmentFields,
	); err != nil {
		return err
	}
	for raw, normalized := range c.CaseMapping.StatusValues {
		if strings.TrimSpace(raw) == "" || !contains([]string{"open", "processing", "closed"}, normalized) {
			return fmt.Errorf("sqlserver status mapping %q is invalid", raw)
		}
	}
	for raw, normalized := range c.CaseMapping.PriorityValues {
		if strings.TrimSpace(raw) == "" || !contains([]string{"high", "medium", "low"}, normalized) {
			return fmt.Errorf("sqlserver priority mapping %q is invalid", raw)
		}
	}
	seenSchemas := make(map[string]struct{}, len(c.Investigation.AllowedSchemas))
	for _, schema := range c.Investigation.AllowedSchemas {
		if schema != strings.TrimSpace(schema) || !sqlIdentifier.MatchString(schema) {
			return fmt.Errorf("sqlserver investigation schema %q is invalid", schema)
		}
		if _, exists := seenSchemas[schema]; exists {
			return fmt.Errorf("sqlserver investigation schema %q is duplicated", schema)
		}
		seenSchemas[schema] = struct{}{}
	}
	if c.Investigation.MaxQueryBytes < 1024 || c.Investigation.MaxQueryBytes > 64*1024 {
		return errors.New("sqlserver investigation maxQueryBytes must be between 1024 and 65536")
	}
	if c.Investigation.MaxRows < 1 || c.Investigation.MaxRows > 10000 {
		return errors.New("sqlserver investigation maxRows must be between 1 and 10000")
	}
	if c.Investigation.MaxResultBytes < 1024 || c.Investigation.MaxResultBytes > 4*1024*1024 {
		return errors.New("sqlserver investigation maxResultBytes must be between 1024 and 4194304")
	}
	if c.Investigation.MaxConcurrentQueries < 1 || c.Investigation.MaxConcurrentQueries > 32 {
		return errors.New("sqlserver investigation maxConcurrentQueries must be between 1 and 32")
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// AuthConfig 定义本地账号认证的 Session 时长和浏览器安全边界。
type AuthConfig struct {
	AllowedOrigins         []string `toml:"allowedOrigins"`
	CookieDomain           string   `toml:"cookieDomain"`
	CookieSecure           bool     `toml:"cookieSecure"`
	SessionIdleMinutes     int      `toml:"sessionIdleMinutes"`
	SessionAbsoluteMinutes int      `toml:"sessionAbsoluteMinutes"`
}

// Validate 检查 Session 时长和可信 Origin。
func (c AuthConfig) Validate() error {
	if c.SessionIdleMinutes <= 0 || c.SessionAbsoluteMinutes <= 0 {
		return errors.New("auth session idle and absolute minutes must be positive")
	}
	if c.SessionIdleMinutes > c.SessionAbsoluteMinutes {
		return errors.New("auth session idle minutes cannot exceed absolute minutes")
	}
	if len(c.AllowedOrigins) == 0 {
		return errors.New("auth allowedOrigins must not be empty")
	}
	for _, rawOrigin := range c.AllowedOrigins {
		origin := strings.TrimSpace(rawOrigin)
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
			return fmt.Errorf("invalid auth allowed origin %q", rawOrigin)
		}
	}
	return nil
}

// ============================================================
// HTTP 配置
// ============================================================

// HTTPConfig 对应 TOML 文件中的 [http] 配置块，用于配置 API 服务器的监听地址。
type HTTPConfig struct {
	Host string `toml:"host"` // 监听主机名，如 "0.0.0.0" 表示监听所有网卡
	Port int    `toml:"port"` // 监听端口号，如 8080
}

// Address 将 Host 和 Port 拼接成 net/http 需要的 "host:port" 格式。
// 例如：Host="0.0.0.0", Port=8080 → 返回 "0.0.0.0:8080"
func (c HTTPConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// ============================================================
// 日志配置
// ============================================================

// LogConfig 对应 TOML 文件中的 [log] 配置块。
// 控制台格式由 format 控制；文件日志固定使用 JSON，便于检索和采集。
type LogConfig struct {
	Level       string `toml:"level"`       // debug / info / warn / error
	Format      string `toml:"format"`      // console / json
	Environment string `toml:"environment"` // development / production
	EnableFile  bool   `toml:"enableFile"`  // 是否同时写入滚动日志文件
	OutputDir   string `toml:"outputDir"`   // 日志文件目录
	MaxSize     int    `toml:"maxSizeMB"`   // 单个文件最大 MB
	MaxAge      int    `toml:"maxAgeDays"`  // 最长保留天数
	MaxBackups  int    `toml:"maxBackups"`  // 最多保留的旧文件数
	Compress    bool   `toml:"compress"`    // 是否压缩旧文件
}

// ============================================================
// PostgreSQL 配置
// ============================================================

// PostgresConfig 对应 TOML 文件中的 [postgres] 配置块，
// 用于配置 PostgreSQL 数据库连接池和连接参数。
type PostgresConfig struct {
	Host        string `toml:"host"`         // 数据库主机地址，如 "localhost"
	Port        int    `toml:"port"`         // 数据库端口，默认 5432
	User        string `toml:"user"`         // 数据库用户名
	Database    string `toml:"database"`     // 要连接的数据库名称
	PasswordEnv string `toml:"passwordEnv"`  // 密码来源：指定存放密码的环境变量名（密码不写在 TOML 里，更安全）
	SSLMode     string `toml:"sslMode"`      // SSL 连接模式，如 "disable" / "require" / "verify-full"
	MaxIdle     int    `toml:"maxIdleConns"` // 连接池最大空闲连接数
	MaxOpen     int    `toml:"maxOpenConns"` // 连接池最大打开连接数
}

// Password 根据 PasswordEnv 指定的环境变量名读取数据库密码。
// 设计意图：密码不写入 TOML 配置文件（防止泄露），而是通过环境变量注入（如 Docker secret / CI 环境变量）。
func (c PostgresConfig) Password() (string, error) {
	return requiredEnv(c.PasswordEnv)
}

// ============================================================
// Redis 配置
// ============================================================

// RedisConfig 对应 TOML 文件中的 [redis] 配置块，
// 用于配置 Redis 客户端连接参数。
type RedisConfig struct {
	Host     string `toml:"host"`     // Redis 服务器地址，如 "localhost"
	Port     int    `toml:"port"`     // Redis 端口，默认 6379
	Password string `toml:"password"` // Redis 认证密码（可选，可留空）
	Database int    `toml:"database"` // 选择的 Redis 数据库编号，默认 0
}

// Address 将 Host 和 Port 拼接成 Redis 客户端需要的 "host:port" 格式。
func (c RedisConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// ============================================================
// 配置加载入口
// ============================================================

// Load 是配置加载的唯一入口，按以下顺序执行：
//  1. 加载 .env 文件（仓库根目录的 .env，主要用于本地开发）
//  2. 读取 MESGUARD_CONFIG_FILE 环境变量，确定 TOML 文件路径
//     - 如果该环境变量为空，默认使用 config/mesguard.toml
//  3. 解码 TOML 文件到 Config 结构体
//  4. 调用 Validate() 检查必要字段是否都已填写
//
// 同名环境变量优先级：系统环境变量（已存在的）> .env 文件中的变量。
// 原因：godotenv 默认不会覆盖已存在的系统环境变量，生产环境通过 Docker/K8s
// 注入的密码和配置文件路径应优先于本地 .env。
func Load() (Config, error) {
	// 第一步：加载 .env 文件
	// 本地开发时，开发者可以在 .env 中写 MESGUARD_CONFIG_FILE 等变量。
	// 文件不存在时静默跳过（生产环境通常不部署 .env）。
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	// 第二步：确定 TOML 配置文件路径
	// 优先读取 MESGUARD_CONFIG_FILE 环境变量，允许外部指定配置路径。
	// 典型用法：MESGUARD_CONFIG_FILE=/etc/mesguard/config.toml ./mesguard-api
	path := strings.TrimSpace(os.Getenv("MESGUARD_CONFIG_FILE"))
	if path == "" {
		path = "config/mesguard.toml"
	}

	// 第三步：解码 TOML 文件到 Config 结构体
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}

	// 第四步：校验配置完整性，确保启动前所有必要字段都已填写
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ============================================================
// 配置校验
// ============================================================

// Validate 在服务启动前检查配置是否完整，避免运行到一半才发现缺配置。
// 如果任何必填字段缺失，返回明确的错误信息，启动失败。
func (c Config) Validate() error {
	// 校验 HTTP 配置
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port <= 0 {
		return errors.New("http host and port are required")
	}
	if err := c.Auth.Validate(); err != nil {
		return err
	}
	if err := c.Agent.Validate(); err != nil {
		return err
	}
	if err := c.Models.Chat.Validate(); err != nil {
		return err
	}
	if c.Agent.ContextMemory.Summary.Enabled {
		if !c.Models.Chat.Enabled {
			return errors.New("agent contextMemory summary requires chat models")
		}
		memoryProfile, err := c.Models.Chat.ConversationMemoryProfile()
		if err != nil {
			return err
		}
		if memoryProfile.MaxOutputTokens*20 > memoryProfile.ContextWindowTokens {
			return errors.New("conversation memory profile maxOutputTokens must not exceed 5 percent of its context window")
		}
		if c.Agent.ContextMemory.SummaryTailEnabled {
			activeProfile, err := c.Models.Chat.ActiveProfile()
			if err != nil {
				return err
			}
			maxSummaryTokens := int(math.Floor(
				float64(activeProfile.ContextWindowTokens) * c.Agent.ContextMemory.SummaryMaxRatio,
			))
			if memoryProfile.MaxOutputTokens > maxSummaryTokens {
				return errors.New("conversation memory profile maxOutputTokens exceeds active profile Summary budget")
			}
		}
	}
	if (c.Agent.ContextMemory.ShadowPreflightEnabled ||
		c.Agent.ContextMemory.DiagnosisPreflightEnabled) && c.Models.Chat.Enabled {
		profile, err := c.Models.Chat.ActiveProfile()
		if err != nil {
			return err
		}
		available := profile.ContextWindowTokens - profile.MaxOutputTokens - profile.EffectivePromptSafetyMarginTokens()
		if c.Agent.ContextMemory.ToolGrowthReserveTokens >= available {
			return errors.New("agent contextMemory toolGrowthReserveTokens must leave active profile input capacity")
		}
	}
	if err := c.Models.Judge.Validate(); err != nil {
		return err
	}
	if err := c.Models.Embedding.Validate(); err != nil {
		return err
	}
	if err := c.Models.Rerank.Validate(); err != nil {
		return err
	}
	if err := c.Models.OCR.Validate("models.ocr"); err != nil {
		return err
	}
	if err := c.Models.Vision.Validate("models.vision"); err != nil {
		return err
	}
	if err := c.Models.Table.Validate("models.table"); err != nil {
		return err
	}
	if err := c.Log.Validate(); err != nil {
		return err
	}
	// 校验 PostgreSQL 配置：至少需要 host、port、user、database 和 passwordEnv
	if strings.TrimSpace(c.Postgres.Host) == "" || c.Postgres.Port <= 0 ||
		strings.TrimSpace(c.Postgres.User) == "" || strings.TrimSpace(c.Postgres.Database) == "" {
		return errors.New("postgres host, port, user, and database are required")
	}
	if strings.TrimSpace(c.Postgres.PasswordEnv) == "" {
		return errors.New("postgres passwordEnv is required")
	}
	// 校验 Redis 配置
	if strings.TrimSpace(c.Redis.Host) == "" || c.Redis.Port <= 0 {
		return errors.New("redis host and port are required")
	}
	if err := c.SQLServer.Validate(); err != nil {
		return err
	}
	if err := c.GitHubMCP.Validate(); err != nil {
		return err
	}
	if err := c.WebSearch.Validate(); err != nil {
		return err
	}
	if err := c.MinIO.Validate(); err != nil {
		return err
	}
	if err := c.Knowledge.Validate(); err != nil {
		return err
	}
	if c.Knowledge.Retrieval.QueryRewrite.Enabled {
		if _, err := c.Models.Chat.Profile(c.Knowledge.Retrieval.QueryRewrite.ModelProfile); err != nil {
			return fmt.Errorf("knowledge query rewrite modelProfile: %w", err)
		}
	}
	if c.MinIO.Enabled && c.Knowledge.MaxUploadBytes > c.MinIO.MaxObjectBytes {
		return errors.New("knowledge maxUploadBytes must not exceed minio maxObjectBytes")
	}
	if err := c.RabbitMQ.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate 检查日志级别、格式和文件轮转参数。
func (c LogConfig) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Level)) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log level must be debug, info, warn, or error")
	}
	switch strings.ToLower(strings.TrimSpace(c.Format)) {
	case "console", "json":
	default:
		return errors.New("log format must be console or json")
	}
	if strings.TrimSpace(c.Environment) == "" {
		return errors.New("log environment is required")
	}
	if c.EnableFile && (strings.TrimSpace(c.OutputDir) == "" || c.MaxSize <= 0 || c.MaxAge <= 0 || c.MaxBackups <= 0) {
		return errors.New("log file outputDir, maxSizeMB, maxAgeDays, and maxBackups must be configured")
	}
	return nil
}

// ============================================================
// 内部辅助函数
// ============================================================

// loadDotEnv 加载仓库根目录的 .env 文件。
// 设计意图：本地开发时使用 .env 管理环境变量，生产环境通过 Docker/K8s 注入。
// 文件不存在时返回 nil（允许继续启动），其他错误（权限问题等）则返回。
func loadDotEnv() error {
	err := godotenv.Load(".env")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// requiredEnv 读取指定名称的环境变量，确保其已设置且非空。
// 用于读取敏感信息（如数据库密码），避免硬编码在配置文件中。
func requiredEnv(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("environment variable name is not configured")
	}
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("environment variable %q is empty", name)
	}
	return value, nil
}
