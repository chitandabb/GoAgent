package config

import "testing"

func validRerankModelConfig() RerankModelConfig {
	return RerankModelConfig{
		Enabled: true, Provider: "dashscope",
		Endpoint:  "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "qwen3-rerank", MaxCandidates: 30, TimeoutMillis: 30_000,
	}
}

func TestRerankModelConfigValidate(t *testing.T) {
	valid := validRerankModelConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*RerankModelConfig)
	}{
		{name: "provider", mutate: func(c *RerankModelConfig) { c.Provider = "other" }},
		{name: "insecure endpoint", mutate: func(c *RerankModelConfig) { c.Endpoint = "http://dashscope.example/rerank" }},
		{name: "api key env", mutate: func(c *RerankModelConfig) { c.APIKeyEnv = "not-a-var" }},
		{name: "model", mutate: func(c *RerankModelConfig) { c.Model = "qwen rerank" }},
		{name: "candidate limit", mutate: func(c *RerankModelConfig) { c.MaxCandidates = 51 }},
		{name: "timeout", mutate: func(c *RerankModelConfig) { c.TimeoutMillis = 999 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := valid
			test.mutate(&current)
			if err := current.Validate(); err == nil {
				t.Fatal("Validate accepted invalid config")
			}
		})
	}
	if err := (RerankModelConfig{}).Validate(); err != nil {
		t.Fatalf("disabled Validate(): %v", err)
	}
}
