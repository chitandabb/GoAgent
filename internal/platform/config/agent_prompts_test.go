package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfigLoadPrompts(t *testing.T) {
	directory := t.TempDir()
	cfg := validAgentConfigForTest()
	cfg.SystemPromptFile = writePromptFileForTest(t, directory, "system.md", " system instruction \n")
	cfg.BaselinePromptFile = writePromptFileForTest(t, directory, "baseline.md", "baseline instruction")
	cfg.ReportContractFile = writePromptFileForTest(t, directory, "report.md", "report contract")

	prompts, err := cfg.LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	if prompts.SystemInstruction != "system instruction" ||
		prompts.BaselineInstruction != "baseline instruction" ||
		prompts.ReportContractInstruction != "report contract" {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
}

func TestAgentConfigLoadPromptsRejectsInvalidFiles(t *testing.T) {
	directory := t.TempDir()
	validPath := writePromptFileForTest(t, directory, "valid.md", "valid")
	tests := []struct {
		name    string
		content string
		missing bool
	}{
		{name: "empty", content: " \n\t"},
		{name: "too large", content: strings.Repeat("x", maxAgentPromptBytes+1)},
		{name: "missing", missing: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+".md")
			if !test.missing {
				path = writePromptFileForTest(t, directory, filepath.Base(path), test.content)
			}
			cfg := validAgentConfigForTest()
			cfg.SystemPromptFile = path
			cfg.BaselinePromptFile = validPath
			cfg.ReportContractFile = validPath
			if _, err := cfg.LoadPrompts(); err == nil {
				t.Fatal("LoadPrompts accepted invalid prompt file")
			}
		})
	}
}

func writePromptFileForTest(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write prompt fixture: %v", err)
	}
	return path
}
