package config

import "testing"

func validEmbeddingModelConfig() EmbeddingModelConfig {
	return EmbeddingModelConfig{
		Enabled: true, ProfileKey: "knowledge-v1", Provider: "dashscope",
		Endpoint:  "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "text-embedding-v4", Dimensions: 1024,
		DistanceMetric: "cosine", QueryInputType: "query", DocumentInputType: "document",
		Normalize: true, ConfigVersion: "embedding-v1", BatchSize: 10, MaxConcurrent: 2,
		TimeoutMillis: 30000,
	}
}

func TestEmbeddingModelConfigValidate(t *testing.T) {
	valid := validEmbeddingModelConfig()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	profile, err := valid.Profile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Dimensions != 1024 || profile.Fingerprint == "" {
		t.Fatal("embedding profile was not materialized")
	}

	tests := []struct {
		name   string
		mutate func(*EmbeddingModelConfig)
	}{
		{name: "provider", mutate: func(c *EmbeddingModelConfig) { c.Provider = "other" }},
		{name: "endpoint", mutate: func(c *EmbeddingModelConfig) { c.Endpoint = "http://example.com/embed" }},
		{name: "dimensions", mutate: func(c *EmbeddingModelConfig) { c.Dimensions = 768 }},
		{name: "input modes", mutate: func(c *EmbeddingModelConfig) { c.QueryInputType = "document" }},
		{name: "normalization", mutate: func(c *EmbeddingModelConfig) { c.Normalize = false }},
		{name: "batch", mutate: func(c *EmbeddingModelConfig) { c.BatchSize = 11 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if err := (EmbeddingModelConfig{}).Validate(); err != nil {
		t.Fatalf("disabled embedding config should be valid: %v", err)
	}
}
