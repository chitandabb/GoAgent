package config

import (
	"strings"
	"testing"
)

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

func TestSemanticAnswerCacheConfigAcceptsRedisStackProvider(t *testing.T) {
	valid := SemanticAnswerCacheConfig{
		Enabled: true, Provider: "redis-stack", TTLSeconds: 86400, TTLJitterRatio: 0.1,
		MaxRecords: 1000, MaxAnswerBytes: 16 * 1024, MaxCitations: 8,
		LookupTimeoutMillis: 100, WriteTimeoutMillis: 200,
		RedisStack: SemanticAnswerCacheRedisStackConfig{
			Host: "127.0.0.1", Port: 6380, Database: 0,
			IndexName: "mesguard_semantic_cache_v1", KeyPrefix: "mesguard:semantic-cache:v1:",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid redis stack config: %v", err)
	}
	for name, mutate := range map[string]func(*SemanticAnswerCacheConfig){
		"host":        func(c *SemanticAnswerCacheConfig) { c.RedisStack.Host = "" },
		"port":        func(c *SemanticAnswerCacheConfig) { c.RedisStack.Port = 0 },
		"database":    func(c *SemanticAnswerCacheConfig) { c.RedisStack.Database = 16 },
		"passwordEnv": func(c *SemanticAnswerCacheConfig) { c.RedisStack.PasswordEnv = "not valid" },
		"indexName":   func(c *SemanticAnswerCacheConfig) { c.RedisStack.IndexName = "bad index" },
		"keyPrefix":   func(c *SemanticAnswerCacheConfig) { c.RedisStack.KeyPrefix = "" },
	} {
		t.Run(name, func(t *testing.T) {
			current := valid
			mutate(&current)
			if err := current.Validate(); err == nil {
				t.Fatal("invalid redis stack config was accepted")
			}
		})
	}
}

func TestSemanticAnswerCacheConfigBindsSemanticThresholdToProfile(t *testing.T) {
	valid := SemanticAnswerCacheConfig{
		Enabled: true, Provider: "postgres", TTLSeconds: 86400, TTLJitterRatio: 0.1,
		MaxRecords: 1000, MaxAnswerBytes: 16 * 1024, MaxCitations: 8,
		LookupTimeoutMillis: 100, WriteTimeoutMillis: 200,
		SemanticEnabled: true, SemanticMinimumSimilarity: 0.94, SemanticCandidateLimit: 5,
		SemanticEmbeddingTimeoutMillis: 1500, SemanticProfileFingerprint: strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid semantic config: %v", err)
	}
	for name, mutate := range map[string]func(*SemanticAnswerCacheConfig){
		"threshold":   func(c *SemanticAnswerCacheConfig) { c.SemanticMinimumSimilarity = 0.49 },
		"candidates":  func(c *SemanticAnswerCacheConfig) { c.SemanticCandidateLimit = 21 },
		"timeout":     func(c *SemanticAnswerCacheConfig) { c.SemanticEmbeddingTimeoutMillis = 9 },
		"fingerprint": func(c *SemanticAnswerCacheConfig) { c.SemanticProfileFingerprint = "uncalibrated" },
	} {
		t.Run(name, func(t *testing.T) {
			current := valid
			mutate(&current)
			if err := current.Validate(); err == nil {
				t.Fatal("invalid semantic config was accepted")
			}
		})
	}
}
