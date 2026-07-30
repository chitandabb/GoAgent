package chatmodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/schema"
)

func TestNewStepFunUsesOpenAICompatibleContract(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"step-3.7-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	t.Setenv("MESGUARD_STEPFUN_API_KEY", "test-key")
	chatModel, err := NewStepFun(context.Background(), config.ChatModelConfig{
		Enabled: true, Provider: "stepfun", BaseURL: server.URL + "/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 5000, MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewStepFun: %v", err)
	}
	message, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if message.Content != "ok" {
		t.Fatalf("content = %q", message.Content)
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil || message.ResponseMeta.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", message.ResponseMeta)
	}
	if requestBody["model"] != "step-3.7-flash" {
		t.Fatalf("model = %v", requestBody["model"])
	}
	if requestBody["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort = %v", requestBody["reasoning_effort"])
	}
	if requestBody["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens = %v", requestBody["max_tokens"])
	}
}

func TestNewStepFunRequiresAPIKey(t *testing.T) {
	t.Setenv("MESGUARD_STEPFUN_API_KEY", "")
	_, err := NewStepFun(context.Background(), config.ChatModelConfig{
		Enabled: true, Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "medium", TimeoutMillis: 5000, MaxOutputTokens: 1024,
	})
	if err == nil {
		t.Fatal("NewStepFun accepted an empty API key")
	}
}
