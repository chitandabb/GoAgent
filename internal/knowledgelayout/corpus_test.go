package knowledgelayout

import "testing"

func TestRoutingCorpusValidateCases(t *testing.T) {
	pageCount := 3
	corpus := RoutingCorpus{
		DatasetVersion: "layout-v1",
		Documents: []RoutingCorpusDocument{{
			DocumentID: "doc-a", Title: "Fixture", Publisher: "Publisher",
			SourceURL: "https://example.com/source", DownloadURL: "https://example.com/file.pdf",
			UsageBasis: "Public evaluation fixture.", FileName: "file.pdf", MediaType: "application/pdf",
			SizeBytes: 10, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			PageCount: &pageCount, Coverage: []string{"native-text"},
		}},
	}
	cases := []RoutingEvaluationCase{{
		DatasetVersion: "layout-v1", CaseID: "case-a", DocumentID: "doc-a", PageNumber: 3,
		ExpectedPageClass: PageNativeDigital, ExpectedRoutes: []ProcessingRoute{RouteNativeText},
	}}
	if err := corpus.ValidateCases(cases); err != nil {
		t.Fatal(err)
	}
	cases[0].PageNumber = 4
	if err := corpus.ValidateCases(cases); err == nil {
		t.Fatal("expected page count error")
	}
}

func TestRoutingCorpusRejectsTraversalFileName(t *testing.T) {
	document := RoutingCorpusDocument{
		DocumentID: "doc-a", Title: "Fixture", Publisher: "Publisher",
		SourceURL: "https://example.com/source", DownloadURL: "https://example.com/file.pdf",
		UsageBasis: "Public evaluation fixture.", FileName: "../file.pdf", MediaType: "application/pdf",
		SizeBytes: 10, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Coverage: []string{"native-text"},
	}
	if err := document.Validate(); err == nil {
		t.Fatal("expected traversal file name error")
	}
}
