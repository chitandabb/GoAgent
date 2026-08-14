package semanticcache_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
	"github.com/google/uuid"
)

func TestSemanticQuestionsCompatibleProtectsMeaningChangingFacts(t *testing.T) {
	t.Parallel()

	compatible := []struct {
		name      string
		original  string
		candidate string
	}{
		{
			name:      "semantic paraphrase",
			original:  "MESGuard 的知识文档发布流程是什么？",
			candidate: "如何发布 MESGuard 知识库文档？",
		},
		{
			name:      "same numeric constraint",
			original:  "设备点检周期 30 天如何配置？",
			candidate: "怎样把设备点检周期设置为 30 天？",
		},
		{
			name:      "ordinary english words are not entities",
			original:  "How to publish a knowledge document?",
			candidate: "What is the document publishing process?",
		},
	}
	for _, test := range compatible {
		t.Run(test.name, func(t *testing.T) {
			if result := semanticcache.CompareQuestions(test.original, test.candidate); !result.Compatible {
				t.Fatalf("CompareQuestions() rejected reusable pair: %+v", result)
			}
		})
	}

	conflicts := []struct {
		name      string
		original  string
		candidate string
		want      semanticcache.ConflictKind
	}{
		{name: "number", original: "点检周期是 30 天吗？", candidate: "点检周期是 60 天吗？", want: semanticcache.ConflictNumber},
		{name: "date", original: "2026-08-01 的制度是什么？", candidate: "2026-09-01 的制度是什么？", want: semanticcache.ConflictDate},
		{name: "version", original: "MESGuard v2.1 如何升级？", candidate: "MESGuard v2.2 如何升级？", want: semanticcache.ConflictVersion},
		{name: "negation", original: "管理员可以删除制度吗？", candidate: "管理员不可以删除制度吗？", want: semanticcache.ConflictNegation},
		{name: "entity", original: "MESGuard 的发布流程是什么？", candidate: "GoChat 的发布流程是什么？", want: semanticcache.ConflictEntity},
		{name: "intent", original: "为什么需要发布知识文档？", candidate: "如何发布知识文档？", want: semanticcache.ConflictIntent},
		{name: "chinese entity", original: "甲车间点检流程是什么？", candidate: "乙车间点检流程是什么？", want: semanticcache.ConflictEntity},
		{name: "chinese number", original: "点检周期是三十天吗？", candidate: "点检周期是六十天吗？", want: semanticcache.ConflictNumber},
	}
	for _, test := range conflicts {
		t.Run(test.name, func(t *testing.T) {
			result := semanticcache.CompareQuestions(test.original, test.candidate)
			if result.Compatible || !slices.Contains(result.Conflicts, test.want) {
				t.Fatalf("CompareQuestions() = %+v, want conflict %q", result, test.want)
			}
		})
	}
}

func TestSemanticLookupInputRequiresBoundProfileAndNormalization(t *testing.T) {
	t.Parallel()

	profileID := uuid.New()
	valid := semanticcache.SemanticLookupInput{
		Question: "MESGuard 的知识文档如何发布？", Vector: []float32{0.6, 0.8},
		ProfileID: profileID, ProfileFingerprint: strings.Repeat("a", 64),
		NormalizationVersion: semanticcache.SemanticNormalizationVersion,
		MinimumSimilarity:    0.92, CandidateLimit: 5, Now: time.Now().UTC(),
	}
	if err := valid.Validate(2, true); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, mutate := range map[string]func(*semanticcache.SemanticLookupInput){
		"profile":       func(input *semanticcache.SemanticLookupInput) { input.ProfileID = uuid.Nil },
		"fingerprint":   func(input *semanticcache.SemanticLookupInput) { input.ProfileFingerprint = "wrong" },
		"normalization": func(input *semanticcache.SemanticLookupInput) { input.NormalizationVersion = "semantic-question-v0" },
		"threshold":     func(input *semanticcache.SemanticLookupInput) { input.MinimumSimilarity = 0.4 },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := input.Validate(2, true); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestExactQuestionKeyNormalizesOnlySafeSurfaceDifferences(t *testing.T) {
	left, err := semanticcache.ExactQuestionKey("  MESGuard\t如何配置？  ")
	if err != nil {
		t.Fatalf("create left key: %v", err)
	}
	right, err := semanticcache.ExactQuestionKey("mesguard 如何配置?")
	if err != nil {
		t.Fatalf("create right key: %v", err)
	}
	if left != right {
		t.Fatalf("safe normalization should produce the same key: %q != %q", left, right)
	}
}

func TestExactQuestionKeyPreservesSemanticDifferences(t *testing.T) {
	questions := []string{
		"制度版本 1.2 在 2026-08-13 是否生效？",
		"制度版本 1.3 在 2026-08-13 是否生效？",
		"制度版本 1.2 在 2026-08-14 是否生效？",
		"制度版本 1.2 在 2026-08-13 是否不生效？",
	}
	seen := make(map[string]string, len(questions))
	for _, question := range questions {
		key, err := semanticcache.ExactQuestionKey(question)
		if err != nil {
			t.Fatalf("create key for %q: %v", question, err)
		}
		if previous, exists := seen[key]; exists {
			t.Fatalf("semantic difference collapsed: %q and %q", previous, question)
		}
		seen[key] = question
	}
}

func TestQuestionEligibilityRejectsContextDependentAndTemporalQuestions(t *testing.T) {
	tests := []struct {
		name     string
		question semanticcache.Question
	}{
		{name: "attachment", question: semanticcache.Question{Text: "连接池规范是什么？", HasAttachments: true}},
		{name: "case reference", question: semanticcache.Question{Text: "连接池规范是什么？", HasCaseReferences: true}},
		{name: "task reference", question: semanticcache.Question{Text: "连接池规范是什么？", HasTaskReferences: true}},
		{name: "report reference", question: semanticcache.Question{Text: "连接池规范是什么？", HasReportReferences: true}},
		{name: "temporal", question: semanticcache.Question{Text: "今天最新的值班制度是什么？"}},
		{name: "conversation deixis", question: semanticcache.Question{Text: "继续解释上面这个规则。"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if semanticcache.EligibleForLookup(test.question) {
				t.Fatal("question should not be cache eligible")
			}
		})
	}
	if !semanticcache.EligibleForLookup(semanticcache.Question{Text: "设备点检周期规范是什么？"}) {
		t.Fatal("independent knowledge question should be cache eligible")
	}
	if !semanticcache.EligibleForLookup(semanticcache.Question{Text: "What is the security policy?"}) {
		t.Fatal("English substrings such as it in security must not be treated as context deixis")
	}
	if !semanticcache.EligibleForLookup(semanticcache.Question{
		Text: "连接池规范是什么？", HasPriorMessages: true,
	}) {
		t.Fatal("independent question in a multi-turn conversation should remain cache eligible")
	}
	if semanticcache.EligibleForLookup(semanticcache.Question{
		Text: "这个规范是什么？", HasPriorMessages: true,
	}) {
		t.Fatal("context-dependent question should not be cache eligible")
	}
}
