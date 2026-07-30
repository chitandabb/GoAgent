package config

import "testing"

func validChatModelConfig() ChatModelConfig {
	return ChatModelConfig{
		Enabled: true, Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 120_000, MaxOutputTokens: 4096,
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
		{name: "unknown provider", mutate: func(c *ChatModelConfig) { c.Provider = "other" }},
		{name: "insecure remote URL", mutate: func(c *ChatModelConfig) { c.BaseURL = "http://api.stepfun.com/v1" }},
		{name: "invalid api key env", mutate: func(c *ChatModelConfig) { c.APIKeyEnv = "step-key" }},
		{name: "invalid effort", mutate: func(c *ChatModelConfig) { c.ReasoningEffort = "extreme" }},
		{name: "invalid timeout", mutate: func(c *ChatModelConfig) { c.TimeoutMillis = 0 }},
		{name: "invalid output limit", mutate: func(c *ChatModelConfig) { c.MaxOutputTokens = 0 }},
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
