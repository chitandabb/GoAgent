package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestRepositoryConfigFilesDecodeAndValidate(t *testing.T) {
	configDirectory := filepath.Join("..", "..", "..", "config")
	for _, name := range []string{"mesguard.toml", "mesguard.docker.toml"} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			path := filepath.Join(configDirectory, name)
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				t.Fatalf("DecodeFile(%q): %v", path, err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate(%q): %v", path, err)
			}
			if cfg.Agent.ConversationPromptVersion != "conversation-v9" {
				t.Fatalf("%q conversation prompt version = %q, want conversation-v9 for readonly SQL counting semantics",
					path, cfg.Agent.ConversationPromptVersion)
			}
			memoryProfile, err := cfg.Models.Chat.ConversationMemoryProfile()
			if err != nil {
				t.Fatalf("ConversationMemoryProfile(%q): %v", path, err)
			}
			if memoryProfile.EffectiveToolExposureStrategy() != ToolExposureStrategyStaticFrozen {
				t.Fatalf("%q conversation memory Tool exposure is not static_frozen", path)
			}
			opencode, ok := cfg.Models.Chat.Profiles["opencode-deepseek-main"]
			if !ok {
				t.Fatalf("%q must configure the opencode-deepseek-main profile", path)
			}
			if opencode.Provider != "opencode-go" ||
				opencode.BaseURL != "https://opencode.ai/zen/go/v1" ||
				opencode.APIKeyEnv != "MESGUARD_OPENCODE_GO_API_KEY" ||
				opencode.Model != "deepseek-v4-flash" ||
				opencode.ResponseFormat != "text" {
				t.Fatalf("%q opencode-deepseek-main = %+v", path, opencode)
			}
			if opencode.ReasoningEffort != "" || opencode.ThinkingMode != "" {
				t.Fatalf("%q opencode-deepseek-main must not configure reasoningEffort/thinkingMode: %+v", path, opencode)
			}
			if cfg.Models.Chat.ActiveProfileName != "stepfun-main" ||
				cfg.Models.Chat.ConversationMemoryProfileName != "stepfun-conversation-memory" {
				t.Fatalf("%q must use activeProfile=stepfun-main while keeping conversationMemoryProfile=stepfun-conversation-memory", path)
			}
			stepfunMain, stepfunOK := cfg.Models.Chat.Profiles["stepfun-main"]
			if !stepfunOK || stepfunMain.Provider != "stepfun" || stepfunMain.Model != "step-3.7-flash" {
				t.Fatalf("%q must configure the stepfun-main active profile: %+v", path, stepfunMain)
			}
			active, activeErr := cfg.Models.Chat.ActiveProfile()
			if activeErr != nil {
				t.Fatalf("ActiveProfile(%q): %v", path, activeErr)
			}
			if active.Provider != stepfunMain.Provider || active.Model != stepfunMain.Model ||
				active.BaseURL != stepfunMain.BaseURL || active.APIKeyEnv != stepfunMain.APIKeyEnv {
				t.Fatalf("%q active profile must resolve to stepfun-main: %+v", path, active)
			}
			// 切换后不能再拿 Active Profile 与 OpenCode 自己比较：对照必须是
			// 显式读取的 stepfun-main，保证首轮上下文合同不因切换漂移。
			if opencode.ContextWindowTokens != stepfunMain.ContextWindowTokens ||
				opencode.MaxOutputTokens != stepfunMain.MaxOutputTokens ||
				opencode.PromptSafetyMarginTokens != stepfunMain.PromptSafetyMarginTokens ||
				opencode.PromptSafetyMarginRatio != stepfunMain.PromptSafetyMarginRatio ||
				opencode.TokenizerStrategy != stepfunMain.TokenizerStrategy ||
				opencode.ToolExposureStrategy != stepfunMain.ToolExposureStrategy {
				t.Fatalf("%q opencode-deepseek-main context contract must match the stepfun-main fallback: %+v vs %+v", path, opencode, stepfunMain)
			}
			if !cfg.Agent.ContextMemory.ShadowPreflightEnabled ||
				!cfg.Agent.ContextMemory.DiagnosisPreflightEnabled ||
				!cfg.Agent.ContextMemory.ContinuousTailEnabled ||
				!cfg.Agent.ContextMemory.SummaryTailEnabled ||
				!cfg.Agent.ContextMemory.AsyncCompactionEnabled ||
				cfg.Agent.ContextMemory.AsyncMaxAttempts != 3 ||
				cfg.Agent.ContextMemory.RetryJitterRatio != 0.10 ||
				!cfg.Agent.ContextMemory.MemoryCacheEnabled ||
				cfg.Agent.ContextMemory.MemoryCacheTTL != "2h" ||
				cfg.Agent.ContextMemory.MemoryCacheJitterRatio != 0.10 ||
				cfg.Agent.ContextMemory.MemoryCacheTimeoutMillis != 50 ||
				!cfg.Agent.ContextMemory.SourceRecoveryEnabled ||
				cfg.Agent.ContextMemory.SourceRecoveryMaxMessages != 20 ||
				cfg.Agent.ContextMemory.SourceRecoveryMaxTokens != 8192 ||
				cfg.Agent.ContextMemory.SourceRecoveryMaxCalls != 2 ||
				cfg.Agent.ContextMemory.MemoryMaxRatio != 0.20 ||
				cfg.Agent.ContextMemory.SummaryMaxRatio != 0.05 ||
				cfg.Agent.ContextMemory.TailMaxRatio != 0.15 ||
				cfg.Agent.ContextMemory.PreflightTimeoutMillis != 500 ||
				cfg.Agent.ContextMemory.SoftThresholdRatio != 0.70 ||
				cfg.Agent.ContextMemory.HardThresholdRatio != 0.85 ||
				cfg.Agent.ContextMemory.ToolGrowthReserveTokens != 16384 ||
				cfg.Agent.ContextMemory.SyncCompactionTimeoutMillis != 45000 {
				t.Fatalf("%q context-memory shadow preflight = %+v", path, cfg.Agent.ContextMemory)
			}
			if cfg.RabbitMQ.MemoryCompactionQueue != "mesguard.conversation.memory.compact" ||
				cfg.RabbitMQ.MemoryCompactionRoutingKey != "conversation.memory.compact" {
				t.Fatalf("%q memory compaction topology = %+v", path, cfg.RabbitMQ)
			}
			if cfg.Models.Judge.Enabled {
				t.Fatalf("%q enables the offline Judge by default", path)
			}
			judge := cfg.Models.Judge
			judge.Enabled = true
			if err := judge.Validate(); err != nil {
				t.Fatalf("configured models.judge in %q is invalid: %v", path, err)
			}
		})
	}
}

func TestDefaultConfigFourProcessComposeBudgetStaysWithinSafetyEnvelope(t *testing.T) {
	// docker-compose.yml runs exactly four services that consume embeddings:
	// backend, conversation-worker, diagnosis-worker, knowledge-worker. Each
	// process enforces its own process-level budget from the same TOML file.
	// The sum of the four per-process budgets must stay within the provider
	// repository safety envelope of 900 RPM / 600000 TPM without Redis or
	// another distributed limiter. This is not an account-quota assertion;
	// horizontal scaling requires re-allocating these values.
	configDirectory := filepath.Join("..", "..", "..", "config")
	const embeddingConsumingProcesses = 4
	for _, name := range []string{"mesguard.toml", "mesguard.docker.toml"} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			path := filepath.Join(configDirectory, name)
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				t.Fatalf("DecodeFile(%q): %v", path, err)
			}
			embedding := cfg.Models.Embedding
			if !embedding.Enabled {
				t.Fatalf("%q disables the embedding model", path)
			}
			if embedding.RPM*embeddingConsumingProcesses > MaxEmbeddingRPM {
				t.Fatalf("%q per-process rpm=%d exceeds the repository safety envelope across %d processes",
					path, embedding.RPM, embeddingConsumingProcesses)
			}
			if embedding.TPM*embeddingConsumingProcesses > MaxEmbeddingTPM {
				t.Fatalf("%q per-process tpm=%d exceeds the repository safety envelope across %d processes",
					path, embedding.TPM, embeddingConsumingProcesses)
			}
			if embedding.MaxAttempts < 1 || embedding.BackoffMaxMillis < 1 {
				t.Fatalf("%q must configure explicit retry bounds: maxAttempts=%d backoffMaxMillis=%d",
					path, embedding.MaxAttempts, embedding.BackoffMaxMillis)
			}
		})
	}
}

func TestConversationPromptDefinesRowCountingSemantics(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "prompts", "conversation-system.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	prompt := string(content)
	for _, required := range []string{
		"use `COUNT(*)`",
		"`COUNT(column)` excludes NULL values",
		"count non-NULL values of a specific column",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("conversation prompt is missing %q", required)
		}
	}
}

func TestConfigRejectsConversationMemoryOutputThatExceedsActiveSummaryBudget(t *testing.T) {
	var cfg Config
	path := filepath.Join("..", "..", "..", "config", "mesguard.toml")
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("DecodeFile(%q): %v", path, err)
	}
	profiles := make(map[string]ChatModelProfileConfig, len(cfg.Models.Chat.Profiles))
	for name, profile := range cfg.Models.Chat.Profiles {
		profiles[name] = profile
	}
	cfg.Models.Chat.Profiles = profiles
	active := cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName]
	active.ContextWindowTokens = 8192
	active.MaxOutputTokens = 512
	active.PromptSafetyMarginTokens = 256
	active.PromptSafetyMarginRatio = 0
	cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName] = active
	cfg.Agent.ContextMemory.ToolGrowthReserveTokens = 256
	cfg.Agent.ContextMemory.SourceRecoveryMaxTokens = 256

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "active profile Summary budget") {
		t.Fatalf("Validate() error = %v, want active profile Summary budget rejection", err)
	}
}
