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
	ConversationInstruction   string
}

func (c AgentConfig) LoadPrompts() (AgentPrompts, error) {
	systemInstruction, err := loadPromptFile("agent", "system prompt", c.SystemPromptFile, maxAgentPromptBytes)
	if err != nil {
		return AgentPrompts{}, err
	}
	baselineInstruction, err := loadPromptFile("agent", "baseline prompt", c.BaselinePromptFile, maxAgentPromptBytes)
	if err != nil {
		return AgentPrompts{}, err
	}
	reportContractInstruction, err := loadPromptFile("agent", "report contract", c.ReportContractFile, maxAgentPromptBytes)
	if err != nil {
		return AgentPrompts{}, err
	}
	conversationInstruction, err := loadPromptFile("agent", "conversation prompt", c.ConversationPromptFile, maxAgentPromptBytes)
	if err != nil {
		return AgentPrompts{}, err
	}
	return AgentPrompts{
		SystemInstruction:         systemInstruction,
		BaselineInstruction:       baselineInstruction,
		ReportContractInstruction: reportContractInstruction,
		ConversationInstruction:   conversationInstruction,
	}, nil
}

func loadPromptFile(owner, name, path string, maxBytes int) (string, error) {
	path = strings.TrimSpace(path)
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s %s file %q: %w", owner, name, path, err)
	}
	if len(content) > maxBytes {
		return "", fmt.Errorf("%s %s file %q exceeds %d bytes", owner, name, path, maxBytes)
	}
	instruction := strings.TrimSpace(string(content))
	if instruction == "" {
		return "", fmt.Errorf("%s %s file %q is empty", owner, name, path)
	}
	return instruction, nil
}
