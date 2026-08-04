package knowledgeparser

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestOOXMLParserExtractsDOCXParagraphsAndTables(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: DOCXMediaType, OriginalName: "manual.docx",
		Content: officeFixture(map[string]string{
			"word/document.xml": `<w:document xmlns:w="w"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Database</w:t></w:r></w:p>
<w:p><w:r><w:t>Check connection pool.</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>timeout</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>30s</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:body></w:document>`,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.ParserVersion != DOCXParserVersion || len(result.Elements) != 2 ||
		result.Elements[0].ElementType != knowledge.ElementText ||
		strings.Join(result.Elements[0].SectionPath, "/") != "Database" ||
		result.Elements[1].ElementType != knowledge.ElementTable ||
		result.Elements[1].ContentText != "timeout | 30s" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserExtractsXLSXSheetsAndCellTypes(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: XLSXMediaType, OriginalName: "data.xlsx",
		Content: officeFixture(map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Faults" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/sharedStrings.xml":       `<sst><si><t>timeout</t></si></sst>`,
			"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>30s</t></is></c></row>
<row r="2"><c r="A2"><v>5</v></c></row>
</sheetData></worksheet>`,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.ParserVersion != XLSXParserVersion || len(result.Elements) != 1 ||
		result.Elements[0].ElementType != knowledge.ElementTable ||
		strings.Join(result.Elements[0].SectionPath, "/") != "Faults" ||
		result.Elements[0].ContentText != "timeout | 30s\n5" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserExtractsPPTXInPresentationOrder(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: PPTXMediaType, OriginalName: "slides.pptx",
		Content: officeFixture(map[string]string{
			"ppt/presentation.xml":            `<p:presentation xmlns:p="p" xmlns:r="relationships"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`,
			"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/><Relationship Id="rId2" Target="slides/slide2.xml"/></Relationships>`,
			"ppt/slides/slide1.xml":           `<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`,
			"ppt/slides/slide2.xml":           `<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>First</a:t></a:r></a:p></p:sld>`,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.ParserVersion != PPTXParserVersion || len(result.Elements) != 2 ||
		result.Elements[0].ContentText != "First" || result.Elements[1].ContentText != "Second" ||
		result.Elements[0].PageNumber == nil || *result.Elements[0].PageNumber != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserRejectsExpandedArchiveAndWorksheetLimits(t *testing.T) {
	t.Run("archive entries", func(t *testing.T) {
		limits := testParserLimits()
		limits.MaxArchiveEntries = 1
		parser, _ := NewOOXMLParser(limits)
		_, err := parser.Parse(context.Background(), Input{
			MediaType: DOCXMediaType, OriginalName: "manual.docx",
			Content: officeFixture(map[string]string{
				"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>text</w:t></w:r></w:p></w:body></w:document>`,
			}),
		})
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("Parse error = %v", err)
		}
	})

	t.Run("expanded archive", func(t *testing.T) {
		limits := testParserLimits()
		limits.MaxExpandedBytes = 1024 * 1024
		limits.MaxXMLBytes = 64 * 1024
		parser, _ := NewOOXMLParser(limits)
		_, err := parser.Parse(context.Background(), Input{
			MediaType: DOCXMediaType, OriginalName: "manual.docx",
			Content: officeFixture(map[string]string{
				"word/document.xml":    `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>text</w:t></w:r></w:p></w:body></w:document>`,
				"word/media/large.bin": strings.Repeat("x", 1024*1024),
			}),
		})
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("Parse error = %v", err)
		}
	})

	t.Run("single XML part", func(t *testing.T) {
		limits := testParserLimits()
		limits.MaxXMLBytes = 64 * 1024
		parser, _ := NewOOXMLParser(limits)
		_, err := parser.Parse(context.Background(), Input{
			MediaType: DOCXMediaType, OriginalName: "manual.docx",
			Content: officeFixture(map[string]string{
				"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>` + strings.Repeat("x", 64*1024) + `</w:t></w:r></w:p></w:body></w:document>`,
			}),
		})
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("Parse error = %v", err)
		}
	})

	t.Run("worksheet rows", func(t *testing.T) {
		limits := testParserLimits()
		limits.MaxSpreadsheetRows = 1
		parser, _ := NewOOXMLParser(limits)
		_, err := parser.Parse(context.Background(), Input{
			MediaType: XLSXMediaType, OriginalName: "data.xlsx",
			Content: officeFixture(map[string]string{
				"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Faults" r:id="rId1"/></sheets></workbook>`,
				"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
				"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row><c><v>1</v></c></row><row><c><v>2</v></c></row></sheetData></worksheet>`,
			}),
		})
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("Parse error = %v", err)
		}
	})
}

func TestSpreadsheetColumnIndexValidatesCompleteCellReference(t *testing.T) {
	for reference, want := range map[string]int{"A1": 0, "Z9": 25, "AA10": 26} {
		got, err := spreadsheetColumnIndex(reference)
		if err != nil || got != want {
			t.Fatalf("spreadsheetColumnIndex(%q) = %d, %v; want %d", reference, got, err, want)
		}
	}
	for _, reference := range []string{"A", "1", "A1junk", "A0", "XFE1"} {
		got, err := spreadsheetColumnIndex(reference)
		if err == nil {
			t.Fatalf("spreadsheetColumnIndex(%q) = %d, nil", reference, got)
		}
	}
}

func TestParseWorksheetEnforcesRuneBudgetWhileBuildingOutput(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	content := []byte(`<worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>timeout</t></is></c></row></sheetData></worksheet>`)

	_, _, err := parser.parseWorksheet(context.Background(), content, nil, newRuneBudget(6))
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("parseWorksheet error = %v", err)
	}
}

func testParserLimits() Limits {
	return Limits{
		MaxDocumentUnits: 20, MaxArchiveEntries: 100,
		MaxExpandedBytes: 2 * 1024 * 1024, MaxXMLBytes: 1024 * 1024,
		MaxExtractedRunes: 100_000, MaxSpreadsheetRows: 100,
		MaxSpreadsheetColumns: 32,
	}
}

func officeFixture(parts map[string]string) []byte {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	allParts := map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}
	for name, content := range parts {
		allParts[name] = content
	}
	for name, content := range allParts {
		entry, err := writer.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return output.Bytes()
}
