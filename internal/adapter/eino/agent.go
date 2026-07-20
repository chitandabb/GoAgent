package einoadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Agent is an Eino-backed implementation of diagnosis.Agent. It deliberately
// exposes no Eino types to the diagnosis package.
type Agent struct {
	chat model.ToolCallingChatModel
}

func New(ctx context.Context, cfg config.LLMConfig) (*Agent, error) {
	apiKey, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("llm baseURL and model are required")
	}
	chat, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		APIKey:  apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create eino chat model: %w", err)
	}
	return &Agent{chat: chat}, nil
}

func (a *Agent) Diagnose(ctx context.Context, input diagnosis.AgentInput) (diagnosis.AgentOutput, error) {
	messages := []*schema.Message{
		schema.SystemMessage("You are a MES diagnostic assistant. State evidence, uncertainty, and recommended next checks."),
		schema.UserMessage(fmt.Sprintf(
			"Subject type: %s\nSubject id: %s\nQuestion: %s\nMES data: %#v",
			input.SubjectType, input.SubjectID, input.Question, input.MESData,
		)),
	}
	response, err := a.chat.Generate(ctx, messages)
	if err != nil {
		return diagnosis.AgentOutput{}, fmt.Errorf("generate diagnosis: %w", err)
	}
	return diagnosis.AgentOutput{Summary: response.Content}, nil
}
