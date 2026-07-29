package config

import "testing"

func TestAuthConfigValidate(t *testing.T) {
	valid := AuthConfig{
		AllowedOrigins:         []string{"http://localhost:5173"},
		SessionIdleMinutes:     120,
		SessionAbsoluteMinutes: 720,
	}
	tests := []struct {
		name    string
		mutate  func(*AuthConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*AuthConfig) {}},
		{name: "missing origins", mutate: func(c *AuthConfig) { c.AllowedOrigins = nil }, wantErr: true},
		{name: "origin with path", mutate: func(c *AuthConfig) { c.AllowedOrigins = []string{"http://localhost:5173/app"} }, wantErr: true},
		{name: "unsupported origin scheme", mutate: func(c *AuthConfig) { c.AllowedOrigins = []string{"file://local"} }, wantErr: true},
		{name: "idle must be positive", mutate: func(c *AuthConfig) { c.SessionIdleMinutes = 0 }, wantErr: true},
		{name: "absolute must be positive", mutate: func(c *AuthConfig) { c.SessionAbsoluteMinutes = 0 }, wantErr: true},
		{name: "idle cannot exceed absolute", mutate: func(c *AuthConfig) { c.SessionIdleMinutes = 721 }, wantErr: true},
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
