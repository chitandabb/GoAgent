package config

import "testing"

func TestWebSearchConfigValidate(t *testing.T) {
	valid := WebSearchConfig{
		Enabled: true, Provider: "firecrawl", BaseURL: "https://api.firecrawl.dev",
		APIKeyEnv: "FIRECRAWL_API_KEY", TimeoutMillis: 30_000,
		MaxResults: 5, MaxFetchedPages: 3, MaxPageChars: 20_000,
		MaxRounds: 2, MaxResponseBytes: 2 * 1024 * 1024,
	}
	tests := []struct {
		name    string
		mutate  func(*WebSearchConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*WebSearchConfig) {}},
		{name: "disabled accepts empty", mutate: func(c *WebSearchConfig) { *c = WebSearchConfig{} }},
		{name: "reject unknown provider", mutate: func(c *WebSearchConfig) { c.Provider = "other" }, wantErr: true},
		{name: "reject insecure remote URL", mutate: func(c *WebSearchConfig) { c.BaseURL = "http://example.com" }, wantErr: true},
		{name: "allow localhost HTTP", mutate: func(c *WebSearchConfig) { c.BaseURL = "http://127.0.0.1:3002" }},
		{name: "reject invalid API key env", mutate: func(c *WebSearchConfig) { c.APIKeyEnv = "secret-value" }, wantErr: true},
		{name: "reject fetched pages over results", mutate: func(c *WebSearchConfig) { c.MaxFetchedPages = 6 }, wantErr: true},
		{name: "reject unbounded page size", mutate: func(c *WebSearchConfig) { c.MaxPageChars = 100_001 }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWebSearchConfigAPIKeyReadsConfiguredEnvironment(t *testing.T) {
	t.Setenv("FIRECRAWL_API_KEY_TEST", " test-key ")
	value, err := (WebSearchConfig{APIKeyEnv: "FIRECRAWL_API_KEY_TEST"}).APIKey()
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if value != "test-key" {
		t.Fatalf("APIKey() = %q", value)
	}
}
