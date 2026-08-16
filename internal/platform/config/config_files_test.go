package config

import (
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
			if cfg.Agent.ConversationPromptVersion != "conversation-v8" {
				t.Fatalf("%q conversation prompt version = %q, want conversation-v8 for stable per-message turn_context rendering",
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
			if cfg.Models.Chat.ActiveProfileName != "opencode-deepseek-main" ||
				cfg.Models.Chat.ConversationMemoryProfileName != "stepfun-conversation-memory" {
				t.Fatalf("%q must switch activeProfile=opencode-deepseek-main while keeping conversationMemoryProfile=stepfun-conversation-memory", path)
			}
			// Active Profile 必须解析为 OpenCode Go DeepSeek 生产身份。
			active, activeErr := cfg.Models.Chat.ActiveProfile()
			if activeErr != nil {
				t.Fatalf("ActiveProfile(%q): %v", path, activeErr)
			}
			if active.Provider != "opencode-go" || active.Model != "deepseek-v4-flash" ||
				active.BaseURL != "https://opencode.ai/zen/go/v1" ||
				active.APIKeyEnv != "MESGUARD_OPENCODE_GO_API_KEY" ||
				active.ResponseFormat != "text" ||
				active.ReasoningEffort != "" || active.ThinkingMode != "" {
				t.Fatalf("%q active profile must be the OpenCode Go DeepSeek identity: %+v", path, active)
			}
			// stepfun-main 保留为显式回退 Profile，不删除。
			stepfunMain, stepfunOK := cfg.Models.Chat.Profiles["stepfun-main"]
			if !stepfunOK || stepfunMain.Provider != "stepfun" || stepfunMain.Model != "step-3.7-flash" {
				t.Fatalf("%q must keep the stepfun-main fallback profile: %+v", path, stepfunMain)
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
				cfg.Agent.ContextMemory.PreflightTimeoutMillis != 250 ||
				cfg.Agent.ContextMemory.SoftThresholdRatio != 0.70 ||
				cfg.Agent.ContextMemory.HardThresholdRatio != 0.85 ||
				cfg.Agent.ContextMemory.ToolGrowthReserveTokens != 8192 {
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
