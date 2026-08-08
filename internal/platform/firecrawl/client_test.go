package firecrawl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/webresearch"
)

func TestClientSearchAndScrapeContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer test-key" ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: method=%s auth=%q content-type=%q", request.Method,
				request.Header.Get("Authorization"), request.Header.Get("Content-Type"))
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch request.URL.Path {
		case "/v2/search":
			var payload searchRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Query != "PostgreSQL timeout 258" || payload.Limit != 2 {
				t.Fatalf("search payload=%+v err=%v", payload, err)
			}
			_, _ = writer.Write([]byte(`{"success":true,"data":{"web":[{"url":"https://8.8.8.8/guide","title":"Guide","description":"Timeout help","publishedDate":"2026-08-01"}]}}`))
		case "/v2/scrape":
			var payload scrapeRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.URL != "https://8.8.8.8/guide" ||
				!payload.OnlyMainContent || len(payload.Formats) != 1 || payload.Formats[0] != "markdown" {
				t.Fatalf("scrape payload=%+v err=%v", payload, err)
			}
			_, _ = writer.Write([]byte(`{"success":true,"data":{"markdown":"# Public guide","metadata":{"title":"Guide","sourceURL":"https://8.8.8.8/guide","publishedTime":"2026-08-01","modifiedTime":"2026-08-02","statusCode":200}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newClientForTest(t, server.URL, server.Client(), 64*1024)
	queryPolicy, _ := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	query, err := queryPolicy.Sanitize("PostgreSQL timeout 258", nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Search(context.Background(), query, 2)
	if err != nil || len(results) != 1 || results[0].PublishedTime != "2026-08-01" {
		t.Fatalf("Search=%+v err=%v", results, err)
	}
	target, err := webresearch.NewURLPolicy(nil).Validate(context.Background(), results[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Scrape(context.Background(), target)
	if err != nil || page.Markdown != "# Public guide" || page.ModifiedTime != "2026-08-02" {
		t.Fatalf("Scrape=%+v err=%v", page, err)
	}
}

func TestClientMapsRateLimitAndCredentialErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, want: webresearch.ErrProviderRateLimited},
		{name: "unauthorized", status: http.StatusUnauthorized, want: webresearch.ErrProviderUnauthorized},
		{name: "server", status: http.StatusServiceUnavailable, want: webresearch.ErrProviderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"success":false,"error":"provider detail must not escape"}`))
			}))
			defer server.Close()
			client := newClientForTest(t, server.URL, server.Client(), 64*1024)
			query := publicQueryForTest(t)
			_, err := client.Search(context.Background(), query, 1)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "provider detail") {
				t.Fatalf("Search error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestClientRejectsOversizedOrNonJSONResponse(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "oversized", contentType: "application/json", body: strings.Repeat("x", 64*1024+1)},
		{name: "html", contentType: "text/html", body: `{}`},
		{name: "trailing JSON", contentType: "application/json", body: `{"success":true,"data":{"web":[]}} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newClientForTest(t, server.URL, server.Client(), 64*1024)
			_, err := client.Search(context.Background(), publicQueryForTest(t), 1)
			if !errors.Is(err, webresearch.ErrInvalidProviderData) {
				t.Fatalf("Search error=%v", err)
			}
		})
	}
}

func newClientForTest(t *testing.T, baseURL string, httpClient *http.Client, maxBytes int64) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL: baseURL, APIKey: "test-key", Timeout: 5 * time.Second,
		MaxResponseBytes: maxBytes, HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func publicQueryForTest(t *testing.T) webresearch.PublicQuery {
	t.Helper()
	policy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	query, err := policy.Sanitize("PostgreSQL timeout guidance", nil)
	if err != nil {
		t.Fatal(err)
	}
	return query
}
