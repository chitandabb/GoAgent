package semanticcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxQuestionRunes = 32_000
	MaxAnswerBytes   = 16 * 1024
	MaxCitations     = 8
	MaxSources       = 64
)

var ErrInvalidRecord = errors.New("semantic answer cache record is invalid")

// ExactQuestionKey intentionally normalizes only surface differences that
// cannot change question meaning. Numbers, dates, versions, negation and
// punctuation remain part of the hashed input.
func ExactQuestionKey(question string) (string, error) {
	question = norm.NFKC.String(strings.TrimSpace(question))
	if question == "" || len([]rune(question)) > MaxQuestionRunes {
		return "", ErrInvalidRecord
	}
	var normalized strings.Builder
	normalized.Grow(len(question))
	spacePending := false
	for _, current := range question {
		if unicode.IsSpace(current) {
			spacePending = normalized.Len() > 0
			continue
		}
		if spacePending {
			normalized.WriteByte(' ')
			spacePending = false
		}
		normalized.WriteRune(unicode.ToLower(current))
	}
	digest := sha256.Sum256([]byte(normalized.String()))
	return hex.EncodeToString(digest[:]), nil
}

type Question struct {
	Text                string
	HasPriorMessages    bool
	HasAttachments      bool
	HasCaseReferences   bool
	HasTaskReferences   bool
	HasReportReferences bool
}

var temporalOrContextChineseTerms = []string{
	"今天", "现在", "目前", "最新", "实时", "最近", "截至", "刚才", "上面", "下面", "继续", "这个", "那个", "它",
}

var temporalOrContextEnglishTerms = []string{
	"today", "now", "current", "latest", "real-time", "realtime", "recent", "as of", "above", "previous", "continue", "this", "that", "it",
}

// EligibleForLookup is deliberately conservative. False negatives only lose a
// cache optimization; false positives can return an answer for the wrong
// conversational or temporal context.
func EligibleForLookup(question Question) bool {
	if question.HasPriorMessages || question.HasAttachments || question.HasCaseReferences ||
		question.HasTaskReferences || question.HasReportReferences {
		return false
	}
	text := strings.ToLower(norm.NFKC.String(strings.TrimSpace(question.Text)))
	if text == "" || len([]rune(text)) > MaxQuestionRunes {
		return false
	}
	for _, term := range temporalOrContextChineseTerms {
		if strings.Contains(text, term) {
			return false
		}
	}
	for _, term := range temporalOrContextEnglishTerms {
		if containsBoundedEnglishTerm(text, term) {
			return false
		}
	}
	return true
}

func containsBoundedEnglishTerm(text, term string) bool {
	textRunes, termRunes := []rune(text), []rune(term)
	for index := 0; index+len(termRunes) <= len(textRunes); index++ {
		if !slices.Equal(textRunes[index:index+len(termRunes)], termRunes) {
			continue
		}
		leftBoundary := index == 0 || !unicode.IsLetter(textRunes[index-1]) && !unicode.IsDigit(textRunes[index-1])
		right := index + len(termRunes)
		rightBoundary := right == len(textRunes) || !unicode.IsLetter(textRunes[right]) && !unicode.IsDigit(textRunes[right])
		if leftBoundary && rightBoundary {
			return true
		}
	}
	return false
}

type Source struct {
	Position      int    `json:"position"`
	SourceType    string `json:"sourceType"`
	SourceRef     string `json:"sourceRef"`
	ContentSHA256 string `json:"contentSha256"`
}

type Answer struct {
	Content          string
	Citations        []Source
	RetrievedSources []Source
	SourceRunID      uuid.UUID
	ModelProvider    string
	ModelID          string
	PromptVersion    string
	Generation       int64
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

type LookupInput struct {
	QuestionHash string
	Now          time.Time
}

type PutInput struct {
	QuestionHash string
	Answer       Answer
	TTL          time.Duration
}

// Provider owns authoritative Generation checks. Callers must never compose a
// cache result with a separately read Generation because that introduces a
// time-of-check/time-of-use window.
type Provider interface {
	Lookup(context.Context, LookupInput) (Answer, bool, error)
	Put(context.Context, PutInput) error
}
