package knowledge

import "testing"

func TestEvaluateAdvancedRetrievalPairedMetrics(t *testing.T) {
	a0 := advancedChunkRef("doc-a", 0, "a0")
	a1 := advancedChunkRef("doc-a", 1, "a1")
	b0 := advancedChunkRef("doc-b", 0, "b0")
	b1 := advancedChunkRef("doc-b", 1, "b1")
	c0 := advancedChunkRef("doc-c", 0, "c0")
	cases := []AdvancedRetrievalEvaluationCase{
		{
			DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha timeout",
			RelevantDocumentKeys: []string{"doc-a"}, RelevantChunks: []RetrievalEvaluationChunkRef{a0, a1},
			K: 2, Tags: []string{"multi-chunk"},
		},
		{
			DatasetVersion: "rag-advanced-v1", CaseID: "case-b", Query: "beta timeout",
			RelevantDocumentKeys: []string{"doc-b"}, RelevantChunks: []RetrievalEvaluationChunkRef{b0}, K: 2,
		},
	}
	baselineA := advancedBaselineObservation("case-a", "alpha timeout", 10)
	baselineA.ReturnedDocumentKeys = []string{"doc-a"}
	baselineA.ReturnedHitChunks = []RetrievalEvaluationChunkRef{a0}
	baselineA.HitContextRunes = 100
	baselineB := advancedBaselineObservation("case-b", "beta timeout", 20)
	baselineB.ReturnedDocumentKeys = []string{"doc-c"}
	baselineB.ReturnedHitChunks = []RetrievalEvaluationChunkRef{c0}
	baselineB.HitContextRunes = 100

	experimentA := advancedExperimentObservation("case-a", "alpha timeout", 20)
	experimentA.ReturnedDocumentKeys = []string{"doc-a"}
	experimentA.ReturnedHitChunks = []RetrievalEvaluationChunkRef{a0}
	experimentA.ReturnedContextChunks = []RetrievalEvaluationChunkRef{a1}
	experimentA.HitContextRunes = 100
	experimentA.ExpandedContextRunes = 80
	experimentA.ContextExpanded = true
	experimentB := advancedExperimentObservation("case-b", "beta timeout", 40)
	experimentB.ReturnedDocumentKeys = []string{"doc-b"}
	experimentB.ReturnedHitChunks = []RetrievalEvaluationChunkRef{b0}
	experimentB.ReturnedContextChunks = []RetrievalEvaluationChunkRef{b1}
	experimentB.HitContextRunes = 100
	experimentB.ExpandedContextRunes = 40
	experimentB.ContextExpanded = true

	summary, err := EvaluateAdvancedRetrieval(cases, []AdvancedRetrievalObservation{
		baselineA, experimentA, baselineB, experimentB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.PairedCases != 2 || summary.Baseline.HitRateAtK != 0.5 || summary.Experiment.HitRateAtK != 1 ||
		summary.Baseline.RecallAtK != 0.5 || summary.Experiment.RecallAtK != 1 ||
		summary.Baseline.MeanReciprocalRank != 0.5 || summary.Experiment.MeanReciprocalRank != 1 ||
		summary.Baseline.ContextPrecision != 0.5 || summary.Baseline.ContextRecall != 0.25 ||
		summary.Experiment.ContextPrecision != 0.75 || summary.Experiment.ContextRecall != 1 {
		t.Fatalf("quality summary = %+v", summary)
	}
	if summary.Delta.QueryAmplificationRatio != 1 || summary.Delta.ContextRuneChangeRate != 0.6 ||
		summary.Delta.DurationChangeRate != 1 || summary.Experiment.AverageContextExpansionRatio != 1.6 ||
		summary.Experiment.RewriteTotalTokens != 0 {
		t.Fatalf("cost summary = %+v", summary)
	}
}

func TestEvaluateAdvancedRetrievalDistinguishesHitRateFromDocumentRecall(t *testing.T) {
	a0 := advancedChunkRef("doc-a", 0, "a0")
	b0 := advancedChunkRef("doc-b", 0, "b0")
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha", K: 2,
		RelevantDocumentKeys: []string{"doc-a", "doc-b"},
		RelevantChunks:       []RetrievalEvaluationChunkRef{a0, b0},
	}
	baseline := advancedBaselineObservation("case-a", "alpha", 10)
	baseline.ReturnedDocumentKeys = []string{"doc-a"}
	baseline.ReturnedHitChunks = []RetrievalEvaluationChunkRef{a0}
	baseline.HitContextRunes = 10
	experiment := advancedExperimentObservation("case-a", "alpha", 10)
	experiment.ReturnedDocumentKeys = []string{"doc-a", "doc-b"}
	experiment.ReturnedHitChunks = []RetrievalEvaluationChunkRef{a0}
	experiment.ReturnedContextChunks = []RetrievalEvaluationChunkRef{b0}
	experiment.HitContextRunes = 10
	experiment.ExpandedContextRunes = 10
	experiment.ContextExpanded = true

	summary, err := EvaluateAdvancedRetrieval(
		[]AdvancedRetrievalEvaluationCase{definition},
		[]AdvancedRetrievalObservation{baseline, experiment},
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Baseline.HitRateAtK != 1 || summary.Baseline.RecallAtK != 0.5 ||
		summary.Experiment.HitRateAtK != 1 || summary.Experiment.RecallAtK != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateAdvancedRetrievalReportsProviderTokenChanges(t *testing.T) {
	chunk := advancedChunkRef("doc-a", 0, "a0")
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha", K: 2,
		RelevantDocumentKeys: []string{"doc-a"}, RelevantChunks: []RetrievalEvaluationChunkRef{chunk},
	}
	baseline := advancedBaselineObservation("case-a", "alpha", 10)
	baseline.ReturnedDocumentKeys = []string{"doc-a"}
	baseline.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	baseline.HitContextRunes = 10
	baseline.EmbeddingTotalTokens = 10
	baseline.RerankTotalTokens = 20
	experiment := advancedRewriteObservation("case-a", "alpha", 10)
	experiment.ReturnedDocumentKeys = []string{"doc-a"}
	experiment.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	experiment.HitContextRunes = 10
	experiment.EmbeddingTotalTokens = 25
	experiment.RerankTotalTokens = 30

	summary, err := EvaluateAdvancedRetrieval(
		[]AdvancedRetrievalEvaluationCase{definition},
		[]AdvancedRetrievalObservation{baseline, experiment},
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Baseline.EmbeddingTotalTokens != 10 || summary.Experiment.EmbeddingTotalTokens != 25 ||
		summary.Delta.EmbeddingTokenChangeRate != 1.5 || summary.Delta.RerankTokenChangeRate != 0.5 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateAdvancedRetrievalMeasuresContextCompressionIndependently(t *testing.T) {
	hit := advancedChunkRef("doc-a", 0, "hit")
	relevantContext := advancedChunkRef("doc-a", 1, "relevant")
	noiseContext := advancedChunkRef("doc-a", 2, "noise")
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-compression", Query: "alpha timeout", K: 2,
		RelevantDocumentKeys: []string{"doc-a"},
		RelevantChunks:       []RetrievalEvaluationChunkRef{hit, relevantContext},
	}
	baseline := advancedExperimentObservation("case-compression", "alpha timeout", 10)
	baseline.Variant = AdvancedRetrievalBaseline
	baseline.RunID = "baseline-case-compression"
	baseline.ReturnedDocumentKeys = []string{"doc-a"}
	baseline.ReturnedHitChunks = []RetrievalEvaluationChunkRef{hit}
	baseline.ReturnedContextChunks = []RetrievalEvaluationChunkRef{relevantContext, noiseContext}
	baseline.HitContextRunes = 40
	baseline.ExpandedContextRunes = 100
	baseline.ContextExpanded = true

	experiment := baseline
	experiment.Variant = AdvancedRetrievalExperiment
	experiment.RunID = "experiment-case-compression"
	experiment.ContextCompressionEnabled = true
	experiment.ContextCompressionMaxChunks = 1
	experiment.ContextCompressionMaxRunes = 128
	experiment.ContextCompressionMinScore = 0.05
	experiment.ContextCompressionApplied = true
	experiment.ContextCompression = ContextCompressionStats{
		InputChunks: 2, OutputChunks: 1, InputRunes: 100, OutputRunes: 60, OmittedChunks: 1,
	}
	experiment.ReturnedContextChunks = []RetrievalEvaluationChunkRef{relevantContext}
	experiment.ExpandedContextRunes = 60

	summary, err := EvaluateAdvancedRetrieval(
		[]AdvancedRetrievalEvaluationCase{definition},
		[]AdvancedRetrievalObservation{baseline, experiment},
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Baseline.ContextRecall != 1 || summary.Experiment.ContextRecall != 1 ||
		summary.Delta.ContextRecall != 0 || summary.Experiment.ContextPrecision != 1 ||
		summary.Experiment.CompressionAppliedRuns != 1 ||
		summary.Experiment.CompressionTriggeredRuns != 1 ||
		summary.Experiment.CompressionOmittedChunks != 1 ||
		summary.Experiment.AverageCompressionRatio != 0.6 ||
		summary.Experiment.AverageCompressionSavingsRate != 0.4 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateAdvancedRetrievalRejectsMixedProfilesAndIncompletePairs(t *testing.T) {
	chunk := advancedChunkRef("doc-a", 0, "a0")
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha",
		RelevantDocumentKeys: []string{"doc-a"}, RelevantChunks: []RetrievalEvaluationChunkRef{chunk}, K: 1,
	}
	baseline := advancedBaselineObservation("case-a", "alpha", 10)
	baseline.ReturnedDocumentKeys = []string{"doc-a"}
	baseline.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	baseline.HitContextRunes = 10
	if _, err := EvaluateAdvancedRetrieval([]AdvancedRetrievalEvaluationCase{definition}, []AdvancedRetrievalObservation{baseline}); err == nil {
		t.Fatal("EvaluateAdvancedRetrieval accepted an incomplete pair")
	}
	experiment := advancedExperimentObservation("case-a", "alpha", 10)
	experiment.EmbeddingProfile = "other-profile"
	experiment.ReturnedDocumentKeys = []string{"doc-a"}
	experiment.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	experiment.HitContextRunes = 10
	if _, err := EvaluateAdvancedRetrieval(
		[]AdvancedRetrievalEvaluationCase{definition},
		[]AdvancedRetrievalObservation{baseline, experiment},
	); err == nil {
		t.Fatal("EvaluateAdvancedRetrieval accepted mixed retrieval profiles")
	}
}

func TestEvaluateAdvancedRetrievalRejectsConfoundedPair(t *testing.T) {
	chunk := advancedChunkRef("doc-a", 0, "a0")
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha",
		RelevantDocumentKeys: []string{"doc-a"}, RelevantChunks: []RetrievalEvaluationChunkRef{chunk}, K: 2,
	}
	baseline := advancedBaselineObservation("case-a", "alpha", 10)
	baseline.ReturnedDocumentKeys = []string{"doc-a"}
	baseline.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	baseline.HitContextRunes = 10
	experiment := advancedRewriteObservation("case-a", "alpha", 10)
	experiment.ContextMode = RetrievalContextParent
	experiment.ContextExpansionEnabled = true
	experiment.ReturnedDocumentKeys = []string{"doc-a"}
	experiment.ReturnedHitChunks = []RetrievalEvaluationChunkRef{chunk}
	experiment.HitContextRunes = 10
	if _, err := EvaluateAdvancedRetrieval(
		[]AdvancedRetrievalEvaluationCase{definition},
		[]AdvancedRetrievalObservation{baseline, experiment},
	); err == nil {
		t.Fatal("EvaluateAdvancedRetrieval accepted a pair that changes two axes")
	}
}

func advancedChunkRef(documentKey string, ordinal int, content string) RetrievalEvaluationChunkRef {
	return RetrievalEvaluationChunkRef{DocumentKey: documentKey, Ordinal: ordinal, ContentSHA256: SHA256Hex(content)}
}

func advancedBaselineObservation(caseID, query string, duration float64) AdvancedRetrievalObservation {
	return AdvancedRetrievalObservation{
		DatasetVersion: "rag-advanced-v1", CaseID: caseID, Variant: AdvancedRetrievalBaseline,
		RunID: "baseline-" + caseID, Query: query, K: 2,
		RetrieverVersion: "postgres-rrf-v1", EmbeddingProfile: "knowledge-v1",
		QueryMode: RetrievalQueryOriginal, FTSQueryCount: 1, VectorQueryCount: 1,
		QueryRewriteStatus: QueryRewriteDisabled,
		ContextMode:        RetrievalContextChild, DurationMillis: duration,
	}
}

func advancedExperimentObservation(caseID, query string, duration float64) AdvancedRetrievalObservation {
	return AdvancedRetrievalObservation{
		DatasetVersion: "rag-advanced-v1", CaseID: caseID, Variant: AdvancedRetrievalExperiment,
		RunID: "experiment-" + caseID, Query: query, K: 2,
		RetrieverVersion: "postgres-rrf-v1", EmbeddingProfile: "knowledge-v1",
		QueryMode: RetrievalQueryOriginal, FTSQueryCount: 1, VectorQueryCount: 1,
		QueryRewriteStatus: QueryRewriteDisabled,
		ContextMode:        RetrievalContextParent, ContextExpansionEnabled: true, DurationMillis: duration,
	}
}

func advancedRewriteObservation(caseID, query string, duration float64) AdvancedRetrievalObservation {
	return AdvancedRetrievalObservation{
		DatasetVersion: "rag-advanced-v1", CaseID: caseID, Variant: AdvancedRetrievalExperiment,
		RunID: "experiment-rewrite-" + caseID, Query: query, K: 2,
		RetrieverVersion: "postgres-rrf-v1", EmbeddingProfile: "knowledge-v1",
		QueryMode: RetrievalQueryRewrite, FTSQueryCount: 3, VectorQueryCount: 3,
		QueryRewriteStatus: QueryRewriteAccepted, RewriteApplied: true,
		RewriteProvider: "stepfun", RewriteModelID: "step-3.7-flash", RewritePromptVersion: "query-rewrite-v1",
		RewriteUsage: QueryRewriteUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		ContextMode:  RetrievalContextChild, DurationMillis: duration,
	}
}
