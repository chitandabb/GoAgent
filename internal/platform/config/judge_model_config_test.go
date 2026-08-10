package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func validJudgeModelConfig() JudgeModelConfig {
	return JudgeModelConfig{
		Enabled: true, Provider: "dashscope",
		BaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "qwen3-max",
		PromptFile: "config/prompts/rag-judge.md", PromptVersion: "rag-judge-v2",
		TimeoutMillis: 120_000, MaxOutputTokens: 2048,
	}
}

func TestJudgeModelConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JudgeModelConfig)
		valid  bool
	}{
		{name: "valid DashScope", valid: true},
		{name: "disabled accepts empty", mutate: func(c *JudgeModelConfig) { *c = JudgeModelConfig{} }, valid: true},
		{name: "unknown provider", mutate: func(c *JudgeModelConfig) { c.Provider = "other" }},
		{name: "insecure remote URL", mutate: func(c *JudgeModelConfig) { c.BaseURL = "http://dashscope.aliyuncs.com/v1" }},
		{name: "invalid API key env", mutate: func(c *JudgeModelConfig) { c.APIKeyEnv = "secret-value" }},
		{name: "invalid model", mutate: func(c *JudgeModelConfig) { c.Model = "qwen model" }},
		{name: "empty prompt file", mutate: func(c *JudgeModelConfig) { c.PromptFile = "" }},
		{name: "oversized prompt path", mutate: func(c *JudgeModelConfig) { c.PromptFile = strings.Repeat("x", 513) }},
		{name: "invalid prompt version", mutate: func(c *JudgeModelConfig) { c.PromptVersion = "rag judge v1" }},
		{name: "invalid timeout", mutate: func(c *JudgeModelConfig) { c.TimeoutMillis = 999 }},
		{name: "invalid output limit", mutate: func(c *JudgeModelConfig) { c.MaxOutputTokens = 255 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validJudgeModelConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			if err := cfg.Validate(); (err == nil) != tt.valid {
				t.Fatalf("Validate error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestJudgeModelConfigAPIKeyReadsConfiguredEnvironment(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY_TEST", " test-key ")
	value, err := (JudgeModelConfig{APIKeyEnv: "DASHSCOPE_API_KEY_TEST"}).APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if value != "test-key" {
		t.Fatalf("APIKey() = %q", value)
	}
}

func TestJudgeModelConfigLoadPrompt(t *testing.T) {
	directory := t.TempDir()
	path := writePromptFileForTest(t, directory, "judge.md", " judge instruction \n")
	cfg := validJudgeModelConfig()
	cfg.PromptFile = path

	prompt, err := cfg.LoadPrompt()
	if err != nil {
		t.Fatalf("LoadPrompt: %v", err)
	}
	if prompt != "judge instruction" {
		t.Fatalf("LoadPrompt() = %q", prompt)
	}

	cfg.PromptFile = filepath.Join(directory, "missing.md")
	if _, err := cfg.LoadPrompt(); err == nil {
		t.Fatal("LoadPrompt accepted a missing file")
	}
}
