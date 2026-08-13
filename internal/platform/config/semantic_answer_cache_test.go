package config

import "testing"

func TestSemanticAnswerCacheConfigValidate(t *testing.T) {
	valid := SemanticAnswerCacheConfig{
		Enabled: true, Provider: "postgres", TTLSeconds: 86400, TTLJitterRatio: 0.1,
		MaxRecords: 1000, MaxAnswerBytes: 16 * 1024, MaxCitations: 8,
		LookupTimeoutMillis: 100, WriteTimeoutMillis: 200,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*SemanticAnswerCacheConfig)
	}{
		{name: "unknown provider", mutate: func(c *SemanticAnswerCacheConfig) { c.Provider = "redis" }},
		{name: "short ttl", mutate: func(c *SemanticAnswerCacheConfig) { c.TTLSeconds = 59 }},
		{name: "lookup timeout", mutate: func(c *SemanticAnswerCacheConfig) { c.LookupTimeoutMillis = 9 }},
		{name: "write timeout", mutate: func(c *SemanticAnswerCacheConfig) { c.WriteTimeoutMillis = 5001 }},
		{name: "jitter", mutate: func(c *SemanticAnswerCacheConfig) { c.TTLJitterRatio = 0.21 }},
		{name: "capacity", mutate: func(c *SemanticAnswerCacheConfig) { c.MaxRecords = 0 }},
		{name: "answer bytes", mutate: func(c *SemanticAnswerCacheConfig) { c.MaxAnswerBytes = 1023 }},
		{name: "citations", mutate: func(c *SemanticAnswerCacheConfig) { c.MaxCitations = 9 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := valid
			test.mutate(&current)
			if err := current.Validate(); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
	if err := (SemanticAnswerCacheConfig{}).Validate(); err != nil {
		t.Fatalf("disabled config should be optional: %v", err)
	}
}
