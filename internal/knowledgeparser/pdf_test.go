package knowledgeparser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestPDFParserExtractsEmbeddedTextPerPage(t *testing.T) {
	parser, err := NewPDFParser(testParserLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := parser.Parse(context.Background(), Input{
		MediaType: "application/pdf", OriginalName: "manual.pdf",
		Content: buildTestPDF("Connection pool timeout"),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.ParserVersion != PDFParserVersion || len(result.Elements) != 1 ||
		result.Elements[0].ElementType != knowledge.ElementText ||
		result.Elements[0].PageNumber == nil || *result.Elements[0].PageNumber != 1 ||
		!strings.Contains(result.Elements[0].ContentText, "Connection pool timeout") {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Pages) != 1 || result.Pages[0].NativeTextRunes == 0 ||
		result.Pages[0].NonWhitespaceRunes == 0 || result.Pages[0].PrintableRatio != 1 ||
		!result.Pages[0].ExtractionComplete || result.Pages[0].VisualCandidatesKnown {
		t.Fatalf("page observations = %+v", result.Pages)
	}
}

func TestPDFParserRejectsPageCountAboveLimit(t *testing.T) {
	limits := testParserLimits()
	limits.MaxDocumentUnits = 1
	parser, _ := NewPDFParser(limits)
	_, err := parser.Parse(context.Background(), Input{
		MediaType: "application/pdf", OriginalName: "manual.pdf",
		Content: buildTestPDF("first page", "second page"),
	})
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestPDFParserRejectsExtractedTextAboveLimit(t *testing.T) {
	limits := testParserLimits()
	limits.MaxExtractedRunes = 1000
	parser, _ := NewPDFParser(limits)
	_, err := parser.Parse(context.Background(), Input{
		MediaType: "application/pdf", OriginalName: "manual.pdf",
		Content: buildTestPDF(strings.Repeat("x", 1001)),
	})
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestPDFParserRoutesPageWithoutEmbeddedTextToVisualEnrichment(t *testing.T) {
	parser, _ := NewPDFParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: "application/pdf", OriginalName: "scan.pdf", Content: buildTestPDF(""),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Elements) != 0 || len(result.VisualAssets) != 1 ||
		result.VisualAssets[0].Kind != VisualAssetDocumentPage ||
		result.VisualAssets[0].PageNumber == nil || *result.VisualAssets[0].PageNumber != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Pages) != 1 || result.Pages[0].NativeTextRunes != 0 ||
		result.Pages[0].VisualCandidatesKnown {
		t.Fatalf("page observations = %+v", result.Pages)
	}
}

func buildTestPDF(pageTexts ...string) []byte {
	var output bytes.Buffer
	offsets := []int{0}
	output.WriteString("%PDF-1.4\n")
	writeObject := func(id int, body string) {
		for len(offsets) <= id {
			offsets = append(offsets, 0)
		}
		offsets[id] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", id, body)
	}
	pageIDs := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		pageIDs = append(pageIDs, fmt.Sprintf("%d 0 R", 3+index))
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(pageIDs, " "), len(pageTexts)))
	fontID := 3 + len(pageTexts)
	for index := range pageTexts {
		contentID := fontID + 1 + index
		writeObject(3+index, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontID, contentID))
	}
	writeObject(fontID, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for index, text := range pageTexts {
		text = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		writeObject(fontID+1+index, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xrefOffset := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for id := 1; id < len(offsets); id++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xrefOffset)
	return output.Bytes()
}
