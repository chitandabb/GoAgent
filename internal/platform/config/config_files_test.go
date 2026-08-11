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
			memoryProfile, err := cfg.Models.Chat.ConversationMemoryProfile()
			if err != nil {
				t.Fatalf("ConversationMemoryProfile(%q): %v", path, err)
			}
			if memoryProfile.EffectiveToolExposureStrategy() != ToolExposureStrategyStaticFrozen {
				t.Fatalf("%q conversation memory Tool exposure is not static_frozen", path)
			}
			if !cfg.Agent.ContextMemory.ShadowPreflightEnabled ||
				!cfg.Agent.ContextMemory.ContinuousTailEnabled ||
				!cfg.Agent.ContextMemory.SummaryTailEnabled ||
				!cfg.Agent.ContextMemory.AsyncCompactionEnabled ||
				cfg.Agent.ContextMemory.AsyncMaxAttempts != 3 ||
				cfg.Agent.ContextMemory.RetryJitterRatio != 0.10 ||
				!cfg.Agent.ContextMemory.MemoryCacheEnabled ||
				cfg.Agent.ContextMemory.MemoryCacheTTL != "2h" ||
				cfg.Agent.ContextMemory.MemoryCacheJitterRatio != 0.10 ||
				cfg.Agent.ContextMemory.MemoryCacheTimeoutMillis != 50 ||
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
	cfg.Agent.ContextMemory.ToolGrowthReserveTokens = 128

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "active profile Summary budget") {
		t.Fatalf("Validate() error = %v, want active profile Summary budget rejection", err)
	}
}
