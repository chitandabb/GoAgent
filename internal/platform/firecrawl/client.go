package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

type Client struct {
	baseURL          *url.URL
	apiKey           string
	httpClient       *http.Client
	maxResponseBytes int64
}

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type searchResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Web []struct {
			URL           string `json:"url"`
			Title         string `json:"title"`
			Description   string `json:"description"`
			PublishedTime string `json:"publishedTime"`
			PublishedDate string `json:"publishedDate"`
		} `json:"web"`
	} `json:"data"`
}

type scrapeRequest struct {
	URL             string   `json:"url"`
	Formats         []string `json:"formats"`
	OnlyMainContent bool     `json:"onlyMainContent"`
}

type scrapeResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Markdown string `json:"markdown"`
		Metadata struct {
			Title         string `json:"title"`
			SourceURL     string `json:"sourceURL"`
			URL           string `json:"url"`
			PublishedTime string `json:"publishedTime"`
			ModifiedTime  string `json:"modifiedTime"`
			StatusCode    int    `json:"statusCode"`
		} `json:"metadata"`
	} `json:"data"`
}

func New(cfg Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("firecrawl base URL is invalid")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("firecrawl API key is required")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 2*time.Minute {
		return nil, errors.New("firecrawl timeout is invalid")
	}
	if cfg.MaxResponseBytes < 64*1024 || cfg.MaxResponseBytes > 10*1024*1024 {
		return nil, errors.New("firecrawl response byte limit is invalid")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.Timeout = cfg.Timeout
	return &Client{
		baseURL: baseURL, apiKey: apiKey, httpClient: httpClient,
		maxResponseBytes: cfg.MaxResponseBytes,
	}, nil
}

func (c *Client) Search(ctx context.Context, query webresearch.PublicQuery, limit int) ([]webresearch.ProviderSearchResult, error) {
	if c == nil || strings.TrimSpace(query.String()) == "" || limit < 1 || limit > 20 {
		return nil, errors.New("firecrawl search request is invalid")
	}
	var response searchResponse
	if err := c.postJSON(ctx, "/v2/search", searchRequest{Query: query.String(), Limit: limit}, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, webresearch.ErrProviderUnavailable
	}
	results := make([]webresearch.ProviderSearchResult, 0, len(response.Data.Web))
	for _, item := range response.Data.Web {
		published := item.PublishedTime
		if strings.TrimSpace(published) == "" {
			published = item.PublishedDate
		}
		results = append(results, webresearch.ProviderSearchResult{
			URL: item.URL, Title: item.Title, Description: item.Description, PublishedTime: published,
		})
	}
	return results, nil
}

func (c *Client) Scrape(ctx context.Context, target webresearch.PublicURL) (webresearch.ProviderPage, error) {
	if c == nil || strings.TrimSpace(target.String()) == "" {
		return webresearch.ProviderPage{}, errors.New("firecrawl scrape request is invalid")
	}
	var response scrapeResponse
	request := scrapeRequest{URL: target.String(), Formats: []string{"markdown"}, OnlyMainContent: true}
	if err := c.postJSON(ctx, "/v2/scrape", request, &response); err != nil {
		return webresearch.ProviderPage{}, err
	}
	if !response.Success || strings.TrimSpace(response.Data.Markdown) == "" || response.Data.Metadata.StatusCode >= 400 {
		return webresearch.ProviderPage{}, webresearch.ErrInvalidProviderData
	}
	pageURL := response.Data.Metadata.SourceURL
	if strings.TrimSpace(pageURL) == "" {
		pageURL = response.Data.Metadata.URL
	}
	return webresearch.ProviderPage{
		URL: pageURL, Title: response.Data.Metadata.Title, Markdown: response.Data.Markdown,
		PublishedTime: response.Data.Metadata.PublishedTime, ModifiedTime: response.Data.Metadata.ModifiedTime,
	}, nil
}

// Fetch implements webresearch.ContentProvider. Scrape is retained as a
// compatibility method for callers that used the original Firecrawl name.
func (c *Client) Fetch(ctx context.Context, target webresearch.PublicURL) (webresearch.ProviderPage, error) {
	return c.Scrape(ctx, target)
}

func (c *Client) postJSON(ctx context.Context, path string, input, output any) error {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return webresearch.ErrProviderUnavailable
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode firecrawl request: %w", err)
	}
	endpoint, err := url.JoinPath(c.baseURL.String(), path)
	if err != nil {
		return webresearch.ErrProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return webresearch.ErrProviderUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return webresearch.ErrProviderUnavailable
	}
	defer response.Body.Close()

	body, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		switch response.StatusCode {
		case http.StatusTooManyRequests:
			return webresearch.ErrProviderRateLimited
		case http.StatusUnauthorized, http.StatusForbidden:
			return webresearch.ErrProviderUnauthorized
		default:
			return webresearch.ErrProviderUnavailable
		}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return webresearch.ErrInvalidProviderData
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(output); err != nil {
		return webresearch.ErrInvalidProviderData
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return webresearch.ErrInvalidProviderData
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, webresearch.ErrProviderUnavailable
	}
	if int64(len(data)) > limit {
		return nil, webresearch.ErrInvalidProviderData
	}
	return data, nil
}
