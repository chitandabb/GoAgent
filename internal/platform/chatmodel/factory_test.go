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

func TestProviderAdaptersUseExpectedRequestShape(t *testing.T) {
	tests := []struct {
		name       string
		profile    config.ChatModelProfileConfig
		assertBody func(*testing.T, map[string]any)
	}{
		{
			name:    "stepfun reasoning effort",
			profile: profileForTest("stepfun", "step-3.7-flash", "medium", ""),
			assertBody: func(t *testing.T, body map[string]any) {
				if body["reasoning_effort"] != "medium" {
					t.Fatalf("reasoning_effort = %v", body["reasoning_effort"])
				}
			},
		},
		{
			name:    "deepseek thinking disabled",
			profile: profileForTest("deepseek", "deepseek-v4-flash", "", "disabled"),
			assertBody: func(t *testing.T, body map[string]any) {
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "disabled" {
					t.Fatalf("thinking = %#v", body["thinking"])
				}
			},
		},
		{
			name:    "deepseek thinking effort",
			profile: profileForTest("deepseek", "deepseek-v4-flash", "max", "enabled"),
			assertBody: func(t *testing.T, body map[string]any) {
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "enabled" || body["reasoning_effort"] != "max" {
					t.Fatalf("thinking request = %#v", body)
				}
			},
		},
		{
			name:    "dashscope thinking disabled",
			profile: profileForTest("dashscope", "qwen3.6-flash", "", "disabled"),
			assertBody: func(t *testing.T, body map[string]any) {
				if body["enable_thinking"] != false {
					t.Fatalf("enable_thinking = %#v", body["enable_thinking"])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
					"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model",
					"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
					"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
				}`))
			}))
			defer server.Close()

			t.Setenv("MESGUARD_TEST_CHAT_KEY", "test-key")
			tt.profile.BaseURL = server.URL + "/v1"
			instance, err := New(context.Background(), "test-profile", tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			message, err := instance.Model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
			if err != nil {
				t.Fatal(err)
			}
			if message.Content != "ok" || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil ||
				message.ResponseMeta.Usage.TotalTokens != 15 {
				t.Fatalf("message = %+v", message)
			}
			if requestBody["model"] != tt.profile.Model || requestBody["max_tokens"] != float64(128) {
				t.Fatalf("request body = %#v", requestBody)
			}
			tt.assertBody(t, requestBody)
		})
	}
}

func TestNewActiveReadsOnlySelectedProfileKey(t *testing.T) {
	active := profileForTest("stepfun", "step-3.7-flash", "low", "")
	active.APIKeyEnv = "ACTIVE_CHAT_KEY"
	inactive := profileForTest("dashscope", "qwen3.6-flash", "", "disabled")
	inactive.APIKeyEnv = "INACTIVE_CHAT_KEY"
	t.Setenv("ACTIVE_CHAT_KEY", "configured")
	t.Setenv("INACTIVE_CHAT_KEY", "")

	instance, err := NewActive(context.Background(), config.ChatModelConfig{
		Enabled: true, ActiveProfileName: "main",
		Profiles: map[string]config.ChatModelProfileConfig{"main": active, "rewrite": inactive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if instance.Identity.Profile != "main" || instance.Identity.Provider != "stepfun" {
		t.Fatalf("identity = %+v", instance.Identity)
	}
}

func TestProviderAdapterRejectsUnsupportedParameters(t *testing.T) {
	tests := []config.ChatModelProfileConfig{
		profileForTest("stepfun", "step", "medium", "disabled"),
		profileForTest("deepseek", "deepseek-chat", "low", "disabled"),
		profileForTest("dashscope", "qwen", "low", "disabled"),
		profileForTest("deepseek", "deepseek-chat", "", ""),
	}
	for _, profile := range tests {
		t.Run(profile.Provider+profile.ReasoningEffort+profile.ThinkingMode, func(t *testing.T) {
			t.Setenv("MESGUARD_TEST_CHAT_KEY", "test-key")
			if _, err := New(context.Background(), "invalid", profile); err == nil {
				t.Fatalf("accepted profile %+v", profile)
			}
		})
	}
}

func profileForTest(provider, model, reasoningEffort, thinkingMode string) config.ChatModelProfileConfig {
	temperature := float32(0)
	return config.ChatModelProfileConfig{
		Provider: provider, BaseURL: "https://example.com/v1", APIKeyEnv: "MESGUARD_TEST_CHAT_KEY",
		Model: model, ReasoningEffort: reasoningEffort, ThinkingMode: thinkingMode,
		Temperature: &temperature, TimeoutMillis: 5000, MaxOutputTokens: 128,
	}
}
