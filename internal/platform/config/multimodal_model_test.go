package config

import (
	"path/filepath"
	"testing"
)

func TestMultimodalModelConfigValidateAndLoadPrompt(t *testing.T) {
	promptPath := writePromptFileForTest(t, t.TempDir(), "vision.md", " return strict JSON ")
	valid := MultimodalModelConfig{
		Enabled: true, Provider: "dashscope",
		BaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "qwen3-vl-plus",
		PromptFile: promptPath, PromptVersion: "vision-v1",
		TimeoutMillis: 120_000, MaxOutputTokens: 2048,
	}
	if err := valid.Validate("models.vision"); err != nil {
		t.Fatal(err)
	}
	prompt, err := valid.LoadPrompt("models.vision")
	if err != nil || prompt != "return strict JSON" {
		t.Fatalf("LoadPrompt = %q, %v", prompt, err)
	}

	invalid := valid
	invalid.BaseURL = "http://example.com/v1"
	if err := invalid.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted insecure remote base URL")
	}
	invalid = valid
	invalid.PromptFile = filepath.Join(t.TempDir(), "missing.md")
	if _, err := invalid.LoadPrompt("models.vision"); err == nil {
		t.Fatal("LoadPrompt accepted missing file")
	}
	if err := (MultimodalModelConfig{}).Validate("models.vision"); err != nil {
		t.Fatalf("disabled config must degrade: %v", err)
	}
}
