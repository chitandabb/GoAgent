package config

import "testing"

func validChatModelProfileConfig() ChatModelProfileConfig {
	return ChatModelProfileConfig{
		Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 120_000, MaxOutputTokens: 4096,
	}
}

func validChatModelConfig() ChatModelConfig {
	return ChatModelConfig{
		Enabled: true, ActiveProfileName: "stepfun-main",
		Profiles: map[string]ChatModelProfileConfig{"stepfun-main": validChatModelProfileConfig()},
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
		{name: "invalid timeout", mutate: func(c *ChatModelProfileConfig) { c.TimeoutMillis = 0 }},
		{name: "invalid output limit", mutate: func(c *ChatModelProfileConfig) { c.MaxOutputTokens = 0 }},
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
