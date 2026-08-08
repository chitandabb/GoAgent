package directweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/chitandabb/GoAgent/internal/webresearch"
)

var errRedirectRejected = errors.New("direct web redirect rejected")

var punctuationSpacing = strings.NewReplacer(
	" .", ".", " ,", ",", " ;", ";", " :", ":", " ?", "?", " !", "!",
	" )", ")", " ]", "]", " }", "}",
)

type Config struct {
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
	ValidateRedirect func(context.Context, string) error
}

type Client struct {
	httpClient       *http.Client
	maxResponseBytes int64
}

func New(cfg Config) (*Client, error) {
	if cfg.Timeout < time.Second || cfg.Timeout > 2*time.Minute {
		return nil, errors.New("direct web timeout is invalid")
	}
	if cfg.MaxResponseBytes < 64*1024 || cfg.MaxResponseBytes > 10*1024*1024 {
		return nil, errors.New("direct web response byte limit is invalid")
	}
	if cfg.ValidateRedirect == nil {
		return nil, errors.New("direct web redirect validator is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	validateRedirect := cfg.ValidateRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errRedirectRejected
		}
		if err := validateRedirect(req.Context(), req.URL.String()); err != nil {
			return fmt.Errorf("%w: %v", errRedirectRejected, err)
		}
		return nil
	}
	httpClient.Timeout = cfg.Timeout
	return &Client{httpClient: httpClient, maxResponseBytes: cfg.MaxResponseBytes}, nil
}

func (c *Client) Fetch(ctx context.Context, target webresearch.PublicURL) (webresearch.ProviderPage, error) {
	if c == nil || c.httpClient == nil || strings.TrimSpace(target.String()) == "" {
		return webresearch.ProviderPage{}, errors.New("direct web fetch request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return webresearch.ProviderPage{}, webresearch.ErrProviderUnavailable
	}
	request.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain, application/json;q=0.9")
	request.Header.Set("User-Agent", "MESGuard/1.0 public-content-fetch")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return webresearch.ProviderPage{}, err
		}
		if errors.Is(err, errRedirectRejected) {
			return webresearch.ProviderPage{}, webresearch.ErrInvalidProviderData
		}
		return webresearch.ProviderPage{}, webresearch.ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if response.StatusCode == http.StatusTooManyRequests {
			return webresearch.ProviderPage{}, webresearch.ErrProviderRateLimited
		}
		return webresearch.ProviderPage{}, webresearch.ErrProviderUnavailable
	}
	body, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		return webresearch.ProviderPage{}, err
	}
	contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil {
		mediaType = ""
	}
	if mediaType == "" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	if !isSupportedMediaType(mediaType) || !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
		return webresearch.ProviderPage{}, webresearch.ErrInvalidProviderData
	}

	page := webresearch.ProviderPage{}
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		page.Title, page.Markdown = extractHTML(body)
	} else {
		page.Markdown = strings.TrimSpace(string(body))
	}
	if strings.TrimSpace(page.Markdown) == "" {
		return webresearch.ProviderPage{}, webresearch.ErrInvalidProviderData
	}
	page.URL = target.String()
	if response.Request != nil && response.Request.URL != nil {
		page.URL = response.Request.URL.String()
	}
	return page, nil
}

func isSupportedMediaType(mediaType string) bool {
	switch mediaType {
	case "text/html", "application/xhtml+xml", "text/plain", "application/json":
		return true
	default:
		return false
	}
}

func extractHTML(body []byte) (string, string) {
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", ""
	}
	var title string
	var parts []string
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, skip bool) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode {
			tag := strings.ToLower(node.Data)
			if tag == "title" && title == "" {
				title = strings.TrimSpace(nodeText(node))
			}
			if tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" || tag == "template" || tag == "nav" || tag == "footer" || tag == "form" || tag == "head" {
				skip = true
			}
			if !skip && isBlockTag(tag) && len(parts) > 0 && parts[len(parts)-1] != "\n" {
				parts = append(parts, "\n")
			}
		}
		if !skip && node.Type == html.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" {
				parts = append(parts, text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skip)
		}
	}
	walk(document, false)
	content := normalizeText(strings.Join(parts, " "))
	return normalizeText(title), content
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}
	var parts []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		parts = append(parts, nodeText(child))
	}
	return strings.Join(parts, " ")
}

func isBlockTag(tag string) bool {
	switch tag {
	case "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt", "h1", "h2", "h3", "h4", "h5", "h6", "li", "main", "p", "pre", "section", "table", "tr", "td", "th", "ul", "ol":
		return true
	default:
		return false
	}
}

func normalizeText(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		line = punctuationSpacing.Replace(line)
		if line != "" && (len(result) == 0 || result[len(result)-1] != line) {
			result = append(result, line)
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
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
