package semanticcache

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const SemanticNormalizationVersion = "semantic-question-v1"

type ConflictKind string

const (
	ConflictEntity    ConflictKind = "entity"
	ConflictNumber    ConflictKind = "number"
	ConflictDate      ConflictKind = "date"
	ConflictVersion   ConflictKind = "version"
	ConflictNegation  ConflictKind = "negation"
	ConflictIntent    ConflictKind = "intent"
	ConflictDirection ConflictKind = "direction"
	ConflictAction    ConflictKind = "action"
)

type QuestionComparison struct {
	Compatible bool
	Conflicts  []ConflictKind
}

var (
	semanticDatePattern          = regexp.MustCompile(`\d{4}[-/.年]\d{1,2}(?:[-/.月]\d{1,2}日?)?`)
	semanticVersionPattern       = regexp.MustCompile(`(?i)\bv?\d+(?:\.\d+){1,3}\b`)
	semanticNumberPattern        = regexp.MustCompile(`\d+(?:\.\d+)?%?|第[零一二两三四五六七八九十百千万亿]+|[零一二两三四五六七八九十百千万亿]+(?:天|小时|分钟|次|条|个|台|页|年|月|日)`)
	semanticEntityPattern        = regexp.MustCompile(`\b(?:[A-Z]{2,}[A-Za-z0-9_-]*|[A-Z][a-z]+[A-Z][A-Za-z0-9_-]*|[A-Za-z]+[0-9][A-Za-z0-9_.-]*)\b`)
	semanticChineseEntityPattern = regexp.MustCompile(`(?:第?[零一二两三四五六七八九十甲乙丙丁东西南北]{1,4}|[A-Za-z0-9_-]{1,12})(?:车间|产线|设备|工厂|公司)`)
	semanticQuotedPattern        = regexp.MustCompile("[`\"“”‘’']([^`\"“”‘’']{1,64})[`\"“”‘’']")
)

// CompareQuestions rejects candidates when deterministic facts show that the
// questions cannot safely share an answer. It intentionally prefers misses to
// false-positive cache hits.
func CompareQuestions(question, candidate string) QuestionComparison {
	left, right := protectedQuestionFacts(question), protectedQuestionFacts(candidate)
	conflicts := make([]ConflictKind, 0, 6)
	if !slices.Equal(left.entities, right.entities) {
		conflicts = append(conflicts, ConflictEntity)
	}
	if !slices.Equal(left.numbers, right.numbers) {
		conflicts = append(conflicts, ConflictNumber)
	}
	if !slices.Equal(left.dates, right.dates) {
		conflicts = append(conflicts, ConflictDate)
	}
	if !slices.Equal(left.versions, right.versions) {
		conflicts = append(conflicts, ConflictVersion)
	}
	if left.negated != right.negated {
		conflicts = append(conflicts, ConflictNegation)
	}
	if left.intent != "" && right.intent != "" && left.intent != right.intent {
		conflicts = append(conflicts, ConflictIntent)
	}
	if left.direction != "" && right.direction != "" && left.direction != right.direction {
		conflicts = append(conflicts, ConflictDirection)
	}
	if left.action != "" && right.action != "" && left.action != right.action {
		conflicts = append(conflicts, ConflictAction)
	}
	return QuestionComparison{Compatible: len(conflicts) == 0, Conflicts: conflicts}
}

type questionFacts struct {
	entities  []string
	numbers   []string
	dates     []string
	versions  []string
	negated   bool
	intent    string
	direction string
	action    string
}

func protectedQuestionFacts(value string) questionFacts {
	value = norm.NFKC.String(strings.TrimSpace(value))
	normalized := strings.ToLower(value)
	return questionFacts{
		entities: normalizedEntities(value),
		numbers:  normalizedMatches(normalized, semanticNumberPattern, false),
		dates:    normalizedMatches(normalized, semanticDatePattern, false),
		versions: normalizedMatches(normalized, semanticVersionPattern, false),
		negated: containsAnyTerm(normalized, []string{"不", "未", "没有", "无法", "不能", "禁止", "否认"}) ||
			containsBoundedAny(normalized, []string{"not", "no", "never", "without", "cannot", "can't", "mustn't"}),
		intent:    questionIntent(normalized),
		direction: fallbackDirection(normalized),
		action:    questionAction(normalized),
	}
}

func fallbackDirection(value string) string {
	failure := containsAnyTerm(value, []string{"失败", "不可用", "超时", "异常"}) ||
		containsBoundedAny(value, []string{"fail", "unavailable", "timeout"})
	transition := containsAnyTerm(value, []string{"降级", "回退", "调用"}) ||
		containsBoundedAny(value, []string{"fallback", "fall back", "call"})
	if !failure || !transition {
		return ""
	}
	type componentPosition struct {
		name  string
		index int
	}
	components := make([]componentPosition, 0, 4)
	for name, aliases := range map[string][]string{
		"fts":    {"fts"},
		"vector": {"向量检索", "vector search"},
		"ocr":    {"ocr"},
		"vlm":    {"vlm"},
	} {
		index := -1
		for _, alias := range aliases {
			current := strings.Index(value, alias)
			if current >= 0 && (index < 0 || current < index) {
				index = current
			}
		}
		if index >= 0 {
			components = append(components, componentPosition{name: name, index: index})
		}
	}
	if len(components) != 2 {
		return ""
	}
	slices.SortFunc(components, func(left, right componentPosition) int { return left.index - right.index })
	return components[0].name + "_to_" + components[1].name
}

func questionAction(value string) string {
	switch {
	case containsAnyTerm(value, []string{"关闭", "禁用", "停用"}) ||
		containsBoundedAny(value, []string{"disable", "close", "deactivate"}):
		return "disable"
	case containsAnyTerm(value, []string{"开启", "启用", "打开"}) ||
		containsBoundedAny(value, []string{"enable", "open", "activate"}):
		return "enable"
	default:
		return ""
	}
}

func normalizedEntities(value string) []string {
	values := normalizedMatches(value, semanticEntityPattern, true)
	values = append(values, normalizedMatches(value, semanticChineseEntityPattern, false)...)
	slices.Sort(values)
	return slices.Compact(values)
}

func normalizedMatches(value string, pattern *regexp.Regexp, includeQuoted bool) []string {
	values := pattern.FindAllString(value, -1)
	if includeQuoted {
		for _, match := range semanticQuotedPattern.FindAllStringSubmatch(value, -1) {
			if len(match) == 2 {
				values = append(values, strings.TrimSpace(match[1]))
			}
		}
	}
	for index := range values {
		values[index] = strings.ToLower(strings.TrimSpace(values[index]))
	}
	slices.Sort(values)
	return slices.Compact(values)
}

func containsAnyTerm(value string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func containsBoundedAny(value string, terms []string) bool {
	for _, term := range terms {
		if containsBoundedEnglishTerm(value, term) {
			return true
		}
	}
	return false
}

func questionIntent(value string) string {
	switch {
	case strings.Contains(value, "为什么") || strings.Contains(value, "为何") || containsBoundedAny(value, []string{"why"}):
		return "reason"
	case strings.Contains(value, "有什么作用") || strings.Contains(value, "作用是什么") ||
		containsBoundedAny(value, []string{"purpose", "effect"}):
		return "effect"
	case strings.Contains(value, "如何") || strings.Contains(value, "怎样") || strings.Contains(value, "怎么") ||
		containsBoundedAny(value, []string{"how"}):
		return "procedure"
	case strings.Contains(value, "对比") || strings.Contains(value, "比较") || strings.Contains(value, "区别") ||
		containsBoundedAny(value, []string{"compare", "difference"}):
		return "comparison"
	case strings.Contains(value, "列出") || strings.Contains(value, "有哪些") || containsBoundedAny(value, []string{"list", "which"}):
		return "list"
	default:
		return ""
	}
}

type SemanticLookupInput struct {
	Question             string
	Vector               []float32
	ProfileID            uuid.UUID
	ProfileFingerprint   string
	NormalizationVersion string
	MinimumSimilarity    float64
	CandidateLimit       int
	Now                  time.Time
}

func (i SemanticLookupInput) Validate(dimensions int, normalized bool) error {
	if strings.TrimSpace(i.Question) == "" || len([]rune(i.Question)) > MaxQuestionRunes ||
		i.ProfileID == uuid.Nil || !validSHA256(i.ProfileFingerprint) ||
		i.NormalizationVersion != SemanticNormalizationVersion ||
		math.IsNaN(i.MinimumSimilarity) || math.IsInf(i.MinimumSimilarity, 0) ||
		i.MinimumSimilarity < 0.5 || i.MinimumSimilarity > 1 ||
		i.CandidateLimit < 1 || i.CandidateLimit > 20 || i.Now.IsZero() {
		return ErrInvalidRecord
	}
	if err := validateVector(i.Vector, dimensions, normalized); err != nil {
		return err
	}
	return nil
}

type SemanticIndexInput struct {
	QuestionHash         string
	Question             string
	Vector               []float32
	ProfileID            uuid.UUID
	ProfileFingerprint   string
	NormalizationVersion string
	SourceRunID          uuid.UUID
}

func (i SemanticIndexInput) Validate(dimensions int, normalized bool) error {
	if !validSHA256(i.QuestionHash) || strings.TrimSpace(i.Question) == "" ||
		len([]rune(i.Question)) > MaxQuestionRunes || i.ProfileID == uuid.Nil ||
		!validSHA256(i.ProfileFingerprint) || i.NormalizationVersion != SemanticNormalizationVersion ||
		i.SourceRunID == uuid.Nil {
		return ErrInvalidRecord
	}
	return validateVector(i.Vector, dimensions, normalized)
}

func validateVector(vector []float32, dimensions int, normalized bool) error {
	if dimensions < 1 || len(vector) != dimensions {
		return ErrInvalidRecord
	}
	var squaredNorm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return ErrInvalidRecord
		}
		squaredNorm += float64(value) * float64(value)
	}
	if squaredNorm == 0 || normalized && math.Abs(math.Sqrt(squaredNorm)-1) > 0.002 {
		return ErrInvalidRecord
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if !unicode.IsDigit(current) && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

// SemanticProvider is an optional extension. A Provider that does not
// implement it remains a fully functional L1 exact cache.
type SemanticProvider interface {
	Provider
	LookupSemantic(context.Context, SemanticLookupInput) (Answer, bool, error)
	IndexSemantic(context.Context, SemanticIndexInput) error
}
