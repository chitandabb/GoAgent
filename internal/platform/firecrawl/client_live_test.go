package firecrawl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/webresearch"
	"github.com/joho/godotenv"
)

func TestFirecrawlLiveSearchAndScrape(t *testing.T) {
	if os.Getenv("MESGUARD_TEST_FIRECRAWL_LIVE") != "1" {
		t.Skip("set MESGUARD_TEST_FIRECRAWL_LIVE=1 to spend one search and one scrape request")
	}
	if os.Getenv("FIRECRAWL_API_KEY") == "" {
		_ = godotenv.Load(filepath.Join("..", "..", "..", ".env"))
	}
	apiKey := os.Getenv("FIRECRAWL_API_KEY")
	if apiKey == "" {
		t.Fatal("FIRECRAWL_API_KEY is not configured")
	}
	client, err := New(Config{
		BaseURL: "https://api.firecrawl.dev", APIKey: apiKey,
		Timeout: 30 * time.Second, MaxResponseBytes: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := webresearch.NewService(webresearch.ServiceConfig{
		Provider: client, QueryPolicy: queryPolicy, URLPolicy: webresearch.NewURLPolicy(nil),
		MaxResults: 1, MaxFetchedPages: 1, MaxPageChars: 2000, MaxRounds: 1,
		OfficialDomains: []string{"go.dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx, err = service.WithRunContext(ctx, "live-smoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(ctx, "live-smoke", "Go context cancellation official documentation", 1)
	if err != nil {
		t.Fatalf("live search: %v", err)
	}
	if len(results.Results) != 1 {
		t.Fatalf("live search returned %d safe results", len(results.Results))
	}
	page, err := service.Fetch(ctx, "live-smoke", results.Results[0].ResultID)
	if err != nil {
		t.Fatalf("live scrape: %v", err)
	}
	if page.URL == "" || page.Title == "" || page.ContentText == "" || page.ContentSHA256 == "" {
		t.Fatalf("live page contract is incomplete: url=%q title=%q chars=%d hash=%t",
			page.URL, page.Title, len([]rune(page.ContentText)), page.ContentSHA256 != "")
	}
}
