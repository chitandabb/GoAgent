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
		"/api/v1/diagnosis-tasks/{taskId}/report",
		"/api/v1/admin/diagnosis-tasks/{taskId}/recover",
		"/api/v1/admin/knowledge-documents",
		"/api/v1/admin/knowledge-documents/{documentId}/versions",
		"/api/v1/admin/knowledge-ingestion-tasks/{taskId}",
		"/api/v1/admin/knowledge-ingestion-tasks/{taskId}/cancel",
		"/api/v1/conversations/{conversationId}/turns/{turnId}",
		"/api/v1/conversations/{conversationId}/turns/{turnId}/events",
		"/api/v1/auth/change-password",
		"/api/v1/admin/users",
		"/api/v1/admin/users/{userId}",
		"/api/v1/admin/users/{userId}/reset-password",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("OpenAPI path %q is missing", path)
		}
	}
	taskList := document.Paths["/api/v1/diagnosis-tasks"]["get"]
	taskListOperation, ok := taskList.(map[string]any)
	if !ok {
		t.Fatal("diagnosis task list GET operation is invalid")
	}
	if _, ok := taskListOperation["parameters"].([]any); !ok {
		t.Fatal("diagnosis task list must document query parameters")
	}
	upload := document.Paths["/api/v1/admin/knowledge-documents"]["post"]
	uploadOperation, ok := upload.(map[string]any)
	if !ok {
		t.Fatal("knowledge upload POST operation is invalid")
	}
	requestBody, ok := uploadOperation["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("knowledge upload request body is missing")
	}
	uploadContent, ok := requestBody["content"].(map[string]any)
	if !ok || uploadContent["multipart/form-data"] == nil {
		t.Fatal("knowledge upload must document multipart/form-data")
	}
	events := document.Paths["/api/v1/diagnosis-tasks/{taskId}/events"]["get"]
	operation, ok := events.(map[string]any)
	if !ok {
		t.Fatal("task events GET operation is invalid")
	}
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatal("task events responses are invalid")
	}
	okResponse, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatal("task events 200 response is missing")
	}
	contentMap, ok := okResponse["content"].(map[string]any)
	if !ok || contentMap["application/json"] == nil || contentMap["text/event-stream"] == nil {
		t.Fatal("task events must document JSON and text/event-stream representations")
	}
	conversationEvents := document.Paths["/api/v1/conversations/{conversationId}/turns/{turnId}/events"]["get"]
	conversationOperation, ok := conversationEvents.(map[string]any)
	if !ok {
		t.Fatal("conversation turn events GET operation is invalid")
	}
	conversationResponses, ok := conversationOperation["responses"].(map[string]any)
	if !ok {
		t.Fatal("conversation turn events responses are invalid")
	}
	conversationOK, ok := conversationResponses["200"].(map[string]any)
	if !ok {
		t.Fatal("conversation turn events 200 response is missing")
	}
	conversationContent, ok := conversationOK["content"].(map[string]any)
	if !ok || conversationContent["application/json"] == nil || conversationContent["text/event-stream"] == nil {
		t.Fatal("conversation turn events must document JSON and text/event-stream representations")
	}
}
