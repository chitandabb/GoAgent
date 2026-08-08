package searxng

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/webresearch"
)

func TestClientSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search" || request.URL.Query().Get("q") != "Go context cancellation official documentation" || request.URL.Query().Get("format") != "json" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"url":"https://go.dev/doc/","title":"Go docs","content":"Official documentation","publishedDate":"2026-01-01"},{"url":"https://pkg.go.dev/context","title":"context","content":"Package docs"}]}`))
	}))
	defer server.Close()
	client, err := New(Config{
		BaseURL: server.URL, Timeout: 2 * time.Second, MaxResponseBytes: 128 * 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := client.Search(context.Background(), publicQueryForTest(t), 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go docs" || results[0].Description != "Official documentation" {
		t.Fatalf("results = %+v", results)
	}
}

func TestClientMapsRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New(Config{
		BaseURL: server.URL, Timeout: 2 * time.Second, MaxResponseBytes: 128 * 1024,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Search(context.Background(), publicQueryForTest(t), 1)
	if !errors.Is(err, webresearch.ErrProviderRateLimited) {
		t.Fatalf("Search error = %v, want rate limited", err)
	}
}

func publicQueryForTest(t *testing.T) webresearch.PublicQuery {
	t.Helper()
	policy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{
		MaxInputRunes: 1024, MaxOutputRunes: 384, MinOutputRunes: 8,
	})
	if err != nil {
		t.Fatalf("NewQueryPolicy: %v", err)
	}
	query, err := policy.Sanitize("Go context cancellation official documentation", nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	return query
}
