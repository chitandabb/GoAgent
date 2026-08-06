package knowledge

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxQueryRewriteRunes = 512
	MaxQuerySubqueries   = 2
)

type QueryRewriteUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

func (u QueryRewriteUsage) Validate() error {
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.PromptTokens > u.TotalTokens || u.CompletionTokens > u.TotalTokens ||
		u.PromptTokens > u.TotalTokens-u.CompletionTokens {
		return errors.New("knowledge query rewrite usage is invalid")
	}
	return nil
}

type QueryRewriteStatus string

const (
	QueryRewriteDisabled       QueryRewriteStatus = "disabled"
	QueryRewriteAccepted       QueryRewriteStatus = "accepted"
	QueryRewriteProviderFailed QueryRewriteStatus = "provider_failed"
	QueryRewritePolicyRejected QueryRewriteStatus = "policy_rejected"
)

type QueryRewriteResult struct {
	LexicalQuery  string
	SemanticQuery string
	Subqueries    []string
	PromptVersion string
	Usage         QueryRewriteUsage
}

type QueryPlan struct {
	OriginalQuery    string
	LexicalQuery     string
	SemanticQuery    string
	Subqueries       []string
	RewriteAttempted bool
	RewriteApplied   bool
	PromptVersion    string
	Usage            QueryRewriteUsage
}

type QueryRewriter interface {
	Rewrite(context.Context, string) (QueryRewriteResult, error)
}

func (p QueryPlan) Validate() error {
	if err := validateOriginalQuery(p.OriginalQuery); err != nil {
		return err
	}
	if !p.RewriteAttempted {
		if p.LexicalQuery != p.OriginalQuery || p.SemanticQuery != p.OriginalQuery || len(p.Subqueries) != 0 ||
			p.RewriteApplied || p.PromptVersion != "" || p.Usage != (QueryRewriteUsage{}) {
			return errors.New("knowledge original query plan is invalid")
		}
		return nil
	}
	rebuilt, err := BuildQueryPlan(p.OriginalQuery, QueryRewriteResult{
		LexicalQuery: p.LexicalQuery, SemanticQuery: p.SemanticQuery,
		Subqueries: p.Subqueries, PromptVersion: p.PromptVersion, Usage: p.Usage,
	}, MaxQuerySubqueries)
	if err != nil || rebuilt.RewriteApplied != p.RewriteApplied || len(rebuilt.Subqueries) != len(p.Subqueries) {
		return errors.New("knowledge rewritten query plan is invalid")
	}
	for index := range rebuilt.Subqueries {
		if rebuilt.Subqueries[index] != p.Subqueries[index] {
			return errors.New("knowledge rewritten query plan is invalid")
		}
	}
	return nil
}

func OriginalQueryPlan(query string) (QueryPlan, error) {
	if err := validateOriginalQuery(query); err != nil {
		return QueryPlan{}, err
	}
	return QueryPlan{OriginalQuery: query, LexicalQuery: query, SemanticQuery: query}, nil
}

func BuildQueryPlan(original string, rewrite QueryRewriteResult, maxSubqueries int) (QueryPlan, error) {
	if err := validateOriginalQuery(original); err != nil {
		return QueryPlan{}, err
	}
	if maxSubqueries < 0 || maxSubqueries > MaxQuerySubqueries || len(rewrite.Subqueries) > maxSubqueries {
		return QueryPlan{}, errors.New("knowledge query rewrite subquery limit is invalid")
	}
	if strings.TrimSpace(rewrite.PromptVersion) == "" || rewrite.PromptVersion != strings.TrimSpace(rewrite.PromptVersion) ||
		len(rewrite.PromptVersion) > 128 {
		return QueryPlan{}, errors.New("knowledge query rewrite metadata is invalid")
	}
	if err := rewrite.Usage.Validate(); err != nil {
		return QueryPlan{}, errors.New("knowledge query rewrite metadata is invalid")
	}
	queries := append([]string{rewrite.LexicalQuery, rewrite.SemanticQuery}, rewrite.Subqueries...)
	for index, query := range queries {
		if err := validateRewriteQuery(query); err != nil {
			return QueryPlan{}, err
		}
		requireAllSignals := index < 2
		if !compatibleProtectedSignals(original, query, requireAllSignals) {
			return QueryPlan{}, errors.New("knowledge query rewrite changed protected signals")
		}
	}
	subqueries := deduplicateQueriesExcluding(
		rewrite.Subqueries, original, rewrite.LexicalQuery, rewrite.SemanticQuery,
	)
	plan := QueryPlan{
		OriginalQuery: original, LexicalQuery: rewrite.LexicalQuery, SemanticQuery: rewrite.SemanticQuery,
		Subqueries: subqueries, RewriteAttempted: true, PromptVersion: rewrite.PromptVersion, Usage: rewrite.Usage,
	}
	plan.RewriteApplied = !strings.EqualFold(plan.LexicalQuery, original) ||
		!strings.EqualFold(plan.SemanticQuery, original) || len(plan.Subqueries) > 0
	return plan, nil
}

func (p QueryPlan) FTSQueries() []string {
	candidates := append([]string{p.OriginalQuery, p.LexicalQuery}, p.Subqueries...)
	values := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if len([]rune(candidate)) <= MaxQueryRewriteRunes {
			values = append(values, candidate)
		}
	}
	return deduplicateQueries(values)
}

func (p QueryPlan) VectorQueries() []string {
	values := []string{p.OriginalQuery, p.SemanticQuery}
	values = append(values, p.Subqueries...)
	return deduplicateQueries(values)
}

func validateOriginalQuery(query string) error {
	if strings.TrimSpace(query) == "" || query != strings.TrimSpace(query) || strings.ContainsRune(query, 0) ||
		len([]rune(query)) > MaxKnowledgeSearchQueryRunes || containsQueryControl(query) {
		return errors.New("knowledge query is invalid")
	}
	return nil
}

func validateRewriteQuery(query string) error {
	if strings.TrimSpace(query) == "" || query != strings.TrimSpace(query) || strings.ContainsAny(query, "\r\n") ||
		strings.ContainsRune(query, 0) || len([]rune(query)) > MaxQueryRewriteRunes || containsQueryControl(query) {
		return errors.New("knowledge query rewrite is invalid")
	}
	return nil
}

func containsQueryControl(value string) bool {
	for _, current := range value {
		if unicode.IsControl(current) && current != '\t' {
			return true
		}
	}
	return false
}

var protectedASCIIToken = regexp.MustCompile(`(?i)[a-z0-9](?:[a-z0-9._:-]{0,62}[a-z0-9])?`)

var protectedTerms = []string{
	"不能", "无法", "没有", "尚未", "未能", "未完成", "未配置", "未发现", "未收到", "未返回",
	"未启动", "未更新", "未生效", "未执行", "未连接", "未响应", "未成功", "不是", "并非", "禁止", "不得",
	"之前", "之后", "此前", "以后", "当前", "最近", "今天", "昨天", "明天", "本周", "上周", "下周",
	"截至", "首次", "偶发", "始终",
}

var protectedEnglishTerms = regexp.MustCompile(`(?i)\b(?:no|not|never|without|before|after|current|latest|recent|first|intermittent|always)\b`)

func protectedSignals(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, token := range protectedASCIIToken.FindAllString(value, -1) {
		if strings.IndexFunc(token, unicode.IsDigit) >= 0 {
			result[strings.ToLower(token)] = struct{}{}
		}
	}
	for _, term := range protectedTerms {
		if strings.Contains(value, term) {
			result[term] = struct{}{}
		}
	}
	for _, term := range protectedEnglishTerms.FindAllString(value, -1) {
		result[strings.ToLower(term)] = struct{}{}
	}
	return result
}

func ProtectedQuerySignals(value string) []string {
	signals := protectedSignals(value)
	result := make([]string, 0, len(signals))
	for signal := range signals {
		result = append(result, signal)
	}
	sort.Strings(result)
	return result
}

func compatibleProtectedSignals(original, candidate string, requireAll bool) bool {
	originalSignals := protectedSignals(original)
	candidateSignals := protectedSignals(candidate)
	for signal := range candidateSignals {
		if _, exists := originalSignals[signal]; !exists {
			return false
		}
	}
	if !requireAll {
		return true
	}
	if len(originalSignals) != len(candidateSignals) {
		return false
	}
	for signal := range originalSignals {
		if _, exists := candidateSignals[signal]; !exists {
			return false
		}
	}
	return true
}

func deduplicateQueries(groups ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, values := range groups {
		for _, value := range values {
			key := strings.ToLower(strings.TrimSpace(value))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func deduplicateQueriesExcluding(values []string, excluded ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(excluded))
	for _, value := range excluded {
		if key := strings.ToLower(strings.TrimSpace(value)); key != "" {
			seen[key] = struct{}{}
		}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
