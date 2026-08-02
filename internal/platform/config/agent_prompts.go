package config

import (
	"fmt"
	"os"
	"strings"
)

const maxAgentPromptBytes = 32 * 1024

// AgentPrompts 是启动时加载并缓存的 Agent 指令集合。
type AgentPrompts struct {
	SystemInstruction         string
	BaselineInstruction       string
	ReportContractInstruction string
}

func (c AgentConfig) LoadPrompts() (AgentPrompts, error) {
	systemInstruction, err := loadPromptFile("system prompt", c.SystemPromptFile)
	if err != nil {
		return AgentPrompts{}, err
	}
	baselineInstruction, err := loadPromptFile("baseline prompt", c.BaselinePromptFile)
	if err != nil {
		return AgentPrompts{}, err
	}
	reportContractInstruction, err := loadPromptFile("report contract", c.ReportContractFile)
	if err != nil {
		return AgentPrompts{}, err
	}
	return AgentPrompts{
		SystemInstruction:         systemInstruction,
		BaselineInstruction:       baselineInstruction,
		ReportContractInstruction: reportContractInstruction,
	}, nil
}

func loadPromptFile(name, path string) (string, error) {
	path = strings.TrimSpace(path)
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read agent %s file %q: %w", name, path, err)
	}
	if len(content) > maxAgentPromptBytes {
		return "", fmt.Errorf("agent %s file %q exceeds %d bytes", name, path, maxAgentPromptBytes)
	}
	instruction := strings.TrimSpace(string(content))
	if instruction == "" {
		return "", fmt.Errorf("agent %s file %q is empty", name, path)
	}
	return instruction, nil
}
