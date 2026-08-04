package config

// ============================================================
// 配置层：负责从 .env 文件和 TOML 配置文件加载 MESGuard 的运行配置。
//
// 加载顺序：.env → 确定 TOML 路径 → 解码 TOML → 校验
// 同名环境变量优先级：系统环境变量 > .env 文件中的变量
// TOML 普通字段不会被任意环境变量自动覆盖；敏感值通过 passwordEnv 显式引用。
// ============================================================

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

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
	Chat  ChatModelConfig  `toml:"chat"`
	Judge JudgeModelConfig `toml:"judge"`
}

// ChatModelConfig 定义 OpenAI 兼容聊天模型。当前生产 Provider 为 StepFun Step Plan。
type ChatModelConfig struct {
	Enabled         bool   `toml:"enabled"`
	Provider        string `toml:"provider"`
	BaseURL         string `toml:"baseURL"`
	APIKeyEnv       string `toml:"apiKeyEnv"`
	Model           string `toml:"model"`
	ReasoningEffort string `toml:"reasoningEffort"`
	TimeoutMillis   int    `toml:"timeoutMillis"`
	MaxOutputTokens int    `toml:"maxOutputTokens"`
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (c ChatModelConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "stepfun" {
		return errors.New("models.chat provider must be stepfun")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return errors.New("models.chat baseURL must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("models.chat baseURL must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("models.chat apiKeyEnv is invalid")
	}
	if !modelName.MatchString(strings.TrimSpace(c.Model)) {
		return errors.New("models.chat model is invalid")
	}
	switch strings.ToLower(strings.TrimSpace(c.ReasoningEffort)) {
	case "low", "medium", "high":
	default:
		return errors.New("models.chat reasoningEffort must be low, medium, or high")
	}
	if c.TimeoutMillis <= 0 || c.TimeoutMillis > 300_000 {
		return errors.New("models.chat timeoutMillis must be between 1 and 300000")
	}
	if c.MaxOutputTokens <= 0 || c.MaxOutputTokens > 65_536 {
		return errors.New("models.chat maxOutputTokens must be between 1 and 65536")
	}
	return nil
}

func (c ChatModelConfig) APIKey() (string, error) {
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
	SkillsDirectory    string `toml:"skillsDirectory"`
	PromptVersion      string `toml:"promptVersion"`
	SystemPromptFile   string `toml:"systemPromptFile"`
	BaselinePromptFile string `toml:"baselinePromptFile"`
	ReportContractFile string `toml:"reportContractFile"`
	MaxAgentRuns       int    `toml:"maxAgentRuns"`
	MaxToolCalls       int    `toml:"maxToolCalls"`
	MaxEvidenceItems   int    `toml:"maxEvidenceItems"`
	MaxTotalTokens     int    `toml:"maxTotalTokens"`
	TimeoutMillis      int    `toml:"timeoutMillis"`
}

func (c AgentConfig) Validate() error {
	if strings.TrimSpace(c.SkillsDirectory) == "" {
		return errors.New("agent skillsDirectory is required")
	}
	if !modelName.MatchString(strings.TrimSpace(c.PromptVersion)) {
		return errors.New("agent promptVersion is invalid")
	}
	for _, promptFile := range []struct {
		name string
		path string
	}{
		{name: "systemPromptFile", path: c.SystemPromptFile},
		{name: "baselinePromptFile", path: c.BaselinePromptFile},
		{name: "reportContractFile", path: c.ReportContractFile},
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

// WebSearchConfig 描述公开网页检索的供应商连接与硬预算。
// API Key 只能通过 apiKeyEnv 引用，不得出现在 TOML、日志或 Tool 输出中。
type WebSearchConfig struct {
	Enabled          bool   `toml:"enabled"`
	Provider         string `toml:"provider"`
	BaseURL          string `toml:"baseURL"`
	APIKeyEnv        string `toml:"apiKeyEnv"`
	TimeoutMillis    int    `toml:"timeoutMillis"`
	MaxResults       int    `toml:"maxResults"`
	MaxFetchedPages  int    `toml:"maxFetchedPages"`
	MaxPageChars     int    `toml:"maxPageChars"`
	MaxRounds        int    `toml:"maxRounds"`
	MaxResponseBytes int64  `toml:"maxResponseBytes"`
}

func (c WebSearchConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "firecrawl" {
		return errors.New("webSearch provider must be firecrawl")
	}
	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Host == "" {
		return errors.New("webSearch baseURL must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1")) {
		return errors.New("webSearch baseURL must use HTTPS unless it points to localhost")
	}
	if !environmentVariableName.MatchString(strings.TrimSpace(c.APIKeyEnv)) {
		return errors.New("webSearch apiKeyEnv is invalid")
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
	return nil
}

func (c WebSearchConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
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

// KnowledgeConfig 固定知识入库任务的可追踪流水线版本和重试上限。
type KnowledgeConfig struct {
	PipelineVersion             string `toml:"pipelineVersion"`
	MaxAttempts                 int    `toml:"maxAttempts"`
	MaxUploadBytes              int64  `toml:"maxUploadBytes"`
	ChunkMaxRunes               int    `toml:"chunkMaxRunes"`
	ChunkOverlapRunes           int    `toml:"chunkOverlapRunes"`
	ParserMaxDocumentUnits      int    `toml:"parserMaxDocumentUnits"`
	ParserMaxArchiveEntries     int    `toml:"parserMaxArchiveEntries"`
	ParserMaxExpandedBytes      int64  `toml:"parserMaxExpandedBytes"`
	ParserMaxXMLBytes           int64  `toml:"parserMaxXMLBytes"`
	ParserMaxExtractedRunes     int    `toml:"parserMaxExtractedRunes"`
	ParserMaxSpreadsheetRows    int    `toml:"parserMaxSpreadsheetRows"`
	ParserMaxSpreadsheetColumns int    `toml:"parserMaxSpreadsheetColumns"`
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
	if err := c.Models.Judge.Validate(); err != nil {
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
