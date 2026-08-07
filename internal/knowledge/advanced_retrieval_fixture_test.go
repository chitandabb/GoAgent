package knowledge

import "testing"

func TestValidateAdvancedRetrievalFixturePinsGoldChunks(t *testing.T) {
	corpus := advancedRetrievalFixtureCorpus()
	chunks, err := BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		t.Fatal(err)
	}
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: corpus.DatasetVersion, CaseID: "cancel-query", Query: "cancel a database operation", K: 2,
		RelevantDocumentKeys: []string{"go-cancel-operations"},
		RelevantChunks: []RetrievalEvaluationChunkRef{{
			DocumentKey: "go-cancel-operations", Ordinal: 0,
			ContentSHA256: chunks["go-cancel-operations"][0].ContentSHA256,
		}},
	}
	validated, err := ValidateAdvancedRetrievalFixture(corpus, []AdvancedRetrievalEvaluationCase{definition})
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 2 {
		t.Fatalf("chunks = %+v", validated)
	}

	definition.RelevantChunks[0].ContentSHA256 = SHA256Hex("stale")
	if _, err := ValidateAdvancedRetrievalFixture(corpus, []AdvancedRetrievalEvaluationCase{definition}); err == nil {
		t.Fatal("ValidateAdvancedRetrievalFixture accepted a stale gold chunk")
	}
}

func TestValidateAdvancedRetrievalFixtureRequiresChunkEvidenceForEveryRelevantDocument(t *testing.T) {
	corpus := advancedRetrievalFixtureCorpus()
	chunks, err := BuildAdvancedRetrievalCorpusChunks(corpus)
	if err != nil {
		t.Fatal(err)
	}
	definition := AdvancedRetrievalEvaluationCase{
		DatasetVersion: corpus.DatasetVersion, CaseID: "two-documents", Query: "database timeout", K: 2,
		RelevantDocumentKeys: []string{"go-cancel-operations", "go-manage-connections"},
		RelevantChunks: []RetrievalEvaluationChunkRef{{
			DocumentKey: "go-cancel-operations", Ordinal: 0,
			ContentSHA256: chunks["go-cancel-operations"][0].ContentSHA256,
		}},
	}
	if _, err := ValidateAdvancedRetrievalFixture(corpus, []AdvancedRetrievalEvaluationCase{definition}); err == nil {
		t.Fatal("ValidateAdvancedRetrievalFixture accepted a relevant document without chunk evidence")
	}
}

func TestAdvancedRetrievalCorpusRejectsUnverifiableSource(t *testing.T) {
	corpus := advancedRetrievalFixtureCorpus()
	corpus.Documents[0].SourceURL = "http://localhost/private"
	if err := corpus.Validate(); err == nil {
		t.Fatal("AdvancedRetrievalEvaluationCorpus accepted an unverifiable source")
	}
}

func advancedRetrievalFixtureCorpus() AdvancedRetrievalEvaluationCorpus {
	documents := []AdvancedRetrievalEvaluationDocument{
		{
			DocumentKey: "go-cancel-operations", Title: "Canceling in-progress database operations",
			MediaType: "text/markdown", SourceURL: "https://go.dev/doc/database/cancel-operations",
			SourceRetrievedAt: "2026-08-06", Content: "# Cancel operations\n\nUse a context to cancel database operations that are no longer needed.",
		},
		{
			DocumentKey: "go-manage-connections", Title: "Managing database connections",
			MediaType: "text/markdown", SourceURL: "https://go.dev/doc/database/manage-connections",
			SourceRetrievedAt: "2026-08-06", Content: "# Manage connections\n\nThe sql.DB handle is a connection pool and is safe for concurrent use.",
		},
	}
	for index := range documents {
		documents[index].ContentSHA256 = SHA256Hex(documents[index].Content)
	}
	return AdvancedRetrievalEvaluationCorpus{
		DatasetVersion: "rag-advanced-v1", ChunkerVersion: AdvancedRetrievalMarkdownChunkerV1,
		ChunkMaxRunes: 256, ChunkOverlapRunes: 32, Documents: documents,
	}
}
