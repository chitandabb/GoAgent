package chatmodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
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
			name: "stepfun json object",
			profile: func() config.ChatModelProfileConfig {
				profile := profileForTest("stepfun", "step-3.7-flash", "low", "")
				profile.ResponseFormat = "json_object"
				return profile
			}(),
			assertBody: func(t *testing.T, body map[string]any) {
				format, ok := body["response_format"].(map[string]any)
				if !ok || format["type"] != "json_object" {
					t.Fatalf("response_format = %#v", body["response_format"])
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
			name: "dashscope thinking disabled",
			profile: func() config.ChatModelProfileConfig {
				profile := profileForTest("dashscope", "qwen3.6-flash", "", "disabled")
				profile.ResponseFormat = "json_object"
				return profile
			}(),
			assertBody: func(t *testing.T, body map[string]any) {
				if body["enable_thinking"] != false {
					t.Fatalf("enable_thinking = %#v", body["enable_thinking"])
				}
				format, ok := body["response_format"].(map[string]any)
				if !ok || format["type"] != "json_object" {
					t.Fatalf("response_format = %#v", body["response_format"])
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

func TestProviderAdaptersUseInjectedJSONSchema(t *testing.T) {
	for _, tt := range []struct {
		provider, model, reasoningEffort, thinkingMode string
	}{
		{provider: "stepfun", model: "step-3.7-flash", reasoningEffort: "low"},
		{provider: "dashscope", model: "qwen3.6-flash", thinkingMode: "disabled"},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model",
					"choices":[{"index":0,"message":{"role":"assistant","content":"{\"answer\":\"ok\"}"},"finish_reason":"stop"}],
					"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
				}`))
			}))
			defer server.Close()

			profile := profileForTest(tt.provider, tt.model, tt.reasoningEffort, tt.thinkingMode)
			profile.BaseURL = server.URL + "/v1"
			profile.ResponseFormat, profile.ResponseSchema = "json_schema", "fixture_v1"
			t.Setenv("MESGUARD_TEST_CHAT_KEY", "test-key")
			instance, err := newInstance(context.Background(), "fixture", profile, &ResponseSchema{
				Name: "fixture_v1", Description: "fixture response", Strict: true,
				Schema: &jsonschema.Schema{Type: "object", Required: []string{"answer"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := instance.Model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
				t.Fatal(err)
			}
			format, ok := requestBody["response_format"].(map[string]any)
			schemaBody, schemaOK := format["json_schema"].(map[string]any)
			schemaValue, valueOK := schemaBody["schema"].(map[string]any)
			if !ok || format["type"] != "json_schema" || !schemaOK ||
				schemaBody["name"] != "fixture_v1" || schemaBody["description"] != "fixture response" ||
				schemaBody["strict"] != true || !valueOK || schemaValue["type"] != "object" {
				t.Fatalf("response_format = %#v", requestBody["response_format"])
			}
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
		Enabled: true, ActiveProfileName: "main", ConversationMemoryProfileName: "rewrite",
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
		func() config.ChatModelProfileConfig {
			profile := profileForTest("deepseek", "deepseek-chat", "", "disabled")
			profile.ResponseFormat, profile.ResponseSchema = "json_schema", "fixture_v1"
			return profile
		}(),
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
		Temperature: &temperature, TimeoutMillis: 5000,
		ContextWindowTokens: 8192, MaxOutputTokens: 128,
		PromptSafetyMarginTokens: 256, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
}

// openCodeGoProfileForTest 构造 opencode-go 方言的合法 Profile；模型名故意使用
// deepseek-v4-flash，验证"模型名含 deepseek 不得路由到 deepSeekAdapter"。
func openCodeGoProfileForTest() config.ChatModelProfileConfig {
	temperature := float32(0.5)
	return config.ChatModelProfileConfig{
		Provider: "opencode-go", BaseURL: "https://opencode.ai/zen/go/v1",
		APIKeyEnv: "MESGUARD_OPENCODE_GO_API_KEY", Model: "deepseek-v4-flash",
		ResponseFormat: "text", Temperature: &temperature, TimeoutMillis: 5000,
		ContextWindowTokens: 8192, MaxOutputTokens: 128,
		PromptSafetyMarginTokens: 256, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
}

type openCodeProbeInput struct {
	Value string `json:"value" jsonschema:"required" jsonschema_description:"固定填写 ok"`
}

func TestOpenCodeGoAdapterRequestShape(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zen/go/v1/chat/completions" {
			t.Errorf("request path = %q, want /zen/go/v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q, want Bearer test-key", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"deepseek-v4-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "test-key")
	profile := openCodeGoProfileForTest()
	profile.BaseURL = server.URL + "/zen/go/v1"
	instance, err := New(context.Background(), "opencode-deepseek-main", profile)
	if err != nil {
		t.Fatal(err)
	}
	message, err := instance.Model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "ok" {
		t.Fatalf("message = %+v", message)
	}
	if requestBody["model"] != "deepseek-v4-flash" {
		t.Fatalf("model = %v, want deepseek-v4-flash", requestBody["model"])
	}
	if requestBody["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens = %v, want 128", requestBody["max_tokens"])
	}
	if requestBody["temperature"] != 0.5 {
		t.Fatalf("temperature = %v, want 0.5", requestBody["temperature"])
	}
	for _, forbidden := range []string{"thinking", "reasoning_effort", "enable_thinking"} {
		if _, ok := requestBody[forbidden]; ok {
			t.Fatalf("request body must not contain %q: %#v", forbidden, requestBody)
		}
	}
}

func TestOpenCodeGoAdapterSendsToolsAndParsesToolCall(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek-v4-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"mesguard_capability_probe","arguments":"{\"value\":\"ok\"}"}}]},
				"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "test-key")
	profile := openCodeGoProfileForTest()
	profile.BaseURL = server.URL + "/zen/go/v1"
	instance, err := New(context.Background(), "opencode-deepseek-main", profile)
	if err != nil {
		t.Fatal(err)
	}
	probeTool, err := toolutils.InferTool(
		"mesguard_capability_probe", "MESGuard 模型兼容性探针",
		func(context.Context, openCodeProbeInput) (string, error) { return "ok", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	toolInfo, err := probeTool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	boundModel, err := instance.Model.WithTools([]*schema.ToolInfo{toolInfo})
	if err != nil {
		t.Fatal(err)
	}
	message, err := boundModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("调用探针工具")})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "mesguard_capability_probe" {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	if !strings.Contains(message.ToolCalls[0].Function.Arguments, `"ok"`) {
		t.Fatalf("tool call arguments = %q", message.ToolCalls[0].Function.Arguments)
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one declared tool", requestBody["tools"])
	}
}

func TestOpenCodeGoAdapterValidate(t *testing.T) {
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "test-key")
	t.Run("rejects reasoning effort", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ReasoningEffort = "low"
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err == nil ||
			!strings.Contains(err.Error(), "reasoningEffort") {
			t.Fatalf("error = %v, want reasoningEffort rejection", err)
		}
	})
	t.Run("rejects thinking mode", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ThinkingMode = "disabled"
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err == nil ||
			!strings.Contains(err.Error(), "thinkingMode") {
			t.Fatalf("error = %v, want thinkingMode rejection", err)
		}
	})
	t.Run("accepts text response format", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ResponseFormat = "text"
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err != nil {
			t.Fatalf("text response format must be accepted: %v", err)
		}
	})
	t.Run("accepts empty response format", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ResponseFormat = ""
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err != nil {
			t.Fatalf("empty response format must be accepted: %v", err)
		}
	})
	t.Run("rejects json object", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ResponseFormat = "json_object"
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err == nil ||
			!strings.Contains(err.Error(), "json_object") {
			t.Fatalf("error = %v, want json_object rejection", err)
		}
	})
	t.Run("rejects json schema", func(t *testing.T) {
		profile := openCodeGoProfileForTest()
		profile.ResponseFormat = "json_schema"
		profile.ResponseSchema = "fixture_v1"
		if _, err := New(context.Background(), "opencode-deepseek-main", profile); err == nil ||
			!strings.Contains(err.Error(), "json_schema") {
			t.Fatalf("error = %v, want json_schema rejection", err)
		}
	})
}

// TestOpenCodeGoDoesNotRouteToDeepSeekAdapter 证明 provider 是唯一路由键：模型名
// 含 deepseek 时，opencode-go 不得继承 deepSeekAdapter 的 thinkingMode 约束。
func TestOpenCodeGoDoesNotRouteToDeepSeekAdapter(t *testing.T) {
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "test-key")
	profile := openCodeGoProfileForTest() // model = deepseek-v4-flash，无 thinkingMode/reasoningEffort
	instance, err := New(context.Background(), "opencode-deepseek-main", profile)
	if err != nil {
		t.Fatalf("opencode-go must not inherit deepSeekAdapter constraints: %v", err)
	}
	if instance.Identity.Provider != "opencode-go" {
		t.Fatalf("provider = %q, want opencode-go", instance.Identity.Provider)
	}
	if instance.Identity.Capabilities.ThinkingMode || instance.Identity.Capabilities.ReasoningEffort {
		t.Fatalf("capabilities must not leak deepSeekAdapter semantics: %+v", instance.Identity.Capabilities)
	}
}

func TestOpenCodeGoAdapterCapabilities(t *testing.T) {
	caps := opencodeGoAdapter{}.capabilities(openCodeGoProfileForTest())
	if !caps.ToolCalling || caps.ReasoningEffort || caps.ThinkingMode ||
		caps.ReasoningContentRequired || caps.JSONOutput || caps.JSONSchemaOutput {
		t.Fatalf("capabilities = %+v, want ToolCalling only", caps)
	}
}

func TestNewProfileReadsOnlySpecifiedProfileKey(t *testing.T) {
	stepfun := profileForTest("stepfun", "step-3.7-flash", "low", "")
	stepfun.APIKeyEnv = "MESGUARD_STEPFUN_API_KEY"
	cfg := config.ChatModelConfig{
		Enabled: true, ActiveProfileName: "stepfun-main", ConversationMemoryProfileName: "stepfun-memory",
		Profiles: map[string]config.ChatModelProfileConfig{
			"stepfun-main": stepfun, "stepfun-memory": stepfun, "opencode-deepseek-main": openCodeGoProfileForTest(),
		},
	}
	t.Setenv("MESGUARD_STEPFUN_API_KEY", "stepfun-key")
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "")

	instance, err := NewProfile(context.Background(), cfg, "stepfun-main")
	if err != nil {
		t.Fatalf("NewProfile(stepfun-main) must not require the opencode key: %v", err)
	}
	if instance.Identity.Provider != "stepfun" {
		t.Fatalf("identity = %+v", instance.Identity)
	}
	if _, err := NewProfile(context.Background(), cfg, "opencode-deepseek-main"); err == nil ||
		!strings.Contains(err.Error(), "MESGUARD_OPENCODE_GO_API_KEY") {
		t.Fatalf("NewProfile(opencode-deepseek-main) error = %v, want fail-closed missing key", err)
	}
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "opencode-key")
	instance, err = NewProfile(context.Background(), cfg, "opencode-deepseek-main")
	if err != nil {
		t.Fatalf("NewProfile(opencode-deepseek-main) with key: %v", err)
	}
	if instance.Identity.Provider != "opencode-go" || instance.Identity.ModelID != "deepseek-v4-flash" {
		t.Fatalf("identity = %+v", instance.Identity)
	}
}

func TestNewActiveIgnoresMissingOpenCodeKeyWhenStepFunActive(t *testing.T) {
	stepfun := profileForTest("stepfun", "step-3.7-flash", "low", "")
	stepfun.APIKeyEnv = "MESGUARD_STEPFUN_API_KEY"
	cfg := config.ChatModelConfig{
		Enabled: true, ActiveProfileName: "stepfun-main", ConversationMemoryProfileName: "stepfun-memory",
		Profiles: map[string]config.ChatModelProfileConfig{
			"stepfun-main": stepfun, "stepfun-memory": stepfun, "opencode-deepseek-main": openCodeGoProfileForTest(),
		},
	}
	t.Setenv("MESGUARD_STEPFUN_API_KEY", "stepfun-key")
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "")
	instance, err := NewActive(context.Background(), cfg)
	if err != nil {
		t.Fatalf("active stepfun-main must not require the opencode key: %v", err)
	}
	if instance.Identity.Profile != "stepfun-main" {
		t.Fatalf("identity = %+v", instance.Identity)
	}
}

func TestNewActiveOpenCodeProfileFailsClosedWithoutKey(t *testing.T) {
	stepfun := profileForTest("stepfun", "step-3.7-flash", "low", "")
	stepfun.APIKeyEnv = "MESGUARD_STEPFUN_API_KEY"
	cfg := config.ChatModelConfig{
		Enabled: true, ActiveProfileName: "opencode-deepseek-main", ConversationMemoryProfileName: "stepfun-memory",
		Profiles: map[string]config.ChatModelProfileConfig{
			"stepfun-memory": stepfun, "opencode-deepseek-main": openCodeGoProfileForTest(),
		},
	}
	t.Setenv("MESGUARD_STEPFUN_API_KEY", "stepfun-key")
	t.Setenv("MESGUARD_OPENCODE_GO_API_KEY", "")
	if _, err := NewActive(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "MESGUARD_OPENCODE_GO_API_KEY") {
		t.Fatalf("NewActive error = %v, want fail-closed missing opencode key", err)
	}
}
