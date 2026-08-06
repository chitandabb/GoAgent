// Package webresearch defines the trust boundary between private MESGuard
// context and public web providers.
package webresearch

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultMaxInputRunes  = 1024
	defaultMaxOutputRunes = 384
	defaultMinOutputRunes = 8
	maxSensitiveTerms     = 128
	maxSensitiveTermRunes = 128
)

var (
	ErrInvalidQueryPolicy       = errors.New("public query policy is invalid")
	ErrInvalidPublicQuery       = errors.New("public query is invalid")
	ErrSensitiveSecretDetected  = errors.New("public query contains secret material")
	ErrStructuredContentBlocked = errors.New("public query contains structured private content")
	ErrInsufficientPublicQuery  = errors.New("public query has insufficient safe technical context")
)

type FindingKind string

const (
	FindingEmail          FindingKind = "email"
	FindingPhone          FindingKind = "phone"
	FindingIdentityNumber FindingKind = "identity_number"
	FindingPrivateAddress FindingKind = "private_address"
	FindingInternalHost   FindingKind = "internal_host"
	FindingFilePath       FindingKind = "file_path"
	FindingURL            FindingKind = "url"
	FindingBusinessID     FindingKind = "business_id"
	FindingContentHash    FindingKind = "content_hash"
	FindingSensitiveTerm  FindingKind = "sensitive_term"
)

// Finding contains only category and count. The matched private value is never
// retained for logs, traces, or provider requests.
type Finding struct {
	Kind  FindingKind `json:"kind"`
	Count int         `json:"count"`
}

// PublicQuery can only be constructed by QueryPolicy. Future provider clients
// should accept this type instead of an arbitrary string.
type PublicQuery struct {
	text     string
	findings []Finding
}

func (q PublicQuery) String() string { return q.text }

func (q PublicQuery) Redacted() bool { return len(q.findings) > 0 }

func (q PublicQuery) Findings() []Finding { return append([]Finding(nil), q.findings...) }

type QueryPolicyConfig struct {
	MaxInputRunes  int
	MaxOutputRunes int
	MinOutputRunes int
	SensitiveTerms []string
}

type QueryPolicy struct {
	maxInputRunes  int
	maxOutputRunes int
	minOutputRunes int
	termPatterns   []*regexp.Regexp
}

type replacementRule struct {
	kind    FindingKind
	pattern *regexp.Regexp
}

var blockedSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|client[_-]?secret|password|passwd|pwd)\s*[:=]\s*["']?[^\s,;"']+`),
	regexp.MustCompile(`(?i)\b(?:github_pat_[a-z0-9_]{20,}|gh[pousr]_[a-z0-9]{20,}|sk-[a-z0-9_-]{16,})\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\b`),
}

var blockedStructuredPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(?:select|with|insert|update|delete|merge|exec|execute|create|alter|drop)\b.{1,512}\b(?:from|into|set|table|procedure|view)\b`),
	regexp.MustCompile(`^\s*(?:\[[^\r\n]{1,1000}\]|\{[^\r\n]{1,1000}\})\s*$`),
	regexp.MustCompile(`^\s*[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9]{2}:[0-9]{2}:[0-9]{2}`),
	regexp.MustCompile(`(?i)\bat\s+[a-z0-9_.$/]+\([^\r\n]*:[0-9]+\)`),
}

var publicQueryReplacementRules = []replacementRule{
	{FindingEmail, regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)},
	{FindingPhone, regexp.MustCompile(`(?:\b1[3-9][0-9]{9}\b|\+[0-9][0-9() -]{8,}[0-9])`)},
	{FindingIdentityNumber, regexp.MustCompile(`\b[0-9]{17}[0-9Xx]\b`)},
	{FindingIdentityNumber, regexp.MustCompile(`\b[0-9]{16,19}\b`)},
	{FindingPrivateAddress, regexp.MustCompile(`\b(?:10(?:\.[0-9]{1,3}){3}|192\.168(?:\.[0-9]{1,3}){2}|172\.(?:1[6-9]|2[0-9]|3[01])(?:\.[0-9]{1,3}){2}|127(?:\.[0-9]{1,3}){3}|169\.254(?:\.[0-9]{1,3}){2})\b`)},
	{FindingPrivateAddress, regexp.MustCompile(`(?i)(?:\b(?:fc|fd)[0-9a-f:]{2,}\b|\bfe80:[0-9a-f:]+\b|::1)`)},
	{FindingInternalHost, regexp.MustCompile(`(?i)\b(?:localhost|[a-z0-9][a-z0-9.-]*\.(?:local|internal|corp|lan))\b`)},
	{FindingInternalHost, regexp.MustCompile(`(?i)\b(?:[a-z0-9]+-)?(?:prod|dev|test|staging|uat)-[a-z0-9-]*[0-9][a-z0-9-]*\b`)},
	{FindingFilePath, regexp.MustCompile(`(?i)(?:\\\\[a-z0-9._$-]+\\[^\s]+|[a-z]:\\[^\s]+)`)},
	{FindingURL, regexp.MustCompile(`(?i)\bhttps?://[^\s]+`)},
	{FindingBusinessID, regexp.MustCompile(`(?i)\b(?:tkt|inc|case|wo|lot|batch|serial|sn)(?:[-_:#][a-z0-9]{3,}|[0-9]{4,})\b`)},
	{FindingBusinessID, regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)},
	{FindingBusinessID, regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){5}[0-9a-f]{2}\b`)},
	{FindingContentHash, regexp.MustCompile(`(?i)\b[0-9a-f]{32,64}\b`)},
}

func NewQueryPolicy(cfg QueryPolicyConfig) (*QueryPolicy, error) {
	if cfg.MaxInputRunes == 0 {
		cfg.MaxInputRunes = defaultMaxInputRunes
	}
	if cfg.MaxOutputRunes == 0 {
		cfg.MaxOutputRunes = defaultMaxOutputRunes
	}
	if cfg.MinOutputRunes == 0 {
		cfg.MinOutputRunes = defaultMinOutputRunes
	}
	if cfg.MaxInputRunes < 64 || cfg.MaxInputRunes > 4096 ||
		cfg.MaxOutputRunes < 32 || cfg.MaxOutputRunes > cfg.MaxInputRunes ||
		cfg.MinOutputRunes < 4 || cfg.MinOutputRunes > cfg.MaxOutputRunes {
		return nil, ErrInvalidQueryPolicy
	}
	patterns, err := compileSensitiveTerms(cfg.SensitiveTerms)
	if err != nil {
		return nil, err
	}
	return &QueryPolicy{
		maxInputRunes: cfg.MaxInputRunes, maxOutputRunes: cfg.MaxOutputRunes,
		minOutputRunes: cfg.MinOutputRunes, termPatterns: patterns,
	}, nil
}

func (p *QueryPolicy) Sanitize(input string, taskSensitiveTerms []string) (PublicQuery, error) {
	if p == nil {
		return PublicQuery{}, ErrInvalidQueryPolicy
	}
	if strings.TrimSpace(input) == "" || !utf8.ValidString(input) || strings.ContainsRune(input, 0) ||
		len([]rune(input)) > p.maxInputRunes {
		return PublicQuery{}, ErrInvalidPublicQuery
	}
	if strings.ContainsAny(input, "\r\n") || strings.Contains(input, "```") || containsUnsafeControl(input) {
		return PublicQuery{}, ErrStructuredContentBlocked
	}
	for _, pattern := range blockedSecretPatterns {
		if pattern.MatchString(input) {
			return PublicQuery{}, ErrSensitiveSecretDetected
		}
	}
	if looksLikeConnectionString(input) {
		return PublicQuery{}, ErrSensitiveSecretDetected
	}
	for _, pattern := range blockedStructuredPatterns {
		if pattern.MatchString(input) {
			return PublicQuery{}, ErrStructuredContentBlocked
		}
	}

	query := input
	counts := make(map[FindingKind]int)
	for _, rule := range publicQueryReplacementRules {
		matches := rule.pattern.FindAllStringIndex(query, -1)
		if len(matches) == 0 {
			continue
		}
		counts[rule.kind] += len(matches)
		query = rule.pattern.ReplaceAllString(query, " ")
	}
	taskPatterns, err := compileSensitiveTerms(taskSensitiveTerms)
	if err != nil {
		return PublicQuery{}, err
	}
	for _, pattern := range append(append([]*regexp.Regexp(nil), p.termPatterns...), taskPatterns...) {
		matches := pattern.FindAllStringIndex(query, -1)
		if len(matches) == 0 {
			continue
		}
		counts[FindingSensitiveTerm] += len(matches)
		query = pattern.ReplaceAllString(query, " ")
	}

	query = strings.Join(strings.Fields(query), " ")
	query = strings.Trim(query, " ,;:|/\\")
	if len([]rune(query)) > p.maxOutputRunes {
		return PublicQuery{}, ErrInvalidPublicQuery
	}
	if countTechnicalRunes(query) < p.minOutputRunes {
		return PublicQuery{}, ErrInsufficientPublicQuery
	}
	return PublicQuery{text: query, findings: sortedFindings(counts)}, nil
}

func looksLikeConnectionString(value string) bool {
	keys := make(map[string]struct{})
	for _, part := range strings.Split(value, ";") {
		key, _, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(key, "_", " ")), " "))
		switch normalized {
		case "server", "data source", "address", "addr", "network address",
			"database", "initial catalog", "user id", "uid", "user",
			"password", "pwd", "integrated security", "trusted connection":
			keys[normalized] = struct{}{}
		}
	}
	if len(keys) < 2 {
		return false
	}
	_, server := keys["server"]
	_, dataSource := keys["data source"]
	_, address := keys["address"]
	_, addr := keys["addr"]
	_, networkAddress := keys["network address"]
	return server || dataSource || address || addr || networkAddress
}

func compileSensitiveTerms(values []string) ([]*regexp.Regexp, error) {
	if len(values) > maxSensitiveTerms {
		return nil, ErrInvalidQueryPolicy
	}
	seen := make(map[string]struct{}, len(values))
	patterns := make([]*regexp.Regexp, 0, len(values))
	for _, value := range values {
		term := strings.TrimSpace(value)
		key := strings.ToLower(term)
		if term == "" {
			continue
		}
		if len([]rune(term)) < 2 || len([]rune(term)) > maxSensitiveTermRunes || strings.ContainsAny(term, "\r\n") {
			return nil, ErrInvalidQueryPolicy
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		quoted := regexp.QuoteMeta(term)
		if isASCIIWordTerm(term) {
			quoted = `\b` + quoted + `\b`
		}
		pattern, err := regexp.Compile(`(?i)` + quoted)
		if err != nil {
			return nil, ErrInvalidQueryPolicy
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

func isASCIIWordTerm(value string) bool {
	runes := []rune(value)
	if len(runes) == 0 || !isASCIIAlphaNumeric(runes[0]) || !isASCIIAlphaNumeric(runes[len(runes)-1]) {
		return false
	}
	for _, current := range runes {
		if current > unicode.MaxASCII || !(isASCIIAlphaNumeric(current) || current == '-' || current == '_' || current == '.' || current == ' ') {
			return false
		}
	}
	return true
}

func isASCIIAlphaNumeric(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func containsUnsafeControl(value string) bool {
	for _, current := range value {
		if unicode.IsControl(current) && current != '\t' {
			return true
		}
	}
	return false
}

func countTechnicalRunes(value string) int {
	count := 0
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			count++
		}
	}
	return count
}

func sortedFindings(counts map[FindingKind]int) []Finding {
	kinds := make([]string, 0, len(counts))
	for kind, count := range counts {
		if count > 0 {
			kinds = append(kinds, string(kind))
		}
	}
	sort.Strings(kinds)
	result := make([]Finding, 0, len(kinds))
	for _, kind := range kinds {
		result = append(result, Finding{Kind: FindingKind(kind), Count: counts[FindingKind(kind)]})
	}
	return result
}
