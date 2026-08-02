package config

import "testing"

func TestGitHubMCPConfigValidate(t *testing.T) {
	valid := GitHubMCPConfig{
		Enabled: true, Endpoint: "https://api.githubcopilot.com/mcp/",
		TokenEnv: "MESGUARD_GITHUB_MCP_TOKEN", TimeoutMillis: 30_000,
	}
	tests := []struct {
		name    string
		mutate  func(*GitHubMCPConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*GitHubMCPConfig) {}},
		{name: "disabled accepts empty", mutate: func(c *GitHubMCPConfig) { *c = GitHubMCPConfig{} }},
		{name: "reject insecure remote endpoint", mutate: func(c *GitHubMCPConfig) { c.Endpoint = "http://example.com/mcp" }, wantErr: true},
		{name: "allow localhost HTTP", mutate: func(c *GitHubMCPConfig) { c.Endpoint = "http://127.0.0.1:8080/mcp" }},
		{name: "no repository scope is configured", mutate: func(*GitHubMCPConfig) {}},
		{name: "reject invalid token env", mutate: func(c *GitHubMCPConfig) { c.TokenEnv = "token-value" }, wantErr: true},
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
