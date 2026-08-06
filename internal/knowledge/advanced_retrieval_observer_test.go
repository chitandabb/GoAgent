package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type advancedRetrievalSearcherStub struct {
	result HybridSearch
	err    error
}

func (s advancedRetrievalSearcherStub) Search(
	context.Context,
	uuid.UUID,
	string,
	int,
) (HybridSearch, error) {
	return s.result, s.err
}

func TestAdvancedRetrievalObserverBuildsContextPair(t *testing.T) {
	documentID := uuid.New()
	versionID := uuid.New()
	hitID := uuid.New()
	neighborID := uuid.New()
	hitText := "连接池等待超时"
	neighborText := "检查最大连接数与慢事务"
	plan, err := OriginalQueryPlan("connection timeout")
	if err != nil {
		t.Fatal(err)
	}
	hit := SearchResult{
		DocumentID: documentID, DocumentVersionID: versionID, ChunkID: hitID,
		Title: "连接池排查", Scope: ScopeGlobal, Ordinal: 0, ElementType: ElementText,
		ContentText: hitText, ContentSHA256: SHA256Hex(hitText),
	}
	baselineSearch := HybridSearch{
		Results: []SearchResult{hit}, QueryPlan: plan, Sources: []string{"fts", "vector"},
		QueryRewriteStatus: QueryRewriteDisabled,
	}
	experimentSearch := baselineSearch
	experimentSearch.ContextExpanded = true
	experimentSearch.ContextGroups = []SearchContextGroup{{
		DocumentID: documentID, DocumentVersionID: versionID, HitChunkIDs: []uuid.UUID{hitID},
		Chunks: []SearchContextChunk{{
			ChunkID: neighborID, Ordinal: 1, ElementType: ElementText,
			ContentText: neighborText, ContentSHA256: SHA256Hex(neighborText),
		}},
	}}
	observer, err := NewAdvancedRetrievalObserver(
		advancedRuntimeArm(RetrievalQueryOriginal, RetrievalContextChild, advancedRetrievalSearcherStub{result: baselineSearch}),
		advancedRuntimeArm(RetrievalQueryOriginal, RetrievalContextParent, advancedRetrievalSearcherStub{result: experimentSearch}),
		map[uuid.UUID]string{documentID: "connection-pool"},
	)
	if err != nil {
		t.Fatal(err)
	}
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "connection-timeout", Query: "connection timeout", K: 2,
		RelevantDocumentKeys: []string{"connection-pool"},
		RelevantChunks: []RetrievalEvaluationChunkRef{
			{DocumentKey: "connection-pool", Ordinal: 0, ContentSHA256: SHA256Hex(hitText)},
			{DocumentKey: "connection-pool", Ordinal: 1, ContentSHA256: SHA256Hex(neighborText)},
		},
	}
	observations, err := observer.Observe(context.Background(), uuid.New(), []AdvancedRetrievalEvaluationCase{definition})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].FTSQueryCount != 1 || observations[0].VectorQueryCount != 1 ||
		observations[0].HitContextRunes != len([]rune(hitText)) || observations[1].ExpandedContextRunes != len([]rune(neighborText)) ||
		!observations[1].ContextExpanded || len(observations[1].ReturnedContextChunks) != 1 {
		t.Fatalf("observations = %+v", observations)
	}
	summary, err := EvaluateAdvancedRetrieval([]AdvancedRetrievalEvaluationCase{definition}, observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Baseline.ContextRecall != 0.5 || summary.Experiment.ContextRecall != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestAdvancedRetrievalObserverRecordsFailedRewriteArm(t *testing.T) {
	documentID := uuid.New()
	versionID := uuid.New()
	chunkID := uuid.New()
	content := "error 1205 deadlock victim"
	plan, err := OriginalQueryPlan("error 1205")
	if err != nil {
		t.Fatal(err)
	}
	baselineResult := HybridSearch{
		Results: []SearchResult{{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "SQL Server deadlock", Scope: ScopeGlobal, Ordinal: 0, ElementType: ElementText,
			ContentText: content, ContentSHA256: SHA256Hex(content),
		}},
		QueryPlan: plan, QueryRewriteStatus: QueryRewriteDisabled,
	}
	baseline := advancedRuntimeArm(
		RetrievalQueryOriginal, RetrievalContextChild, advancedRetrievalSearcherStub{result: baselineResult},
	)
	experiment := advancedRuntimeArm(
		RetrievalQueryRewrite, RetrievalContextChild, advancedRetrievalSearcherStub{err: errors.New("provider unavailable")},
	)
	experiment.Arm.RewriteProvider = "stepfun"
	experiment.Arm.RewriteModelID = "step-3.7-flash"
	experiment.Arm.RewritePromptVersion = "query-rewrite-v1"
	observer, err := NewAdvancedRetrievalObserver(
		baseline, experiment, map[uuid.UUID]string{documentID: "deadlock-1205"},
	)
	if err != nil {
		t.Fatal(err)
	}
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "deadlock", Query: "error 1205", K: 2,
		RelevantDocumentKeys: []string{"deadlock-1205"},
		RelevantChunks: []RetrievalEvaluationChunkRef{{
			DocumentKey: "deadlock-1205", Ordinal: 0, ContentSHA256: SHA256Hex(content),
		}},
	}
	observations, err := observer.Observe(context.Background(), uuid.New(), []AdvancedRetrievalEvaluationCase{definition})
	if err != nil {
		t.Fatal(err)
	}
	failed := observations[1]
	if failed.ErrorType != "search_failed" || failed.QueryRewriteStatus != queryRewriteNotObserved ||
		len(failed.DegradedChannels) != 1 || failed.DegradedChannels[0] != "search" {
		t.Fatalf("failed observation = %+v", failed)
	}
	summary, err := EvaluateAdvancedRetrieval([]AdvancedRetrievalEvaluationCase{definition}, observations)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Experiment.FailedRuns != 1 || summary.Experiment.RewriteNotObserved != 1 ||
		summary.Delta.RecallAtK != -1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestAdvancedRetrievalObserverPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	arm := advancedRuntimeArm(
		RetrievalQueryOriginal, RetrievalContextChild, advancedRetrievalSearcherStub{err: context.Canceled},
	)
	experiment := arm
	experiment.Arm.ContextMode = RetrievalContextParent
	observer, err := NewAdvancedRetrievalObserver(
		arm, experiment, map[uuid.UUID]string{uuid.New(): "document-a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: "rag-advanced-v1", CaseID: "case-a", Query: "alpha", K: 1,
		RelevantDocumentKeys: []string{"document-a"},
		RelevantChunks: []RetrievalEvaluationChunkRef{{
			DocumentKey: "document-a", Ordinal: 0, ContentSHA256: SHA256Hex("alpha"),
		}},
	}
	observations, err := observer.Observe(ctx, uuid.New(), []AdvancedRetrievalEvaluationCase{definition})
	if !errors.Is(err, context.Canceled) || len(observations) != 0 {
		t.Fatalf("observations=%v err=%v", observations, err)
	}
}

func advancedRuntimeArm(
	queryMode RetrievalQueryMode,
	contextMode RetrievalContextMode,
	searcher AdvancedRetrievalSearcher,
) AdvancedRetrievalRuntimeArm {
	return AdvancedRetrievalRuntimeArm{
		Arm: AdvancedRetrievalArm{
			RetrieverVersion: "postgres-rrf-v1", EmbeddingProfile: "knowledge-v1",
			QueryMode: queryMode, ContextMode: contextMode,
		},
		Searcher: searcher, FTSEnabled: true, VectorEnabled: true,
	}
}
