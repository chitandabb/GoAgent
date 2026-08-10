package agent

import (
	"strings"
	"testing"
)

const (
	qualityKnowledgeRef  = "knowledge:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222"
	qualityAttachmentRef = "attachment:33333333-3333-4333-8333-333333333333"
	qualityWebRef        = "https://go.dev/doc/go1.25"
)

func TestEvaluateConversationQualityAggregatesSourcesJudgesLatencyAndCost(t *testing.T) {
	cases, observations := conversationQualityFixture()
	summary, err := EvaluateConversationQuality(cases, observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DatasetVersion != "conversation-quality-v1" ||
		summary.ObservationKind != ConversationQualitySeededContract ||
		summary.Cases != 3 || summary.Runs != 3 || summary.PassedRuns != 3 || summary.PassRate != 1 ||
		summary.OutcomeAccuracy != 1 || summary.ContextPrecision != 1 || summary.ContextRecall != 1 ||
		summary.CitationPrecision != 1 || summary.CitationRecall != 1 ||
		summary.PreviewConsistencyRate != 1 || summary.RequiredAnswerTermRecall != 1 ||
		summary.ExpectedDegradedChannelRecall != 1 || summary.ForbiddenAnswerTermHitRate != 0 {
		t.Fatalf("summary quality metrics = %+v", summary)
	}
	if summary.P50DurationMillis != 200 || summary.P95DurationMillis != 300 ||
		summary.TotalTokens != 350 || summary.AverageTokensPerRun != 350.0/3.0 ||
		summary.TotalEstimatedCostCNY != 0.0035 || summary.EstimatedCostPerThousandCNY != 0.0035/3*1000 {
		t.Fatalf("summary runtime metrics = %+v", summary)
	}
	if summary.JudgedRuns != 1 || summary.AverageFaithfulness != 1 ||
		summary.AverageAnswerRelevance != 0.9 || summary.AverageCitationAlignment != 1 {
		t.Fatalf("summary judge metrics = %+v", summary)
	}
	knowledgeSummary := summary.BySourceType[EvidenceSourceKnowledgeChunk]
	attachmentSummary := summary.BySourceType[EvidenceSourceAttachment]
	webSummary := summary.BySourceType[EvidenceSourceWebPage]
	if knowledgeSummary.PreviewChecks != 1 || knowledgeSummary.PreviewMatches != 1 ||
		attachmentSummary.PreviewChecks != 1 || attachmentSummary.PreviewMatches != 1 ||
		webSummary.PreviewChecks != 0 || webSummary.CitationRecall != 1 {
		t.Fatalf("source summaries knowledge=%+v attachment=%+v web=%+v", knowledgeSummary, attachmentSummary, webSummary)
	}
}

func TestEvaluateConversationQualityRejectsMismatchedCitationHashAndPreview(t *testing.T) {
	cases, observations := conversationQualityFixture()
	observations[0].Citations[0].ContentSHA256 = strings.Repeat("d", 64)
	observations[0].Citations[0].PreviewContentSHA256 = strings.Repeat("d", 64)
	summary, err := EvaluateConversationQuality(cases, observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PassedRuns != 2 || summary.CitationPrecision != 2.0/3.0 ||
		summary.CitationRecall != 2.0/3.0 || summary.PreviewConsistencyRate != 0.5 {
		t.Fatalf("summary = %+v", summary)
	}
	knowledgeSummary := summary.BySourceType[EvidenceSourceKnowledgeChunk]
	if knowledgeSummary.CorrectCitations != 0 || knowledgeSummary.CorrectRequired != 0 ||
		knowledgeSummary.PreviewChecks != 1 || knowledgeSummary.PreviewMatches != 0 {
		t.Fatalf("knowledge summary = %+v", knowledgeSummary)
	}
}

func TestConversationQualityContractsRejectInvalidOrMixedInputs(t *testing.T) {
	cases, observations := conversationQualityFixture()

	t.Run("required citation outside relevant sources", func(t *testing.T) {
		invalid := cases[0]
		invalid.RequiredCitationRefs = []string{qualityAttachmentRef}
		if err := invalid.Validate(); err == nil {
			t.Fatal("Validate() accepted an unknown required citation")
		}
	})

	t.Run("retrieval max results outside tool boundary", func(t *testing.T) {
		invalid := cases[0]
		invalid.RetrievalMaxResults = 21
		if err := invalid.Validate(); err == nil {
			t.Fatal("Validate() accepted an unbounded retrieval result count")
		}
	})

	t.Run("malformed source identity", func(t *testing.T) {
		invalid := cases[0].RelevantSources[0]
		invalid.SourceRef = "knowledge:not-a-version/not-a-chunk"
		if err := invalid.Validate(); err == nil {
			t.Fatal("Validate() accepted a malformed source ref")
		}
	})

	t.Run("uppercase content hash", func(t *testing.T) {
		invalid := cases[0].RelevantSources[0]
		invalid.ContentSHA256 = strings.Repeat("A", 64)
		if err := invalid.Validate(); err == nil {
			t.Fatal("Validate() accepted a non-canonical content hash")
		}
	})

	t.Run("seeded and recorded observations", func(t *testing.T) {
		mixed := append([]ConversationQualityObservation(nil), observations...)
		mixed[1].ObservationKind = ConversationQualityRecordedRun
		if _, err := EvaluateConversationQuality(cases, mixed); err == nil {
			t.Fatal("EvaluateConversationQuality() accepted mixed observation kinds")
		}
	})
}

func conversationQualityFixture() ([]ConversationQualityCase, []ConversationQualityObservation) {
	knowledgeHash := strings.Repeat("a", 64)
	attachmentHash := strings.Repeat("b", 64)
	webHash := strings.Repeat("c", 64)
	cases := []ConversationQualityCase{
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "knowledge-answer", UserQuery: "报工失败后多久重试？",
			RelevantSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceKnowledgeChunk, SourceRef: qualityKnowledgeRef,
				ContentSHA256: knowledgeHash, PreviewRequired: true,
			}},
			RequiredCitationRefs: []string{qualityKnowledgeRef}, RequiredAnswerTerms: []string{"30 秒"},
			ForbiddenAnswerTerms: []string{"立即无限重试"}, ExpectedOutcome: ConversationQualityAnswered,
		},
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "visual-attachment-degraded", UserQuery: "这张扫描图说明了什么？",
			RelevantSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceAttachment, SourceRef: qualityAttachmentRef,
				ContentSHA256: attachmentHash, PreviewRequired: true,
			}},
			RequiredCitationRefs: []string{qualityAttachmentRef}, RequiredAnswerTerms: []string{"需要 OCR"},
			ExpectedOutcome:          ConversationQualityDegraded,
			ExpectedDegradedChannels: []string{"attachment_visual_only"},
		},
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "public-go-version", UserQuery: "Go 1.25 的发布时间是什么？",
			RelevantSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceWebPage, SourceRef: qualityWebRef,
				ContentSHA256: webHash,
			}},
			RequiredCitationRefs: []string{qualityWebRef}, RequiredAnswerTerms: []string{"Go 1.25"},
			ExpectedOutcome: ConversationQualityAnswered,
		},
	}
	observations := []ConversationQualityObservation{
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "knowledge-answer", RunID: "seed-knowledge-1",
			ObservationKind: ConversationQualitySeededContract, Model: "fixture", ModelVersion: "v1",
			PromptVersion: "conversation-quality-contract-v1", Answer: "失败后等待 30 秒再重试。",
			Outcome: ConversationQualityAnswered,
			RetrievedSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceKnowledgeChunk, SourceRef: qualityKnowledgeRef,
				ContentSHA256: knowledgeHash, PreviewRequired: true,
			}},
			Citations: []ConversationQualityCitation{{
				SourceType: EvidenceSourceKnowledgeChunk, SourceRef: qualityKnowledgeRef,
				ContentSHA256: knowledgeHash, PreviewContentSHA256: knowledgeHash,
			}},
			Usage:          ModelUsage{ModelCalls: 1, PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			DurationMillis: 100, EstimatedCostCNY: 0.001,
			Judge: &ConversationQualityJudgeObservation{
				Method: "human", JudgeID: "reviewer-a", RubricVersion: "answer-rubric-v1",
				Faithfulness: 1, AnswerRelevance: 0.9, CitationAlignment: 1,
			},
		},
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "visual-attachment-degraded", RunID: "seed-attachment-1",
			ObservationKind: ConversationQualitySeededContract, Model: "fixture", ModelVersion: "v1",
			PromptVersion: "conversation-quality-contract-v1", Answer: "当前只有视觉附件元数据，需要 OCR 后才能判断内容。",
			Outcome: ConversationQualityDegraded,
			RetrievedSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceAttachment, SourceRef: qualityAttachmentRef,
				ContentSHA256: attachmentHash, PreviewRequired: true,
			}},
			Citations: []ConversationQualityCitation{{
				SourceType: EvidenceSourceAttachment, SourceRef: qualityAttachmentRef,
				ContentSHA256: attachmentHash, PreviewContentSHA256: attachmentHash,
			}},
			DegradedChannels: []string{"attachment_visual_only"},
			Usage:            ModelUsage{ModelCalls: 1, PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
			DurationMillis:   300, EstimatedCostCNY: 0.0005,
		},
		{
			DatasetVersion: "conversation-quality-v1", CaseID: "public-go-version", RunID: "seed-web-1",
			ObservationKind: ConversationQualitySeededContract, Model: "fixture", ModelVersion: "v1",
			PromptVersion: "conversation-quality-contract-v1", Answer: "Go 1.25 的发布信息以官方页面为准。",
			Outcome: ConversationQualityAnswered,
			RetrievedSources: []ConversationQualitySource{{
				SourceType: EvidenceSourceWebPage, SourceRef: qualityWebRef, ContentSHA256: webHash,
			}},
			Citations: []ConversationQualityCitation{{
				SourceType: EvidenceSourceWebPage, SourceRef: qualityWebRef, ContentSHA256: webHash,
			}},
			Usage:          ModelUsage{ModelCalls: 1, PromptTokens: 75, CompletionTokens: 25, TotalTokens: 100},
			DurationMillis: 200, EstimatedCostCNY: 0.002,
		},
	}
	return cases, observations
}
