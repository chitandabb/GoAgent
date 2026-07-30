// Package agent 定义 MESGuard 的技能化 Agent 编排模型。
//
// Skill 不是可执行代码插件，而是一组经过应用校验的 Prompt、上下文预算、
// Tool 白名单和执行限制。真正的执行仍由 Eino Graph 与 ReAct Agent 完成。
package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type SkillID string

const (
	SkillTicketDiagnosis   SkillID = "ticket-diagnosis"
	SkillCodeInvestigation SkillID = "code-investigation"
)

var (
	skillIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,127}$`)
)

// ContextBudget 是单个 Skill 的应用侧预算。
// 它不代替模型自身的上下文上限；接入具体模型时还要校验该预算不超过模型能力。
type ContextBudget struct {
	MaxContextTokens     int
	ReservedOutputTokens int
	MaxEvidenceTokens    int
	MaxToolResultTokens  int
	MaxToolResultBytes   int
}

func (b ContextBudget) Validate() error {
	if b.MaxContextTokens <= 0 || b.ReservedOutputTokens <= 0 || b.MaxEvidenceTokens <= 0 ||
		b.MaxToolResultTokens <= 0 || b.MaxToolResultBytes <= 0 {
		return errors.New("context budget values must be positive")
	}
	if b.ReservedOutputTokens >= b.MaxContextTokens {
		return errors.New("reserved output tokens must be less than max context tokens")
	}
	if b.MaxEvidenceTokens+b.MaxToolResultTokens+b.ReservedOutputTokens > b.MaxContextTokens {
		return errors.New("evidence, tool result, and output budgets exceed max context tokens")
	}
	return nil
}

type SkillDefinition struct {
	ID           SkillID
	Version      string
	Description  string
	SystemPrompt string
	AllowedTools []string
	Budget       ContextBudget
	MaxSteps     int
	Timeout      time.Duration
}

func (d SkillDefinition) Validate() error {
	if !skillIDPattern.MatchString(string(d.ID)) {
		return fmt.Errorf("invalid skill id %q", d.ID)
	}
	if strings.TrimSpace(d.Version) == "" {
		return fmt.Errorf("skill %q version is required", d.ID)
	}
	if strings.TrimSpace(d.Description) == "" || strings.TrimSpace(d.SystemPrompt) == "" {
		return fmt.Errorf("skill %q description and system prompt are required", d.ID)
	}
	if len(d.AllowedTools) == 0 {
		return fmt.Errorf("skill %q must allow at least one tool", d.ID)
	}
	seen := make(map[string]struct{}, len(d.AllowedTools))
	for _, rawName := range d.AllowedTools {
		name := strings.TrimSpace(rawName)
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("skill %q contains invalid tool name %q", d.ID, rawName)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("skill %q contains duplicate tool %q", d.ID, name)
		}
		seen[name] = struct{}{}
	}
	if err := d.Budget.Validate(); err != nil {
		return fmt.Errorf("skill %q has invalid context budget: %w", d.ID, err)
	}
	if d.MaxSteps < 2 || d.MaxSteps > 32 {
		return fmt.Errorf("skill %q max steps must be between 2 and 32", d.ID)
	}
	if d.Timeout <= 0 || d.Timeout > 10*time.Minute {
		return fmt.Errorf("skill %q timeout must be between 0 and 10 minutes", d.ID)
	}
	return nil
}

const (
	ToolReadExternalCase         = "read_external_case"
	ToolRequestCodeInvestigation = "request_code_investigation"
)

var GitHubReadOnlyTools = []string{
	"search_code",
	"get_file_contents",
	"list_commits",
	"get_commit",
}
