package config

import "testing"

func TestObservabilityConfigDisabledRequiresNoEndpointOrCredentials(t *testing.T) {
	if err := (ObservabilityConfig{}).Validate(); err != nil {
		t.Fatalf("disabled Validate(): %v", err)
	}
}

func TestObservabilityConfigValidatesEnabledExporter(t *testing.T) {
	valid := ObservabilityConfig{
		Enabled: true, ServiceName: "mesguard-api", Environment: "development",
		OTLPEndpoint: "http://localhost:3000/api/public/otel/v1/traces",
		SampleRatio:  1, ExportTimeoutMillis: 3000, ErrorLogIntervalMillis: 60_000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}

	invalid := valid
	invalid.SampleRatio = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected zero sampling ratio to be rejected when tracing is enabled")
	}
}

func TestObservabilityHeadersComeFromExplicitEnvironmentVariable(t *testing.T) {
	t.Setenv("MESGUARD_TEST_OTEL_HEADERS", `{"Authorization":"Basic redacted","x-langfuse-ingestion-version":"4"}`)
	cfg := ObservabilityConfig{HeadersEnv: "MESGUARD_TEST_OTEL_HEADERS"}
	headers, err := cfg.Headers()
	if err != nil {
		t.Fatalf("Headers(): %v", err)
	}
	if headers["Authorization"] != "Basic redacted" || headers["x-langfuse-ingestion-version"] != "4" {
		t.Fatalf("unexpected headers: %#v", headers)
	}
}
