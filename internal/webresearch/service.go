package webresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	defaultSearchResults = 5
	maxSearchTitleRunes  = 512
	maxSearchTextRunes   = 2048
	maxPageTitleRunes    = 512
	maxPageTimeRunes     = 128
)

var (
	ErrRunStateRequired     = errors.New("web research run state is required")
	ErrRunUserMismatch      = errors.New("web research run user does not match task scope")
	ErrSearchBudgetReached  = errors.New("web research search budget is exhausted")
	ErrFetchBudgetReached   = errors.New("web research page budget is exhausted")
	ErrResultNotAuthorized  = errors.New("web research result is not authorized for this run")
	ErrProviderRateLimited  = errors.New("web research provider is rate limited")
	ErrProviderUnauthorized = errors.New("web research provider rejected its credential")
	ErrProviderUnavailable  = errors.New("web research provider is unavailable")
	ErrInvalidProviderData  = errors.New("web research provider returned invalid data")
)

type SourceTier string

const (
	SourceTierOfficial  SourceTier = "A"
	SourceTierTrusted   SourceTier = "B"
	SourceTierCommunity SourceTier = "C"
)

type ProviderSearchResult struct {
	URL           string
	Title         string
	Description   string
	PublishedTime string
}

type ProviderPage struct {
	URL           string
	Title         string
	Markdown      string
	PublishedTime string
	ModifiedTime  string
}

// Provider receives only policy-created query and URL values. Implementations
// cannot be called with arbitrary private text or an unvalidated target URL.
type Provider interface {
	Search(context.Context, PublicQuery, int) ([]ProviderSearchResult, error)
	Scrape(context.Context, PublicURL) (ProviderPage, error)
}

type SearchResult struct {
	ResultID      string     `json:"resultId"`
	URL           string     `json:"url"`
	Domain        string     `json:"domain"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	PublishedTime string     `json:"publishedTime,omitempty"`
	SourceTier    SourceTier `json:"sourceTier"`
}

type SearchResponse struct {
	Query             string         `json:"query"`
	Redacted          bool           `json:"redacted"`
	RedactionFindings []Finding      `json:"redactionFindings,omitempty"`
	Results           []SearchResult `json:"results"`
	OmittedResults    int            `json:"omittedResults"`
	UntrustedContent  bool           `json:"untrustedContent"`
}

type PageSnapshot struct {
	ResultID         string     `json:"resultId"`
	URL              string     `json:"url"`
	Domain           string     `json:"domain"`
	Title            string     `json:"title"`
	PublishedTime    string     `json:"publishedTime,omitempty"`
	ModifiedTime     string     `json:"modifiedTime,omitempty"`
	FetchedAt        time.Time  `json:"fetchedAt"`
	ContentText      string     `json:"contentText"`
	ContentSHA256    string     `json:"contentSha256"`
	SourceTier       SourceTier `json:"sourceTier"`
	Truncated        bool       `json:"truncated"`
	Redirected       bool       `json:"redirected"`
	UntrustedContent bool       `json:"untrustedContent"`
}

type ServiceConfig struct {
	Provider        Provider
	QueryPolicy     *QueryPolicy
	URLPolicy       *URLPolicy
	MaxResults      int
	MaxFetchedPages int
	MaxPageChars    int
	MaxRounds       int
	OfficialDomains []string
	TrustedDomains  []string
	Clock           func() time.Time
}

type Service struct {
	provider        Provider
	queryPolicy     *QueryPolicy
	urlPolicy       *URLPolicy
	maxResults      int
	maxFetchedPages int
	maxPageChars    int
	maxRounds       int
	officialDomains []string
	trustedDomains  []string
	clock           func() time.Time
}

type authorizedResult struct {
	url           PublicURL
	title         string
	publishedTime string
	tier          SourceTier
}

type RunState struct {
	userID         string
	sensitiveTerms []string
	maxRounds      int
	maxFetched     int

	mu             sync.Mutex
	searchAttempts int
	fetchAttempts  int
	results        map[string]authorizedResult
	resultIDByURL  map[string]string
	pages          map[string]PageSnapshot
}

type runStateContextKey struct{}

func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Provider == nil || cfg.QueryPolicy == nil || cfg.URLPolicy == nil {
		return nil, errors.New("web research provider and policies are required")
	}
	if cfg.MaxResults < 1 || cfg.MaxResults > 20 || cfg.MaxFetchedPages < 1 ||
		cfg.MaxFetchedPages > cfg.MaxResults || cfg.MaxPageChars < 1000 || cfg.MaxPageChars > 100000 ||
		cfg.MaxRounds < 1 || cfg.MaxRounds > 4 {
		return nil, errors.New("web research service budget is invalid")
	}
	official, err := normalizeDomainRules(cfg.OfficialDomains)
	if err != nil {
		return nil, fmt.Errorf("official domain rules: %w", err)
	}
	trusted, err := normalizeDomainRules(cfg.TrustedDomains)
	if err != nil {
		return nil, fmt.Errorf("trusted domain rules: %w", err)
	}
	for _, domain := range official {
		if domainMatchesAny(domain, trusted) || domainMatchesAnyRule(domain, trusted) {
			return nil, fmt.Errorf("domain %q is present in both source tiers", domain)
		}
	}
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		provider: cfg.Provider, queryPolicy: cfg.QueryPolicy, urlPolicy: cfg.URLPolicy,
		maxResults: cfg.MaxResults, maxFetchedPages: cfg.MaxFetchedPages,
		maxPageChars: cfg.MaxPageChars, maxRounds: cfg.MaxRounds,
		officialDomains: official, trustedDomains: trusted, clock: clock,
	}, nil
}

func (s *Service) WithRunContext(ctx context.Context, userID string, sensitiveTerms []string) (context.Context, error) {
	if s == nil {
		return nil, errors.New("web research service is nil")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" || len(sensitiveTerms) > maxSensitiveTerms {
		return nil, errors.New("web research run identity or sensitive terms are invalid")
	}
	if _, err := compileSensitiveTerms(sensitiveTerms); err != nil {
		return nil, err
	}
	state := &RunState{
		userID: userID, sensitiveTerms: append([]string(nil), sensitiveTerms...),
		maxRounds: s.maxRounds, maxFetched: s.maxFetchedPages,
		results: make(map[string]authorizedResult), resultIDByURL: make(map[string]string),
		pages: make(map[string]PageSnapshot),
	}
	return context.WithValue(ctx, runStateContextKey{}, state), nil
}

func (s *Service) Search(ctx context.Context, userID, input string, limit int) (SearchResponse, error) {
	state, err := runStateForUser(ctx, userID)
	if err != nil {
		return SearchResponse{}, err
	}
	if !state.reserveSearch() {
		return SearchResponse{}, ErrSearchBudgetReached
	}
	query, err := s.queryPolicy.Sanitize(input, state.sensitiveTerms)
	if err != nil {
		return SearchResponse{}, err
	}
	if limit <= 0 {
		limit = defaultSearchResults
	}
	if limit > s.maxResults {
		limit = s.maxResults
	}
	providerResults, err := s.provider.Search(ctx, query, limit)
	if err != nil {
		return SearchResponse{}, err
	}
	response := SearchResponse{
		Query: query.String(), Redacted: query.Redacted(), RedactionFindings: query.Findings(),
		Results: make([]SearchResult, 0, min(limit, len(providerResults))), UntrustedContent: true,
	}
	seenURLs := make(map[string]struct{}, len(providerResults))
	for _, candidate := range providerResults {
		if len(response.Results) >= limit {
			response.OmittedResults++
			continue
		}
		publicURL, validateErr := s.urlPolicy.Validate(ctx, candidate.URL)
		if validateErr != nil {
			response.OmittedResults++
			continue
		}
		if _, duplicate := seenURLs[publicURL.String()]; duplicate {
			response.OmittedResults++
			continue
		}
		seenURLs[publicURL.String()] = struct{}{}
		title := boundedProviderText(candidate.Title, maxSearchTitleRunes)
		if title == "" {
			title = publicURL.Domain()
		}
		description := boundedProviderText(candidate.Description, maxSearchTextRunes)
		published := boundedProviderText(candidate.PublishedTime, maxPageTimeRunes)
		tier := s.sourceTier(publicURL.Domain())
		resultID := state.authorize(publicURL, title, published, tier)
		response.Results = append(response.Results, SearchResult{
			ResultID: resultID, URL: publicURL.String(), Domain: publicURL.Domain(), Title: title,
			Description: description, PublishedTime: published, SourceTier: tier,
		})
	}
	return response, nil
}

func (s *Service) Fetch(ctx context.Context, userID, resultID string) (PageSnapshot, error) {
	state, err := runStateForUser(ctx, userID)
	if err != nil {
		return PageSnapshot{}, err
	}
	resultID = strings.TrimSpace(resultID)
	authorized, cached, ok, reserved := state.authorizeFetch(resultID)
	if !ok {
		return PageSnapshot{}, ErrResultNotAuthorized
	}
	if cached != nil {
		return *cached, nil
	}
	if !reserved {
		return PageSnapshot{}, ErrFetchBudgetReached
	}
	page, err := s.provider.Scrape(ctx, authorized.url)
	if err != nil {
		return PageSnapshot{}, err
	}
	finalRawURL := strings.TrimSpace(page.URL)
	if finalRawURL == "" {
		finalRawURL = authorized.url.String()
	}
	finalURL, err := s.urlPolicy.Validate(ctx, finalRawURL)
	if err != nil {
		return PageSnapshot{}, fmt.Errorf("%w: unsafe final page URL", ErrInvalidProviderData)
	}
	content, truncated, err := boundedPageContent(page.Markdown, s.maxPageChars)
	if err != nil {
		return PageSnapshot{}, err
	}
	title := boundedProviderText(page.Title, maxPageTitleRunes)
	if title == "" {
		title = authorized.title
	}
	published := boundedProviderText(page.PublishedTime, maxPageTimeRunes)
	if published == "" {
		published = authorized.publishedTime
	}
	modified := boundedProviderText(page.ModifiedTime, maxPageTimeRunes)
	digest := sha256.Sum256([]byte(content))
	snapshot := PageSnapshot{
		ResultID: resultID, URL: finalURL.String(), Domain: finalURL.Domain(), Title: title,
		PublishedTime: published, ModifiedTime: modified, FetchedAt: s.clock().UTC(),
		ContentText: content, ContentSHA256: hex.EncodeToString(digest[:]),
		SourceTier: s.sourceTier(finalURL.Domain()), Truncated: truncated,
		Redirected: finalURL.String() != authorized.url.String(), UntrustedContent: true,
	}
	state.storePage(resultID, snapshot)
	return snapshot, nil
}

func runStateForUser(ctx context.Context, userID string) (*RunState, error) {
	state, ok := ctx.Value(runStateContextKey{}).(*RunState)
	if !ok || state == nil {
		return nil, ErrRunStateRequired
	}
	if state.userID != strings.TrimSpace(userID) {
		return nil, ErrRunUserMismatch
	}
	return state, nil
}

func (s *Service) sourceTier(domain string) SourceTier {
	if domainMatchesAny(domain, s.officialDomains) {
		return SourceTierOfficial
	}
	if domainMatchesAny(domain, s.trustedDomains) {
		return SourceTierTrusted
	}
	return SourceTierCommunity
}

func (s *RunState) reserveSearch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchAttempts >= s.maxRounds {
		return false
	}
	s.searchAttempts++
	return true
}

func (s *RunState) authorize(target PublicURL, title, published string, tier SourceTier) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.resultIDByURL[target.String()]; existing != "" {
		return existing
	}
	resultID := "web_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.results[resultID] = authorizedResult{url: target, title: title, publishedTime: published, tier: tier}
	s.resultIDByURL[target.String()] = resultID
	return resultID
}

func (s *RunState) authorizeFetch(resultID string) (authorizedResult, *PageSnapshot, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authorized, ok := s.results[resultID]
	if !ok {
		return authorizedResult{}, nil, false, false
	}
	if page, exists := s.pages[resultID]; exists {
		copy := page
		return authorized, &copy, true, false
	}
	if s.fetchAttempts >= s.maxFetched {
		return authorized, nil, true, false
	}
	s.fetchAttempts++
	return authorized, nil, true, true
}

func (s *RunState) storePage(resultID string, page PageSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages[resultID] = page
}

func boundedProviderText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	value = strings.Map(func(current rune) rune {
		if unicode.IsControl(current) && current != '\t' && current != '\n' {
			return ' '
		}
		return current
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = strings.TrimSpace(string(runes[:limit]))
	}
	return value
}

func boundedPageContent(value string, limit int) (string, bool, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", false, ErrInvalidProviderData
	}
	value = strings.Map(func(current rune) rune {
		if unicode.IsControl(current) && current != '\t' && current != '\n' && current != '\r' {
			return -1
		}
		return current
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, ErrInvalidProviderData
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false, nil
	}
	return strings.TrimSpace(string(runes[:limit])), true, nil
}
