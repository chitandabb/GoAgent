package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

type userPromptPayload struct {
	ExternalCaseID string `json:"externalCaseId"`
	Question       string `json:"question"`
}

// BuildUserPrompt 将用户问题转换为传递给 Agent 的结构化消息。
func BuildUserPrompt(request RunRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	userQuery := strings.TrimSpace(request.UserQuery)
	externalCaseID := strings.TrimSpace(request.ExternalCaseID)
	if externalCaseID == "" {
		return userQuery, nil
	}
	payload := userPromptPayload{
		ExternalCaseID: externalCaseID,
		Question:       userQuery,
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal user prompt: %w", err)
	}
	return string(jsonBytes), nil
}
