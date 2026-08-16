package config

import "testing"

func validEmbeddingModelConfig() EmbeddingModelConfig {
	return EmbeddingModelConfig{
		Enabled: true, ProfileKey: "knowledge-v1", Provider: "dashscope",
		Endpoint:  "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding",
		APIKeyEnv: "DASHSCOPE_API_KEY", Model: "text-embedding-v4", Dimensions: 1024,
		DistanceMetric: "cosine", QueryInputType: "query", DocumentInputType: "document",
		Normalize: true, ConfigVersion: "embedding-v1", BatchSize: 10, MaxConcurrent: 2,
		TimeoutMillis: 30000, RPM: 200, TPM: 150_000, MaxAttempts: 3, BackoffMaxMillis: 10_000,
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
		{name: "negative rpm", mutate: func(c *EmbeddingModelConfig) { c.RPM = -1 }},
		{name: "rpm above safety ceiling", mutate: func(c *EmbeddingModelConfig) { c.RPM = MaxEmbeddingRPM + 1 }},
		{name: "tpm below floor", mutate: func(c *EmbeddingModelConfig) { c.TPM = 999 }},
		{name: "tpm above safety ceiling", mutate: func(c *EmbeddingModelConfig) { c.TPM = MaxEmbeddingTPM + 1 }},
		{name: "negative max attempts", mutate: func(c *EmbeddingModelConfig) { c.MaxAttempts = -1 }},
		{name: "max attempts above bound", mutate: func(c *EmbeddingModelConfig) { c.MaxAttempts = 9 }},
		{name: "negative backoff cap", mutate: func(c *EmbeddingModelConfig) { c.BackoffMaxMillis = -1 }},
		{name: "backoff cap below floor", mutate: func(c *EmbeddingModelConfig) { c.BackoffMaxMillis = 999 }},
		{name: "backoff cap above bound", mutate: func(c *EmbeddingModelConfig) { c.BackoffMaxMillis = 120_001 }},
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

func TestEmbeddingModelConfigAppliesProcessQuotaDefaults(t *testing.T) {
	cfg := validEmbeddingModelConfig()
	cfg.RPM, cfg.TPM, cfg.MaxAttempts, cfg.BackoffMaxMillis = 0, 0, 0, 0
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectiveRPM(); got != DefaultEmbeddingRPM {
		t.Fatalf("EffectiveRPM() = %d, want %d", got, DefaultEmbeddingRPM)
	}
	if got := cfg.EffectiveTPM(); got != DefaultEmbeddingTPM {
		t.Fatalf("EffectiveTPM() = %d, want %d", got, DefaultEmbeddingTPM)
	}
	if got := cfg.EffectiveMaxAttempts(); got != DefaultEmbeddingMaxAttempts {
		t.Fatalf("EffectiveMaxAttempts() = %d, want %d", got, DefaultEmbeddingMaxAttempts)
	}
	if got := cfg.EffectiveBackoffMaxMillis(); got != DefaultEmbeddingBackoffMaxMillis {
		t.Fatalf("EffectiveBackoffMaxMillis() = %d, want %d", got, DefaultEmbeddingBackoffMaxMillis)
	}
}

func TestEmbeddingProfileFingerprintExcludesOpsParameters(t *testing.T) {
	base := validEmbeddingModelConfig()
	baseProfile, err := base.Profile()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EmbeddingModelConfig){
		"rpm":            func(c *EmbeddingModelConfig) { c.RPM = 900 },
		"tpm":            func(c *EmbeddingModelConfig) { c.TPM = 600_000 },
		"max attempts":   func(c *EmbeddingModelConfig) { c.MaxAttempts = 8 },
		"backoff cap":    func(c *EmbeddingModelConfig) { c.BackoffMaxMillis = 60_000 },
		"batch size":     func(c *EmbeddingModelConfig) { c.BatchSize = 1 },
		"max concurrent": func(c *EmbeddingModelConfig) { c.MaxConcurrent = 8 },
		"timeout":        func(c *EmbeddingModelConfig) { c.TimeoutMillis = 60_000 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			profile, err := cfg.Profile()
			if err != nil {
				t.Fatal(err)
			}
			if profile.Fingerprint != baseProfile.Fingerprint {
				t.Fatalf("fingerprint changed with ops parameter %s: %s vs %s",
					name, profile.Fingerprint, baseProfile.Fingerprint)
			}
		})
	}
	identityChanged := base
	identityChanged.Model = "text-embedding-v5"
	profile, err := identityChanged.Profile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Fingerprint == baseProfile.Fingerprint {
		t.Fatal("fingerprint must change when the model identity changes")
	}
}
