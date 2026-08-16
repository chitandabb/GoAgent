package dashscopeembedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestClientEmbedPreservesTextIndexesAndNormalizes(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("authorization header is missing")
		}
		var request requestPayload
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Parameters.TextType != "query" || request.Parameters.Dimension != 1024 || len(request.Input.Texts) != 2 {
			t.Errorf("unexpected request: %+v", request)
		}
		first := make([]float32, 1024)
		second := make([]float32, 1024)
		first[0], second[1] = 2, 3
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": []any{
				map[string]any{"text_index": 1, "embedding": second},
				map[string]any{"text_index": 0, "embedding": first},
			}},
			"usage": map[string]any{"total_tokens": 7},
		})
	}))
	defer server.Close()

	cfg := testConfig(server.URL + "/embed")
	client, err := NewClient(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Embed(context.Background(), knowledge.EmbeddingRequest{
		Texts: []string{"one", "two"}, InputType: knowledge.EmbeddingInputQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.TotalTokens != 7 || result.Vectors[0][0] != 1 || result.Vectors[1][1] != 1 {
		t.Fatalf("unexpected result: tokens=%d first=%v second=%v",
			result.Usage.TotalTokens, result.Vectors[0][0], result.Vectors[1][1])
	}
}

func TestClientEmbedRejectsDuplicateIndex(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		vector := make([]float32, 1024)
		vector[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": []any{
				map[string]any{"text_index": 0, "embedding": vector},
				map[string]any{"text_index": 0, "embedding": vector},
			}},
			"usage": map[string]any{"total_tokens": 1},
		})
	}))
	defer server.Close()
	client, err := NewClient(testConfig(server.URL+"/embed"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(context.Background(), knowledge.EmbeddingRequest{
		Texts: []string{"one", "two"}, InputType: knowledge.EmbeddingInputDocument,
	}); err == nil {
		t.Fatal("expected invalid provider response")
	}
}

func testConfig(endpoint string) config.EmbeddingModelConfig {
	return config.EmbeddingModelConfig{
		Enabled: true, ProfileKey: "knowledge-v1", Provider: "dashscope", Endpoint: endpoint,
		APIKeyEnv: "TEST_DASHSCOPE_KEY", Model: "text-embedding-v4", Dimensions: 1024,
		DistanceMetric: "cosine", QueryInputType: "query", DocumentInputType: "document",
		Normalize: true, ConfigVersion: "embedding-v1", BatchSize: 10, MaxConcurrent: 2,
		TimeoutMillis: 30000, RPM: 900, TPM: 600_000, MaxAttempts: 3, BackoffMaxMillis: 10_000,
	}
}
