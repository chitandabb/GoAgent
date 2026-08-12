package config

import "testing"

func validChatModelProfileConfig() ChatModelProfileConfig {
	return ChatModelProfileConfig{
		Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 120_000,
		ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
		PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: TokenizerStrategyLocalCalibrated,
	}
}

func validChatModelConfig() ChatModelConfig {
	memory := validChatModelProfileConfig()
	memory.Provider = "dashscope"
	memory.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	memory.APIKeyEnv = "DASHSCOPE_API_KEY"
	memory.Model = "qwen3.6-flash"
	memory.ReasoningEffort = ""
	memory.ThinkingMode = "disabled"
	return ChatModelConfig{
		Enabled: true, ActiveProfileName: "stepfun-main", ConversationMemoryProfileName: "conversation-memory",
		Profiles: map[string]ChatModelProfileConfig{
			"stepfun-main": validChatModelProfileConfig(), "conversation-memory": memory,
		},
	}
}

func TestChatModelConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatModelConfig)
		valid  bool
	}{
		{name: "valid StepFun", valid: true},
		{name: "disabled accepts empty", mutate: func(c *ChatModelConfig) { *c = ChatModelConfig{} }, valid: true},
		{name: "missing active profile", mutate: func(c *ChatModelConfig) { c.ActiveProfileName = "missing" }},
		{name: "missing memory profile selection", mutate: func(c *ChatModelConfig) {
			c.ConversationMemoryProfileName = ""
		}},
		{name: "missing memory profile", mutate: func(c *ChatModelConfig) {
			c.ConversationMemoryProfileName = "missing"
		}},
		{name: "configured profile missing context contract", mutate: func(c *ChatModelConfig) {
			profile := c.Profiles["stepfun-main"]
			profile.ContextWindowTokens = 0
			profile.PromptSafetyMarginTokens = 0
			profile.PromptSafetyMarginRatio = 0
			profile.TokenizerStrategy = ""
			profile.ToolExposureStrategy = ""
			profile.ProviderNativeCompactionEnabled = false
			c.Profiles["stepfun-main"] = profile
		}},
		{name: "invalid profile name", mutate: func(c *ChatModelConfig) {
			c.Profiles["bad profile"] = validChatModelProfileConfig()
		}},
		{name: "invalid configured inactive profile", mutate: func(c *ChatModelConfig) {
			profile := validChatModelProfileConfig()
			profile.Provider = "other"
			c.Profiles["inactive"] = profile
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validChatModelConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			if err := cfg.Validate(); (err == nil) != tt.valid {
				t.Fatalf("Validate error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestChatModelProfileConfigValidate(t *testing.T) {
	temperature := float32(0)
	tests := []struct {
		name   string
		mutate func(*ChatModelProfileConfig)
		valid  bool
	}{
		{name: "valid StepFun", valid: true},
		{name: "valid DeepSeek root URL", mutate: func(c *ChatModelProfileConfig) {
			c.Provider = "deepseek"
			c.BaseURL = "https://api.deepseek.com"
			c.ReasoningEffort = ""
			c.ThinkingMode = "disabled"
			c.Temperature = &temperature
		}, valid: true},
		{name: "unknown provider", mutate: func(c *ChatModelProfileConfig) { c.Provider = "other" }},
		{name: "insecure remote URL", mutate: func(c *ChatModelProfileConfig) { c.BaseURL = "http://api.stepfun.com/v1" }},
		{name: "invalid api key env", mutate: func(c *ChatModelProfileConfig) { c.APIKeyEnv = "step-key" }},
		{name: "invalid effort", mutate: func(c *ChatModelProfileConfig) { c.ReasoningEffort = "extreme" }},
		{name: "invalid thinking mode", mutate: func(c *ChatModelProfileConfig) { c.ThinkingMode = "auto" }},
		{name: "valid JSON object response", mutate: func(c *ChatModelProfileConfig) { c.ResponseFormat = "json_object" }, valid: true},
		{name: "valid JSON schema response", mutate: func(c *ChatModelProfileConfig) {
			c.ResponseFormat, c.ResponseSchema = "json_schema", "conversation_memory_v1"
		}, valid: true},
		{name: "missing response schema", mutate: func(c *ChatModelProfileConfig) { c.ResponseFormat = "json_schema" }},
		{name: "orphan response schema", mutate: func(c *ChatModelProfileConfig) { c.ResponseSchema = "conversation_memory_v1" }},
		{name: "invalid response format", mutate: func(c *ChatModelProfileConfig) { c.ResponseFormat = "yaml" }},
		{name: "invalid timeout", mutate: func(c *ChatModelProfileConfig) { c.TimeoutMillis = 0 }},
		{name: "invalid output limit", mutate: func(c *ChatModelProfileConfig) { c.MaxOutputTokens = 0 }},
		{name: "missing context window", mutate: func(c *ChatModelProfileConfig) { c.ContextWindowTokens = 0 }},
		{name: "output consumes context window", mutate: func(c *ChatModelProfileConfig) {
			c.ContextWindowTokens = c.MaxOutputTokens
		}},
		{name: "negative safety margin tokens", mutate: func(c *ChatModelProfileConfig) {
			c.PromptSafetyMarginTokens = -1
		}},
		{name: "invalid safety margin ratio", mutate: func(c *ChatModelProfileConfig) {
			c.PromptSafetyMarginRatio = 0.51
		}},
		{name: "safety margin consumes input", mutate: func(c *ChatModelProfileConfig) {
			c.ContextWindowTokens = 8192
			c.MaxOutputTokens = 4096
			c.PromptSafetyMarginTokens = 4096
		}},
		{name: "unknown tokenizer strategy", mutate: func(c *ChatModelProfileConfig) {
			c.TokenizerStrategy = "remote_only"
		}},
		{name: "unknown tool exposure strategy", mutate: func(c *ChatModelProfileConfig) {
			c.ToolExposureStrategy = "dynamic_any"
		}},
		{name: "provider native compaction is optional", mutate: func(c *ChatModelProfileConfig) {
			c.ProviderNativeCompactionEnabled = true
		}, valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validChatModelProfileConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			if err := cfg.Validate(); (err == nil) != tt.valid {
				t.Fatalf("Validate error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestChatModelProfileConfigEffectiveContextContract(t *testing.T) {
	cfg := validChatModelProfileConfig()
	if got := cfg.EffectivePromptSafetyMarginTokens(); got != 6554 {
		t.Fatalf("EffectivePromptSafetyMarginTokens() = %d, want 6554", got)
	}
	if got := cfg.EffectiveToolExposureStrategy(); got != ToolExposureStrategyStaticFrozen {
		t.Fatalf("EffectiveToolExposureStrategy() = %q, want %q", got, ToolExposureStrategyStaticFrozen)
	}

	cfg.ToolExposureStrategy = ToolExposureStrategyEpochRebind
	if got := cfg.EffectiveToolExposureStrategy(); got != ToolExposureStrategyEpochRebind {
		t.Fatalf("configured EffectiveToolExposureStrategy() = %q, want %q", got, ToolExposureStrategyEpochRebind)
	}
}

func TestChatModelProfilePromptFingerprintTracksBehaviorButNotAPIKeyLocation(t *testing.T) {
	profile := validChatModelProfileConfig()
	original, err := profile.PromptProfileFingerprint("stepfun-main")
	if err != nil {
		t.Fatal(err)
	}
	profile.APIKeyEnv = "MESGUARD_ALTERNATE_STEPFUN_API_KEY"
	withoutSecretLocation, err := profile.PromptProfileFingerprint("stepfun-main")
	if err != nil {
		t.Fatal(err)
	}
	if withoutSecretLocation != original {
		t.Fatalf("API key environment name changed prompt fingerprint: %q != %q", withoutSecretLocation, original)
	}
	profile.MaxOutputTokens++
	withContractChange, err := profile.PromptProfileFingerprint("stepfun-main")
	if err != nil {
		t.Fatal(err)
	}
	if withContractChange == original {
		t.Fatal("prompt-window contract change did not change prompt fingerprint")
	}
	profile.MaxOutputTokens--
	profile.ResponseFormat = "json_object"
	withResponseFormat, err := profile.PromptProfileFingerprint("stepfun-main")
	if err != nil {
		t.Fatal(err)
	}
	if withResponseFormat == original {
		t.Fatal("response format change did not change prompt fingerprint")
	}
	profile.ResponseFormat = "json_schema"
	profile.ResponseSchema = "conversation_memory_v1"
	withResponseSchema, err := profile.PromptProfileFingerprint("stepfun-main")
	if err != nil {
		t.Fatal(err)
	}
	if withResponseSchema == withResponseFormat {
		t.Fatal("response schema change did not change prompt fingerprint")
	}
}

func TestChatModelConfigConversationMemoryProfile(t *testing.T) {
	cfg := validChatModelConfig()
	profile, err := cfg.ConversationMemoryProfile()
	if err != nil {
		t.Fatalf("ConversationMemoryProfile() error = %v", err)
	}
	if profile.Model != "qwen3.6-flash" {
		t.Fatalf("ConversationMemoryProfile().Model = %q, want qwen3.6-flash", profile.Model)
	}
}
