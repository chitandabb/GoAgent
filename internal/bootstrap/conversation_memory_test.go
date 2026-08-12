package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"gorm.io/gorm"
)

func TestBuildConversationMemoryServiceUsesTheIndependentMemoryProfile(t *testing.T) {
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "conversation-memory.md")
	if err := os.WriteFile(promptPath, []byte("Return structured memory JSON."), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testAgentConfig()
	cfg.Agent.ContextMemory.Summary = config.ConversationMemorySummaryConfig{
		Enabled: true, PromptFile: promptPath, PromptVersion: "conversation-memory-v1",
		MaxPayloadBytes: 64 * 1024, MaxAttempts: 3, RetryBaseDelayMillis: 250,
	}
	memoryProfile := cfg.Models.Chat.Profiles["test"]
	memoryProfile.Provider = "dashscope"
	memoryProfile.Model = "qwen3.6-flash"
	memoryProfile.ReasoningEffort = ""
	memoryProfile.ThinkingMode = "disabled"
	memoryProfile.ResponseFormat = "json_schema"
	memoryProfile.ResponseSchema = "conversation_memory_v1"
	memoryProfile.TimeoutMillis = 30_000
	cfg.Models.Chat.Profiles["memory"] = memoryProfile
	cfg.Models.Chat.ConversationMemoryProfileName = "memory"
	requestedProfile := ""

	service, err := buildConversationMemoryService(
		context.Background(), &gorm.DB{}, cfg,
		func(_ context.Context, _ config.ChatModelConfig, profileName string) (*chatmodel.Instance, error) {
			requestedProfile = profileName
			return &chatmodel.Instance{
				Model:    stubAgentChatModel{},
				Identity: chatmodel.Identity{Profile: profileName, Provider: "dashscope", ModelID: "qwen3.6-flash"},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("buildConversationMemoryService() error = %v", err)
	}
	if service == nil || requestedProfile != "memory" || requestedProfile == cfg.Models.Chat.ActiveProfileName {
		t.Fatalf("service/profile = %v/%q, active = %q", service, requestedProfile, cfg.Models.Chat.ActiveProfileName)
	}
}

func TestValidateConversationMemoryProfileRequiresStrictSchemaContract(t *testing.T) {
	valid := config.ChatModelProfileConfig{
		ResponseFormat: "json_schema", ResponseSchema: "conversation_memory_v1",
	}
	if err := validateConversationMemoryProfile(valid); err != nil {
		t.Fatalf("valid profile error = %v", err)
	}
	for _, profile := range []config.ChatModelProfileConfig{
		{ResponseFormat: "json_object"},
		{ResponseFormat: "text"},
		{ResponseFormat: "json_schema", ResponseSchema: "other_v1"},
	} {
		if err := validateConversationMemoryProfile(profile); err == nil {
			t.Fatalf("validateConversationMemoryProfile(%+v) succeeded", profile)
		}
	}
}
