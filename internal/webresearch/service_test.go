package webresearch

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type providerStub struct {
	searchResults []ProviderSearchResult
	page          ProviderPage
	searchErr     error
	scrapeErr     error
	searchCalls   int
	scrapeCalls   int
	lastQuery     string
	lastURL       string
}

func (p *providerStub) Search(_ context.Context, query PublicQuery, _ int) ([]ProviderSearchResult, error) {
	p.searchCalls++
	p.lastQuery = query.String()
	return append([]ProviderSearchResult(nil), p.searchResults...), p.searchErr
}

func (p *providerStub) Scrape(_ context.Context, target PublicURL) (ProviderPage, error) {
	p.scrapeCalls++
	p.lastURL = target.String()
	return p.page, p.scrapeErr
}

type resolverStub struct {
	addresses map[string][]netip.Addr
	err       error
}

func (r resolverStub) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]netip.Addr(nil), r.addresses[host]...), nil
}

func TestServiceSearchAuthorizesFetchAndCachesSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	provider := &providerStub{
		searchResults: []ProviderSearchResult{
			{URL: "https://docs.vendor.com/guide#section", Title: "Official guide", Description: "Public timeout guidance"},
			{URL: "http://127.0.0.1/private", Title: "private"},
			{URL: "https://docs.vendor.com/guide", Title: "duplicate"},
		},
		page: ProviderPage{
			URL: "https://docs.vendor.com/guide", Title: "Official guide",
			Markdown: strings.Repeat("公开技术说明", 300), PublishedTime: "2026-08-01",
		},
	}
	service := newServiceForTest(t, provider, 2, 1, 1000, func() time.Time { return now })
	ctx, err := service.WithRunContext(context.Background(), "user-1", []string{"InternalProduct"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.Search(ctx, "user-1", "PostgreSQL InternalProduct timeout code 258", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if provider.lastQuery != "PostgreSQL timeout code 258" || !response.Redacted || len(response.Results) != 1 ||
		response.OmittedResults != 2 || response.Results[0].SourceTier != SourceTierOfficial || !response.UntrustedContent {
		t.Fatalf("Search response = %+v, provider query=%q", response, provider.lastQuery)
	}
	page, err := service.Fetch(ctx, "user-1", response.Results[0].ResultID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if provider.scrapeCalls != 1 || page.FetchedAt != now || !page.Truncated || len([]rune(page.ContentText)) != 1000 ||
		page.ContentSHA256 == "" || !page.UntrustedContent || page.SourceTier != SourceTierOfficial {
		t.Fatalf("page = %+v", page)
	}
	cached, err := service.Fetch(ctx, "user-1", response.Results[0].ResultID)
	if err != nil || cached.ContentSHA256 != page.ContentSHA256 || provider.scrapeCalls != 1 {
		t.Fatalf("cached Fetch = %+v, err=%v, scrapeCalls=%d", cached, err, provider.scrapeCalls)
	}
	if _, err := service.Fetch(ctx, "user-1", "web_not_authorized"); !errors.Is(err, ErrResultNotAuthorized) {
		t.Fatalf("unauthorized Fetch error = %v", err)
	}
}

func TestServiceEnforcesRunIdentityAndBudgets(t *testing.T) {
	provider := &providerStub{searchResults: []ProviderSearchResult{
		{URL: "https://docs.vendor.com/one", Title: "one"},
		{URL: "https://trusted.vendor.com/two", Title: "two"},
	}}
	service := newServiceForTest(t, provider, 1, 1, 2000, nil)
	ctx, err := service.WithRunContext(context.Background(), "user-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(ctx, "user-2", "PostgreSQL timeout guidance", 2); !errors.Is(err, ErrRunUserMismatch) {
		t.Fatalf("user mismatch error = %v", err)
	}
	response, err := service.Search(ctx, "user-1", "PostgreSQL timeout guidance", 2)
	if err != nil || len(response.Results) != 2 {
		t.Fatalf("Search response=%+v err=%v", response, err)
	}
	if _, err := service.Search(ctx, "user-1", "PostgreSQL timeout retry", 2); !errors.Is(err, ErrSearchBudgetReached) {
		t.Fatalf("search budget error = %v", err)
	}
	provider.page = ProviderPage{URL: response.Results[0].URL, Title: "one", Markdown: "public page one"}
	if _, err := service.Fetch(ctx, "user-1", response.Results[0].ResultID); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if _, err := service.Fetch(ctx, "user-1", response.Results[1].ResultID); !errors.Is(err, ErrFetchBudgetReached) {
		t.Fatalf("fetch budget error = %v", err)
	}
}

func TestServiceRejectsUnsafeRedirectAndInvalidPage(t *testing.T) {
	provider := &providerStub{
		searchResults: []ProviderSearchResult{{URL: "https://docs.vendor.com/guide", Title: "guide"}},
		page:          ProviderPage{URL: "http://169.254.169.254/latest/meta-data", Markdown: "metadata"},
	}
	service := newServiceForTest(t, provider, 1, 1, 2000, nil)
	ctx, _ := service.WithRunContext(context.Background(), "user-1", nil)
	response, err := service.Search(ctx, "user-1", "PostgreSQL timeout guidance", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Fetch(ctx, "user-1", response.Results[0].ResultID); !errors.Is(err, ErrInvalidProviderData) {
		t.Fatalf("unsafe redirect error = %v", err)
	}
}

func newServiceForTest(
	t *testing.T,
	provider Provider,
	maxRounds, maxFetched, maxPageChars int,
	clock func() time.Time,
) *Service {
	t.Helper()
	queryPolicy, err := NewQueryPolicy(QueryPolicyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	urlPolicy := NewURLPolicy(resolverStub{addresses: map[string][]netip.Addr{
		"docs.vendor.com":    {netip.MustParseAddr("93.184.216.34")},
		"trusted.vendor.com": {netip.MustParseAddr("93.184.216.35")},
	}})
	service, err := NewService(ServiceConfig{
		Provider: provider, QueryPolicy: queryPolicy, URLPolicy: urlPolicy,
		MaxResults: 5, MaxFetchedPages: maxFetched, MaxPageChars: maxPageChars, MaxRounds: maxRounds,
		OfficialDomains: []string{"vendor.com"}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
