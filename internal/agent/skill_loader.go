package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	skillManifestName    = "skill.toml"
	maxSystemPromptBytes = 64 * 1024
)

type skillManifest struct {
	SchemaVersion int                    `toml:"schemaVersion"`
	ID            string                 `toml:"id"`
	Version       string                 `toml:"version"`
	Enabled       *bool                  `toml:"enabled"`
	Description   string                 `toml:"description"`
	PromptFile    string                 `toml:"promptFile"`
	AllowedTools  []string               `toml:"allowedTools"`
	Budget        skillBudgetManifest    `toml:"budget"`
	Execution     skillExecutionManifest `toml:"execution"`
}

type skillBudgetManifest struct {
	MaxContextTokens     int `toml:"maxContextTokens"`
	ReservedOutputTokens int `toml:"reservedOutputTokens"`
	MaxEvidenceTokens    int `toml:"maxEvidenceTokens"`
	MaxToolResultTokens  int `toml:"maxToolResultTokens"`
	MaxToolResultBytes   int `toml:"maxToolResultBytes"`
}

type skillExecutionManifest struct {
	MaxSteps      int `toml:"maxSteps"`
	TimeoutMillis int `toml:"timeoutMillis"`
}

// LoadSkillDefinitions 从只读目录加载声明式 Skill 包。
// 配置只描述 Prompt、预算和 Tool 白名单，不允许加载或执行任意脚本。
func LoadSkillDefinitions(root string) ([]SkillDefinition, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("skill directory is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill directory: %w", err)
	}
	entries, err := os.ReadDir(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("read skill directory %q: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	definitions := make([]SkillDefinition, 0, len(entries))
	seen := make(map[SkillID]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("skill directory %q must not be a symbolic link", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		definition, enabled, loadErr := loadSkillPackage(filepath.Join(rootAbs, entry.Name()))
		if loadErr != nil {
			return nil, fmt.Errorf("load skill package %q: %w", entry.Name(), loadErr)
		}
		if !enabled {
			continue
		}
		if _, exists := seen[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate skill %q", definition.ID)
		}
		seen[definition.ID] = struct{}{}
		definitions = append(definitions, definition)
	}
	if len(definitions) == 0 {
		return nil, errors.New("skill directory contains no enabled skills")
	}
	return definitions, nil
}

func loadSkillPackage(directory string) (SkillDefinition, bool, error) {
	manifestPath := filepath.Join(directory, skillManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("read manifest: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return SkillDefinition{}, false, errors.New("skill manifest must be a regular file")
	}
	var manifest skillManifest
	metadata, err := toml.DecodeFile(manifestPath, &manifest)
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("decode manifest: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return SkillDefinition{}, false, fmt.Errorf("manifest contains unknown field %q", undecoded[0].String())
	}
	if manifest.SchemaVersion != 1 {
		return SkillDefinition{}, false, fmt.Errorf("unsupported schemaVersion %d", manifest.SchemaVersion)
	}
	if manifest.Enabled == nil {
		return SkillDefinition{}, false, errors.New("manifest enabled is required")
	}
	if !*manifest.Enabled {
		return SkillDefinition{}, false, nil
	}

	promptPath, err := resolveSkillResource(directory, manifest.PromptFile)
	if err != nil {
		return SkillDefinition{}, false, err
	}
	prompt, err := readBoundedRegularFile(promptPath, maxSystemPromptBytes)
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("read system prompt: %w", err)
	}
	definition := SkillDefinition{
		ID:           SkillID(strings.TrimSpace(manifest.ID)),
		Version:      strings.TrimSpace(manifest.Version),
		Description:  strings.TrimSpace(manifest.Description),
		SystemPrompt: strings.TrimSpace(string(prompt)),
		AllowedTools: append([]string(nil), manifest.AllowedTools...),
		Budget: ContextBudget{
			MaxContextTokens:     manifest.Budget.MaxContextTokens,
			ReservedOutputTokens: manifest.Budget.ReservedOutputTokens,
			MaxEvidenceTokens:    manifest.Budget.MaxEvidenceTokens,
			MaxToolResultTokens:  manifest.Budget.MaxToolResultTokens,
			MaxToolResultBytes:   manifest.Budget.MaxToolResultBytes,
		},
		MaxSteps: manifest.Execution.MaxSteps,
		Timeout:  time.Duration(manifest.Execution.TimeoutMillis) * time.Millisecond,
	}
	if err := definition.Validate(); err != nil {
		return SkillDefinition{}, false, err
	}
	return definition, true, nil
}

func resolveSkillResource(directory, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("promptFile must be a relative path")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("promptFile escapes the skill directory")
	}
	if filepath.Base(clean) != clean {
		return "", errors.New("promptFile must be stored in the skill package root")
	}
	return filepath.Join(directory, clean), nil
}

func readBoundedRegularFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("resource must be a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("resource exceeds %d bytes", maxBytes)
	}
	return os.ReadFile(path)
}
