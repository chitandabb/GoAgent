package api_test

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIYAMLParsesAndContainsTaskControlPaths(t *testing.T) {
	content, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var document struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if document.OpenAPI == "" {
		t.Fatal("openapi version is missing")
	}
	for _, path := range []string{
		"/api/v1/diagnosis-tasks/{taskId}/events",
		"/api/v1/diagnosis-tasks/{taskId}/cancel",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("OpenAPI path %q is missing", path)
		}
	}
}
