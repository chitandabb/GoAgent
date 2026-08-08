package searxng

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/webresearch"
)

type Config struct {
	BaseURL          string
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	maxResponseBytes int64
}

type searchResponse struct {
	Results []struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		Content       string `json:"content"`
		PublishedDate string `json:"publishedDate"`
	} `json:"results"`
}

func New(cfg Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("searxng base URL is invalid")
	}
	if baseURL.Scheme != "https" && !isLocalEndpoint(baseURL.Hostname()) {
		return nil, errors.New("searxng base URL must use HTTPS unless it points to a local endpoint")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 2*time.Minute {
		return nil, errors.New("searxng timeout is invalid")
	}
	if cfg.MaxResponseBytes < 64*1024 || cfg.MaxResponseBytes > 10*1024*1024 {
		return nil, errors.New("searxng response byte limit is invalid")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.Timeout = cfg.Timeout
	return &Client{baseURL: baseURL, httpClient: httpClient, maxResponseBytes: cfg.MaxResponseBytes}, nil
}

func (c *Client) Search(ctx context.Context, query webresearch.PublicQuery, limit int) ([]webresearch.ProviderSearchResult, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil || strings.TrimSpace(query.String()) == "" || limit < 1 || limit > 20 {
		return nil, errors.New("searxng search request is invalid")
	}
	endpoint, err := url.JoinPath(c.baseURL.String(), "search")
	if err != nil {
		return nil, webresearch.ErrProviderUnavailable
	}
	values := url.Values{}
	values.Set("q", query.String())
	values.Set("format", "json")
	values.Set("pageno", "1")
	values.Set("safesearch", "1")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return nil, webresearch.ErrProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MESGuard/1.0 public-search")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, webresearch.ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if response.StatusCode == http.StatusTooManyRequests {
			return nil, webresearch.ErrProviderRateLimited
		}
		return nil, webresearch.ErrProviderUnavailable
	}
	body, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		return nil, webresearch.ErrInvalidProviderData
	}
	var payload searchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		return nil, webresearch.ErrInvalidProviderData
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, webresearch.ErrInvalidProviderData
	}
	results := make([]webresearch.ProviderSearchResult, 0, min(limit, len(payload.Results)))
	for _, item := range payload.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, webresearch.ProviderSearchResult{
			URL: item.URL, Title: item.Title, Description: item.Content, PublishedTime: item.PublishedDate,
		})
	}
	return results, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, webresearch.ErrProviderUnavailable
	}
	if int64(len(data)) > limit {
		return nil, webresearch.ErrInvalidProviderData
	}
	return data, nil
}

func isLocalEndpoint(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || !strings.Contains(hostname, ".")
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ webresearch.SearchProvider = (*Client)(nil)
