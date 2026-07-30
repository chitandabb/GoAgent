// Package chatmodel 创建 MESGuard 使用的聊天模型客户端。
package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// NewStepFun 使用 Step Plan 的 OpenAI 兼容接口创建 Eino ToolCallingChatModel。
func NewStepFun(ctx context.Context, cfg config.ChatModelConfig) (model.ToolCallingChatModel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("StepFun chat model is disabled")
	}
	apiKey, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	maxOutputTokens := cfg.MaxOutputTokens
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		Model:   strings.TrimSpace(cfg.Model),
		Timeout: time.Duration(cfg.TimeoutMillis) * time.Millisecond,
		// StepFun Chat Completion 官方示例使用 max_tokens。
		MaxTokens:       &maxOutputTokens,
		ReasoningEffort: reasoningEffort(cfg.ReasoningEffort),
	})
	if err != nil {
		return nil, fmt.Errorf("create StepFun chat model: %w", err)
	}
	return chatModel, nil
}

func reasoningEffort(value string) openai.ReasoningEffortLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return openai.ReasoningEffortLevelLow
	case "high":
		return openai.ReasoningEffortLevelHigh
	default:
		return openai.ReasoningEffortLevelMedium
	}
}
