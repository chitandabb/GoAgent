package dashscopererank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/platform/config"
)

func TestClientRerankUsesIndexesWithoutEchoingDocuments(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_RERANK_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("authorization header is missing")
		}
		var request requestPayload
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "qwen3-rerank" || request.Input.Query != "timeout" ||
			len(request.Input.Documents) != 3 || request.Parameters.TopN != 2 || request.Parameters.ReturnDocuments {
			t.Errorf("unexpected request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"results": []any{
				map[string]any{"index": 2, "relevance_score": 0.94},
				map[string]any{"index": 0, "relevance_score": 0.71},
			}},
			"usage": map[string]any{"total_tokens": 21},
		})
	}))
	defer server.Close()
	client, err := NewClient(testConfig(server.URL+"/rerank"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Rerank(context.Background(), knowledge.RerankRequest{
		Query: "timeout", Documents: []knowledge.RerankDocument{{Content: "first"}, {Content: "second"}, {Content: "third"}}, TopN: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Index != 2 || result.Items[1].Index != 0 || result.Usage.TotalTokens != 21 {
		t.Fatalf("result = %+v", result)
	}
}

func TestClientRerankRejectsDuplicateProviderIndex(t *testing.T) {
	t.Setenv("TEST_DASHSCOPE_RERANK_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"results": []any{
				map[string]any{"index": 0, "relevance_score": 0.9},
				map[string]any{"index": 0, "relevance_score": 0.8},
			}},
			"usage": map[string]any{"total_tokens": 10},
		})
	}))
	defer server.Close()
	client, err := NewClient(testConfig(server.URL+"/rerank"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Rerank(context.Background(), knowledge.RerankRequest{
		Query: "query", Documents: []knowledge.RerankDocument{{Content: "first"}, {Content: "second"}}, TopN: 2,
	})
	if err == nil {
		t.Fatal("Rerank accepted duplicate provider indexes")
	}
}

func testConfig(endpoint string) config.RerankModelConfig {
	return config.RerankModelConfig{
		Enabled: true, Provider: "dashscope", Endpoint: endpoint,
		APIKeyEnv: "TEST_DASHSCOPE_RERANK_KEY", Model: "qwen3-rerank",
		MaxCandidates: 30, TimeoutMillis: 30_000,
	}
}
