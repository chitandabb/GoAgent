package config

import "testing"

func validMinIOConfig() MinIOConfig {
	return MinIOConfig{
		Enabled: true, Endpoint: "127.0.0.1:9000",
		AccessKeyEnv: "MESGUARD_MINIO_ACCESS_KEY", SecretKeyEnv: "MESGUARD_MINIO_SECRET_KEY",
		Region: "us-east-1", AttachmentBucket: "mesguard-attachments",
		KnowledgeSourceBucket: "mesguard-knowledge-sources", AutoCreateBuckets: true,
		TimeoutMillis: 5_000, MaxObjectBytes: 50 * 1024 * 1024,
	}
}

func TestMinIOConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*MinIOConfig)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "disabled accepts empty", mutate: func(c *MinIOConfig) { *c = MinIOConfig{} }, valid: true},
		{name: "scheme is not accepted", mutate: func(c *MinIOConfig) { c.Endpoint = "http://127.0.0.1:9000" }},
		{name: "missing port", mutate: func(c *MinIOConfig) { c.Endpoint = "minio" }},
		{name: "non numeric port", mutate: func(c *MinIOConfig) { c.Endpoint = "minio:http" }},
		{name: "invalid access key env", mutate: func(c *MinIOConfig) { c.AccessKeyEnv = "access-key" }},
		{name: "invalid bucket", mutate: func(c *MinIOConfig) { c.AttachmentBucket = "MESGUARD" }},
		{name: "ip bucket", mutate: func(c *MinIOConfig) { c.AttachmentBucket = "127.0.0.1" }},
		{name: "same buckets", mutate: func(c *MinIOConfig) { c.KnowledgeSourceBucket = c.AttachmentBucket }},
		{name: "invalid timeout", mutate: func(c *MinIOConfig) { c.TimeoutMillis = 999 }},
		{name: "object size over limit", mutate: func(c *MinIOConfig) { c.MaxObjectBytes++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validMinIOConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			if err := cfg.Validate(); (err == nil) != tt.valid {
				t.Fatalf("Validate error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestMinIOConfigCredentialsReadConfiguredEnvironment(t *testing.T) {
	t.Setenv("MESGUARD_MINIO_ACCESS_KEY_TEST", " access ")
	t.Setenv("MESGUARD_MINIO_SECRET_KEY_TEST", " secret ")
	cfg := MinIOConfig{AccessKeyEnv: "MESGUARD_MINIO_ACCESS_KEY_TEST", SecretKeyEnv: "MESGUARD_MINIO_SECRET_KEY_TEST"}
	accessKey, err := cfg.AccessKey()
	if err != nil || accessKey != "access" {
		t.Fatalf("AccessKey() = %q, error = %v", accessKey, err)
	}
	secretKey, err := cfg.SecretKey()
	if err != nil || secretKey != "secret" {
		t.Fatalf("SecretKey() = %q, error = %v", secretKey, err)
	}
}
