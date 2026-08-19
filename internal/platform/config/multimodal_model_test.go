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
		ReasoningEffort: "low", ResponseFormat: "json_object",
		TimeoutMillis: 120_000, MaxOutputTokens: 2048,
	}
	if err := valid.Validate("models.vision"); err != nil {
		t.Fatal(err)
	}
	stepfun := valid
	stepfun.Provider = "stepfun"
	stepfun.BaseURL = "https://api.stepfun.com/step_plan/v1"
	stepfun.APIKeyEnv = "MESGUARD_STEPFUN_API_KEY"
	stepfun.Model = "step-3.7-flash"
	if err := stepfun.Validate("models.vision"); err != nil {
		t.Fatalf("Validate StepFun configuration: %v", err)
	}
	prompt, err := valid.LoadPrompt("models.vision")
	if err != nil || prompt != "return strict JSON" {
		t.Fatalf("LoadPrompt = %q, %v", prompt, err)
	}

	custom := valid
	custom.Provider = "custom-openai"
	if err := custom.Validate("models.vision"); err != nil {
		t.Fatalf("Validate generic OpenAI-compatible provider: %v", err)
	}

	plainText := valid
	plainText.ResponseFormat = "text"
	if err := plainText.Validate("models.ocr"); err != nil {
		t.Fatalf("Validate plain-text OCR configuration: %v", err)
	}
	if err := plainText.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted plain-text response for visual semantics")
	}
	missingStructuredFormat := valid
	missingStructuredFormat.ResponseFormat = ""
	if err := missingStructuredFormat.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted an empty response format for visual semantics")
	}
	if err := missingStructuredFormat.Validate("models.table"); err == nil {
		t.Fatal("Validate accepted an empty response format for table recovery")
	}
	qwenOCR := valid
	qwenOCR.Provider = "dashscope"
	qwenOCR.Model = "qwen-vl-ocr-latest"
	qwenOCR.ResponseFormat = "text"
	qwenOCR.MaxOutputTokens = 4096
	if err := qwenOCR.Validate("models.ocr"); err != nil {
		t.Fatalf("Validate rejected the supported Qwen OCR limit: %v", err)
	}
	qwenOCR.MaxOutputTokens = 4097
	if err := qwenOCR.Validate("models.ocr"); err == nil {
		t.Fatal("Validate accepted a Qwen OCR output limit above the provider maximum")
	}
	otherOCR := qwenOCR
	otherOCR.Model = "other-ocr-model"
	otherOCR.MaxOutputTokens = 8192
	if err := otherOCR.Validate("models.ocr"); err != nil {
		t.Fatalf("Validate applied the Qwen OCR limit to another model: %v", err)
	}

	invalid := valid
	invalid.Provider = "unsupported provider"
	if err := invalid.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted invalid provider identifier")
	}
	invalid = valid
	invalid.ReasoningEffort = "extreme"
	if err := invalid.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted invalid reasoning effort")
	}
	invalid = valid
	invalid.ResponseFormat = "json_schema"
	if err := invalid.Validate("models.vision"); err == nil {
		t.Fatal("Validate accepted unsupported response format")
	}
	invalid = valid
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
