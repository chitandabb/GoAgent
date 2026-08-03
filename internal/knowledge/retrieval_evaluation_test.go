package knowledge

import "testing"

func TestEvaluateRetrieval(t *testing.T) {
	documents := []RetrievalEvaluationDocument{
		{DatasetVersion: "rag-retrieval-v1", DocumentKey: "doc-a", Title: "A", Content: "alpha", MediaType: "text/markdown"},
		{DatasetVersion: "rag-retrieval-v1", DocumentKey: "doc-b", Title: "B", Content: "beta", MediaType: "text/markdown"},
	}
	cases := []RetrievalEvaluationCase{
		{DatasetVersion: "rag-retrieval-v1", CaseID: "case-a", Query: "alpha", RelevantDocumentKeys: []string{"doc-a"}, K: 2},
		{DatasetVersion: "rag-retrieval-v1", CaseID: "case-b", Query: "beta", RelevantDocumentKeys: []string{"doc-b"}, K: 2},
	}
	observations := []RetrievalEvaluationObservation{
		{DatasetVersion: "rag-retrieval-v1", CaseID: "case-a", RunID: "a-run", Retriever: "fts-v1", Query: "alpha", K: 2, RelevantDocumentKeys: []string{"doc-a"}, ReturnedDocumentKeys: []string{"doc-a", "doc-b"}, FirstRelevantRank: 1, HitAtK: true, DurationMillis: 2},
		{DatasetVersion: "rag-retrieval-v1", CaseID: "case-b", RunID: "b-run", Retriever: "fts-v1", Query: "beta", K: 2, RelevantDocumentKeys: []string{"doc-b"}, ReturnedDocumentKeys: []string{"doc-a", "doc-b"}, FirstRelevantRank: 2, HitAtK: true, DurationMillis: 4},
	}
	summary, err := EvaluateRetrieval(documents, cases, observations, "fts-v1", 2, 100)
	if err != nil {
		t.Fatalf("EvaluateRetrieval(): %v", err)
	}
	if summary.RecallAtK != 1 || summary.MeanReciprocalRank != 0.75 || summary.AverageQueryDurationMillis != 3 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestFirstRelevantRankDeduplicatesReturnedDocuments(t *testing.T) {
	if got := FirstRelevantRank([]string{"doc-b"}, []string{"doc-a", "doc-a", "doc-b"}); got != 3 {
		t.Fatalf("FirstRelevantRank() = %d, want result-list rank 3", got)
	}
}
