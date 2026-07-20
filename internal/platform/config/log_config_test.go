package config

import "testing"

func TestLogConfigValidate(t *testing.T) {
	valid := LogConfig{
		Level:       "info",
		Format:      "json",
		Environment: "test",
	}
	tests := []struct {
		name    string
		mutate  func(*LogConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*LogConfig) {}},
		{name: "invalid level", mutate: func(c *LogConfig) { c.Level = "trace" }, wantErr: true},
		{name: "invalid format", mutate: func(c *LogConfig) { c.Format = "xml" }, wantErr: true},
		{name: "missing environment", mutate: func(c *LogConfig) { c.Environment = "" }, wantErr: true},
		{
			name: "file output requires rotation settings",
			mutate: func(c *LogConfig) {
				c.EnableFile = true
			},
			wantErr: true,
		},
		{
			name: "valid file output",
			mutate: func(c *LogConfig) {
				c.EnableFile = true
				c.OutputDir = "logs"
				c.MaxSize = 100
				c.MaxAge = 30
				c.MaxBackups = 10
			},
		},
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
