package searxng

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	platformconfig "github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/directweb"
	platformfirecrawl "github.com/chitandabb/GoAgent/internal/platform/firecrawl"
	"github.com/chitandabb/GoAgent/internal/webresearch"
	"github.com/joho/godotenv"
)

var liveQueryPolicy = func() *webresearch.QueryPolicy {
	p, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		panic(err)
	}
	return p
}()

var liveSearchClient = func() *Client {
	c, err := New(Config{
		BaseURL: "http://127.0.0.1:8081", Timeout: 30 * time.Second,
		MaxResponseBytes: 2 * 1024 * 1024,
	})
	if err != nil {
		panic(err)
	}
	return c
}()

func requireLiveSearxng(t *testing.T) {
	t.Helper()
	if os.Getenv("MESGUARD_RUN_LIVE_SEARXNG_TESTS") != "1" {
		t.Skip("set MESGUARD_RUN_LIVE_SEARXNG_TESTS=1 to run external SearXNG checks")
	}
}

func liveURLPolicy(t *testing.T) *webresearch.URLPolicy {
	t.Helper()
	if os.Getenv("MESGUARD_WEBSEARCH_TRANSPARENT_EGRESS_CIDRS") == "" {
		_ = godotenv.Load(filepath.Join("..", "..", "..", ".env"))
	}
	prefixes, err := (platformconfig.WebSearchConfig{
		TransparentEgressCIDRsEnv: "MESGUARD_WEBSEARCH_TRANSPARENT_EGRESS_CIDRS",
	}).TransparentEgressCIDRs()
	if err != nil {
		t.Fatalf("transparent egress CIDRs: %v", err)
	}
	return webresearch.NewURLPolicy(nil, webresearch.WithTransparentEgressResolverCIDRs(prefixes))
}

// TestSearxngLiveSearch verifies the self-hosted SearXNG endpoint returns
// usable public results through the webresearch pipeline.
func TestSearxngLiveSearch(t *testing.T) {
	requireLiveSearxng(t)
	urlPolicy := liveURLPolicy(t)
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := webresearch.NewService(webresearch.ServiceConfig{
		SearchProvider: liveSearchClient, ContentProvider: nullContentProvider{urlPolicy: urlPolicy},
		QueryPolicy: queryPolicy, URLPolicy: urlPolicy,
		MaxResults: 5, MaxFetchedPages: 1, MaxPageChars: 4000, MaxRounds: 1,
		OfficialDomains: []string{"postgresql.org", "go.dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx, err = service.WithRunContext(ctx, "searxng-live-smoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(ctx, "searxng-live-smoke", "PostgreSQL deadlock detection documentation", 5)
	if err != nil {
		t.Fatalf("live search: %v", err)
	}
	t.Logf("query=%q redacted=%v results=%d omitted=%d", results.Query, results.Redacted, len(results.Results), results.OmittedResults)
	for i, r := range results.Results {
		t.Logf("  [%d] tier=%s %s | %s", i, r.SourceTier, r.Title, r.URL)
	}
	if len(results.Results) == 0 {
		t.Fatal("live search returned zero safe results")
	}
}

// TestSearxngSearchFirecrawlFetch verifies the hybrid pipeline:
// SearXNG discovery (free) + Firecrawl content fetch (metered, at most
// maxFetchedPages pages per run). Set FIRECRAWL_API_KEY in the environment.
func TestSearxngSearchFirecrawlFetch(t *testing.T) {
	requireLiveSearxng(t)
	if os.Getenv("FIRECRAWL_API_KEY") == "" {
		_ = godotenv.Load(filepath.Join("..", "..", "..", ".env"))
	}
	apiKey := os.Getenv("FIRECRAWL_API_KEY")
	if apiKey == "" {
		t.Skip("FIRECRAWL_API_KEY is not configured; skip metered fetch leg")
	}
	urlPolicy := liveURLPolicy(t)
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contentClient, err := platformfirecrawl.New(platformfirecrawl.Config{
		BaseURL: "https://api.firecrawl.dev", APIKey: apiKey,
		Timeout: 60 * time.Second, MaxResponseBytes: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := webresearch.NewService(webresearch.ServiceConfig{
		SearchProvider: liveSearchClient, ContentProvider: contentClient,
		QueryPolicy: queryPolicy, URLPolicy: urlPolicy,
		MaxResults: 3, MaxFetchedPages: 1, MaxPageChars: 4000, MaxRounds: 1,
		OfficialDomains: []string{"postgresql.org", "go.dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ctx, err = service.WithRunContext(ctx, "searxng-hybrid-smoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(ctx, "searxng-hybrid-smoke", "PostgreSQL deadlock detection documentation", 3)
	if err != nil {
		t.Fatalf("live search: %v", err)
	}
	t.Logf("query=%q results=%d", results.Query, len(results.Results))
	for i, r := range results.Results {
		t.Logf("  [%d] %s | %s", i, r.Title, r.URL)
	}
	if len(results.Results) == 0 {
		t.Fatal("hybrid search returned zero safe results")
	}
	page, err := service.Fetch(ctx, "searxng-hybrid-smoke", results.Results[0].ResultID)
	if err != nil {
		t.Fatalf("live fetch: %v", err)
	}
	t.Logf("page url=%s title=%q chars=%d sha=%s", page.URL, page.Title,
		len([]rune(page.ContentText)), page.ContentSHA256)
	if page.URL == "" || page.Title == "" || page.ContentText == "" || page.ContentSHA256 == "" {
		t.Fatalf("live page contract incomplete: url=%q title=%q chars=%d hash=%t",
			page.URL, page.Title, len([]rune(page.ContentText)), page.ContentSHA256 != "")
	}
}

// TestSearxngSearchDirectFetch verifies the fully self-hosted pipeline:
// SearXNG discovery + direct public fetch, with zero third-party metered
// credits. Requires working public DNS on the machine.
func TestSearxngSearchDirectFetch(t *testing.T) {
	requireLiveSearxng(t)
	urlPolicy := liveURLPolicy(t)
	queryPolicy, err := webresearch.NewQueryPolicy(webresearch.QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contentClient, err := directweb.New(directweb.Config{
		Timeout: 30 * time.Second, MaxResponseBytes: 2 * 1024 * 1024,
		ValidateRedirect: func(ctx context.Context, rawURL string) error {
			_, err := urlPolicy.Validate(ctx, rawURL)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := webresearch.NewService(webresearch.ServiceConfig{
		SearchProvider: liveSearchClient, ContentProvider: contentClient,
		QueryPolicy: queryPolicy, URLPolicy: urlPolicy,
		MaxResults: 3, MaxFetchedPages: 1, MaxPageChars: 4000, MaxRounds: 1,
		OfficialDomains: []string{"postgresql.org", "go.dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx, err = service.WithRunContext(ctx, "searxng-direct-smoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(ctx, "searxng-direct-smoke", "PostgreSQL deadlock detection documentation", 3)
	if err != nil {
		t.Fatalf("live search: %v", err)
	}
	t.Logf("query=%q results=%d", results.Query, len(results.Results))
	for i, r := range results.Results {
		t.Logf("  [%d] %s | %s", i, r.Title, r.URL)
	}
	if len(results.Results) == 0 {
		t.Fatal("direct search returned zero safe results")
	}
	page, err := service.Fetch(ctx, "searxng-direct-smoke", results.Results[0].ResultID)
	if err != nil {
		t.Fatalf("live direct fetch: %v", err)
	}
	t.Logf("page url=%s title=%q chars=%d redirected=%v sha=%s", page.URL, page.Title,
		len([]rune(page.ContentText)), page.Redirected, page.ContentSHA256)
	if page.URL == "" || page.Title == "" || page.ContentText == "" || page.ContentSHA256 == "" {
		t.Fatalf("live page contract incomplete: url=%q title=%q chars=%d hash=%t",
			page.URL, page.Title, len([]rune(page.ContentText)), page.ContentSHA256 != "")
	}
}

type nullContentProvider struct{ urlPolicy *webresearch.URLPolicy }

func (p nullContentProvider) Fetch(ctx context.Context, target webresearch.PublicURL) (webresearch.ProviderPage, error) {
	return webresearch.ProviderPage{URL: target.String(), Title: target.Domain()}, nil
}
