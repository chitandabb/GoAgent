package semanticcache_test

import (
	"testing"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

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
		{name: "prior conversation", question: semanticcache.Question{Text: "连接池规范是什么？", HasPriorMessages: true}},
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
}
