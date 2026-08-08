package directweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/webresearch"
)

type resolverStub struct{}

func (resolverStub) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientFetchesHTMLAndValidatesRedirects(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/redirect":
			return responseForTest(request, http.StatusFound, "text/plain", "", "/page"), nil
		case "/page":
			body := `<html><head><title>Public docs</title><script>ignore()</script></head><body><nav>skip nav</nav><main><h1>Main heading</h1><p>Useful <strong>content</strong>.</p></main></body></html>`
			return responseForTest(request, http.StatusOK, "text/html; charset=utf-8", body, ""), nil
		default:
			return responseForTest(request, http.StatusNotFound, "text/plain", "missing", ""), nil
		}
	})}
	client, err := New(Config{
		Timeout: 2 * time.Second, MaxResponseBytes: 128 * 1024, HTTPClient: httpClient,
		ValidateRedirect: func(_ context.Context, rawURL string) error {
			if !strings.HasPrefix(rawURL, "https://example.com/") {
				return errors.New("unexpected redirect target")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page, err := client.Fetch(context.Background(), publicURLForTest(t, "https://example.com/redirect"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if page.URL != "https://example.com/page" || page.Title != "Public docs" {
		t.Fatalf("page metadata = %+v", page)
	}
	if !strings.Contains(page.Markdown, "Useful content.") || strings.Contains(page.Markdown, "ignore") || strings.Contains(page.Markdown, "skip nav") {
		t.Fatalf("unexpected extracted content: %q", page.Markdown)
	}
}

func TestClientRejectsUnsafeRedirectBeforeFetch(t *testing.T) {
	privateFetched := false
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/private" {
			privateFetched = true
		}
		return responseForTest(request, http.StatusFound, "text/plain", "", "/private"), nil
	})}
	client, err := New(Config{
		Timeout: 2 * time.Second, MaxResponseBytes: 128 * 1024, HTTPClient: httpClient,
		ValidateRedirect: func(_ context.Context, rawURL string) error {
			if strings.HasSuffix(rawURL, "/private") {
				return errors.New("private address")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Fetch(context.Background(), publicURLForTest(t, "https://example.com/redirect"))
	if !errors.Is(err, webresearch.ErrInvalidProviderData) {
		t.Fatalf("Fetch error = %v, want invalid provider data", err)
	}
	if privateFetched {
		t.Fatal("unsafe redirect target was fetched")
	}
}

func publicURLForTest(t *testing.T, rawURL string) webresearch.PublicURL {
	t.Helper()
	result, err := webresearch.NewURLPolicy(resolverStub{}).Validate(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("Validate(%q): %v", rawURL, err)
	}
	return result
}

func responseForTest(request *http.Request, status int, contentType, body, location string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}
