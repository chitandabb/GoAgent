package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillDefinitions(t *testing.T) {
	root := t.TempDir()
	writeSkillPackage(t, root, "ticket", SkillTicketDiagnosis, true, "system-prompt.md", "diagnose ticket", "")
	writeSkillPackage(t, root, "code", SkillCodeInvestigation, true, "system-prompt.md", "investigate code", "")

	definitions, err := LoadSkillDefinitions(root)
	if err != nil {
		t.Fatalf("LoadSkillDefinitions: %v", err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions count = %d", len(definitions))
	}
	if definitions[0].ID != SkillCodeInvestigation || definitions[1].ID != SkillTicketDiagnosis {
		t.Fatalf("definitions are not deterministically sorted: %v, %v", definitions[0].ID, definitions[1].ID)
	}
	if definitions[1].SystemPrompt != "diagnose ticket" {
		t.Fatalf("ticket prompt = %q", definitions[1].SystemPrompt)
	}
	if definitions[1].Version != "test-v1" {
		t.Fatalf("ticket version = %q", definitions[1].Version)
	}
}

func TestRepositorySkillPackages(t *testing.T) {
	definitions, err := LoadSkillDefinitions(filepath.Join("..", "..", "config", "skills"))
	if err != nil {
		t.Fatalf("LoadSkillDefinitions: %v", err)
	}
	registry, err := NewRegistry(definitions...)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, id := range []SkillID{SkillTicketDiagnosis, SkillCodeInvestigation} {
		if _, err := registry.Get(id); err != nil {
			t.Fatalf("repository skill %q: %v", id, err)
		}
	}
}

func TestLoadSkillDefinitionsRejectsUnsafeOrInvalidPackages(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string)
		want    string
	}{
		{
			name: "unknown manifest field",
			prepare: func(t *testing.T, root string) {
				writeSkillPackage(t, root, "ticket", SkillTicketDiagnosis, true, "system-prompt.md", "prompt", "unknownField = true\n")
			},
			want: "unknown field",
		},
		{
			name: "prompt path traversal",
			prepare: func(t *testing.T, root string) {
				writeSkillPackage(t, root, "ticket", SkillTicketDiagnosis, true, "../outside.md", "prompt", "")
			},
			want: "escapes the skill directory",
		},
		{
			name: "duplicate skill id",
			prepare: func(t *testing.T, root string) {
				writeSkillPackage(t, root, "first", SkillTicketDiagnosis, true, "system-prompt.md", "prompt", "")
				writeSkillPackage(t, root, "second", SkillTicketDiagnosis, true, "system-prompt.md", "prompt", "")
			},
			want: "duplicate skill",
		},
		{
			name: "no enabled skills",
			prepare: func(t *testing.T, root string) {
				writeSkillPackage(t, root, "ticket", SkillTicketDiagnosis, false, "system-prompt.md", "", "")
			},
			want: "contains no enabled skills",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.prepare(t, root)
			_, err := LoadSkillDefinitions(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadSkillDefinitions error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func writeSkillPackage(
	t *testing.T,
	root, directory string,
	id SkillID,
	enabled bool,
	promptFile, prompt, extra string,
) {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create skill directory: %v", err)
	}
	manifest := fmt.Sprintf(`schemaVersion = 1
id = %q
version = "test-v1"
enabled = %t
description = "test skill"
promptFile = %q
allowedTools = ["read_external_case"]
%s
[budget]
maxContextTokens = 1000
reservedOutputTokens = 100
maxEvidenceTokens = 300
maxToolResultTokens = 200
maxToolResultBytes = 1024

[execution]
maxSteps = 4
timeoutMillis = 1000
`, id, enabled, promptFile, extra)
	if err := os.WriteFile(filepath.Join(dir, skillManifestName), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}
	if enabled && !strings.HasPrefix(promptFile, "..") {
		if err := os.WriteFile(filepath.Join(dir, promptFile), []byte(prompt), 0o600); err != nil {
			t.Fatalf("write prompt: %v", err)
		}
	}
}
