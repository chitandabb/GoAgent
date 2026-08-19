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
		result.Elements[0].ContentText != "| timeout | 30s |\n| --- | --- |\n| 5 |  |" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserEscapesXLSXCellsForMarkdownTableChunks(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: XLSXMediaType, OriginalName: "data.xlsx",
		Content: officeFixture(map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Faults" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
				<row r="1"><c r="A1" t="inlineStr"><is><t>alarm|code</t></is></c><c r="B1" t="inlineStr"><is><t>meaning</t></is></c></row>
				<row r="2"><c r="A2" t="inlineStr"><is><t>E01</t></is></c><c r="B2" t="inlineStr"><is><t>line one&#10;line two</t></is></c></row>
			</sheetData></worksheet>`,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Elements) != 1 || result.Elements[0].ContentText != "| alarm\\|code | meaning |\n| --- | --- |\n| E01 | line one<br>line two |" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserPadsXLSXRowsToLaterColumnWidth(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: XLSXMediaType, OriginalName: "sparse.xlsx",
		Content: officeFixture(map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Faults" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
				<row r="1"><c r="A1" t="inlineStr"><is><t>code</t></is></c></row>
				<row r="2"><c r="A2" t="inlineStr"><is><t>E01</t></is></c><c r="B2" t="inlineStr"><is><t>overheat</t></is></c></row>
			</sheetData></worksheet>`,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Elements) != 1 || result.Elements[0].ContentText != "| code |  |\n| --- | --- |\n| E01 | overheat |" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOOXMLParserXLSXChunksRepeatWorksheetHeader(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	longValue := strings.Repeat("x", 72)
	worksheet := `<worksheet><sheetData>
		<row r="1"><c r="A1" t="inlineStr"><is><t>code</t></is></c><c r="B1" t="inlineStr"><is><t>description</t></is></c></row>
		<row r="2"><c r="A2" t="inlineStr"><is><t>E01</t></is></c><c r="B2" t="inlineStr"><is><t>` + longValue + `</t></is></c></row>
		<row r="3"><c r="A3" t="inlineStr"><is><t>E02</t></is></c><c r="B3" t="inlineStr"><is><t>` + longValue + `</t></is></c></row>
	</sheetData></worksheet>`
	result, err := parser.Parse(context.Background(), Input{
		MediaType: XLSXMediaType, OriginalName: "data.xlsx",
		Content: officeFixture(map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Faults" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/worksheets/sheet1.xml":   worksheet,
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := knowledge.ChunkElements(result.Elements, knowledge.TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatalf("ChunkElements: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk.ContentText, "| code | description |\n| --- | --- |\n") ||
			len([]rune(chunk.ContentText)) > 128 {
			t.Fatalf("chunk = %+v", chunk)
		}
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

func TestOOXMLParserExtractsEmbeddedVisualAssetsDeterministically(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: DOCXMediaType, OriginalName: "manual.docx",
		Content: officeFixtureBytes(map[string][]byte{
			"word/document.xml":     []byte(`<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>正文</w:t></w:r></w:p></w:body></w:document>`),
			"word/media/image2.png": rasterFixture(t, "png", 80, 60),
			"word/media/image1.jpg": rasterFixture(t, "jpeg", 120, 90),
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.VisualAssets) != 2 ||
		result.VisualAssets[0].SourcePath != "word/media/image1.jpg" ||
		result.VisualAssets[1].SourcePath != "word/media/image2.png" ||
		result.VisualAssets[0].SHA256 == "" {
		t.Fatalf("visual assets = %+v", result.VisualAssets)
	}
}

func TestOOXMLParserOnlyReferencesDrawingMLAndVMLImages(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: DOCXMediaType, OriginalName: "manual.docx",
		Content: officeFixtureBytes(map[string][]byte{
			"word/document.xml": []byte(`<w:document xmlns:w="w" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:f="https://example.invalid/not-drawing"><w:body>
				<w:p><w:r><w:t>Text</w:t></w:r></w:p>
				<w:hyperlink r:embed="rIdNotAnImage"><w:r><w:t>Link</w:t></w:r></w:hyperlink>
				<f:blip r:embed="rIdDecoy"/>
				<w:drawing><a:blip r:embed="rIdDrawing"/></w:drawing>
				<v:imagedata r:id="rIdVML"/>
			</w:body></w:document>`),
			"word/_rels/document.xml.rels": []byte(`<Relationships><Relationship Id="rIdNotAnImage" Target="media/decoy.png"/><Relationship Id="rIdDecoy" Target="media/decoy.png"/><Relationship Id="rIdDrawing" Target="media/drawing.png"/><Relationship Id="rIdVML" Target="media/vml.png"/></Relationships>`),
			"word/media/decoy.png":         rasterFixture(t, "png", 100, 100),
			"word/media/drawing.png":       rasterFixture(t, "png", 100, 100),
			"word/media/vml.png":           rasterFixture(t, "png", 100, 100),
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	relationships := make(map[string]string, len(result.VisualAssets))
	for _, asset := range result.VisualAssets {
		relationships[asset.SourcePath] = asset.RelationshipID
		if asset.RelationshipID != "" && asset.SourcePart != "word/document.xml" {
			t.Fatalf("visual asset source part = %+v", asset)
		}
	}
	if len(result.VisualAssets) != 3 || relationships["word/media/decoy.png"] != "" ||
		relationships["word/media/drawing.png"] != "rIdDrawing" || relationships["word/media/vml.png"] != "rIdVML" {
		t.Fatalf("visual assets = %+v", result.VisualAssets)
	}
}

func TestOOXMLParserLocatesPPTXImageByPresentationOrder(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: PPTXMediaType, OriginalName: "slides.pptx",
		Content: officeFixtureBytes(map[string][]byte{
			"ppt/presentation.xml":             []byte(`<p:presentation xmlns:p="p" xmlns:r="relationships"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`),
			"ppt/_rels/presentation.xml.rels":  []byte(`<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/><Relationship Id="rId2" Target="slides/slide2.xml"/></Relationships>`),
			"ppt/slides/slide1.xml":            []byte(`<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>First</a:t></a:r></a:p></p:sld>`),
			"ppt/slides/slide2.xml":            []byte(`<p:sld xmlns:p="p" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:pic><a:blipFill><a:blip r:embed="rIdImage"/></a:blipFill></p:pic><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`),
			"ppt/slides/_rels/slide2.xml.rels": []byte(`<Relationships><Relationship Id="rIdImage" Target="../media/image1.png"/></Relationships>`),
			"ppt/media/image1.png":             rasterFixture(t, "png", 100, 100),
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.VisualAssets) != 1 || result.VisualAssets[0].RelationshipID != "rIdImage" ||
		result.VisualAssets[0].SourcePart != "ppt/slides/slide2.xml" ||
		result.VisualAssets[0].PageNumber == nil || *result.VisualAssets[0].PageNumber != 1 {
		t.Fatalf("visual assets = %+v", result.VisualAssets)
	}
}

func TestOOXMLParserPreservesRepeatedPPTXImageOccurrences(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: PPTXMediaType, OriginalName: "slides.pptx",
		Content: officeFixtureBytes(map[string][]byte{
			"ppt/presentation.xml":             []byte(`<p:presentation xmlns:p="p" xmlns:r="relationships"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`),
			"ppt/_rels/presentation.xml.rels":  []byte(`<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/><Relationship Id="rId2" Target="slides/slide2.xml"/></Relationships>`),
			"ppt/slides/slide1.xml":            []byte(`<p:sld xmlns:p="p" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:pic><a:blipFill><a:blip r:embed="rIdShared"/></a:blipFill></p:pic><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`),
			"ppt/slides/slide2.xml":            []byte(`<p:sld xmlns:p="p" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:pic><a:blipFill><a:blip r:embed="rIdShared"/></a:blipFill></p:pic><a:p><a:r><a:t>First</a:t></a:r></a:p></p:sld>`),
			"ppt/slides/_rels/slide1.xml.rels": []byte(`<Relationships><Relationship Id="rIdShared" Target="../media/shared.png"/></Relationships>`),
			"ppt/slides/_rels/slide2.xml.rels": []byte(`<Relationships><Relationship Id="rIdShared" Target="../media/shared.png"/></Relationships>`),
			"ppt/media/shared.png":             rasterFixture(t, "png", 100, 100),
		}),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.VisualAssets) != 2 || result.VisualAssets[0].PageNumber == nil ||
		*result.VisualAssets[0].PageNumber != 1 || result.VisualAssets[1].PageNumber == nil ||
		*result.VisualAssets[1].PageNumber != 2 || result.VisualAssets[0].SourcePart != "ppt/slides/slide2.xml" ||
		result.VisualAssets[1].SourcePart != "ppt/slides/slide1.xml" ||
		&result.VisualAssets[0].Content[0] != &result.VisualAssets[1].Content[0] {
		t.Fatalf("visual assets = %+v", result.VisualAssets)
	}
}

func TestOOXMLParserRejectsIncompletePPTXPresentationRelationships(t *testing.T) {
	parser, _ := NewOOXMLParser(testParserLimits())
	_, err := parser.Parse(context.Background(), Input{
		MediaType: DOCXMediaType, OriginalName: "manual.docx",
		Content: officeFixture(map[string]string{
			"word/document.xml":    `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>text</w:t></w:r></w:p></w:body></w:document>`,
			"ppt/presentation.xml": `<p:presentation xmlns:p="p"/>`,
		}),
	})
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestOOXMLParserAcceptsCanonicalDirectoryEntries(t *testing.T) {
	parts := map[string][]byte{
		"ppt/":                            nil,
		"ppt/_rels/":                      nil,
		"ppt/slides/":                     nil,
		"ppt/presentation.xml":            []byte(`<p:presentation xmlns:p="p" xmlns:r="relationships"><p:sldIdLst><p:sldId r:id="rId1"/></p:sldIdLst></p:presentation>`),
		"ppt/_rels/presentation.xml.rels": []byte(`<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/></Relationships>`),
		"ppt/slides/slide1.xml":           []byte(`<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>Canonical package</a:t></a:r></a:p></p:sld>`),
	}
	parser, _ := NewOOXMLParser(testParserLimits())
	result, err := parser.Parse(context.Background(), Input{
		MediaType: PPTXMediaType, OriginalName: "slides.pptx", Content: officeFixtureBytes(parts),
	})
	if err != nil || len(result.Elements) != 1 {
		t.Fatalf("Parse = %+v, %v", result, err)
	}
}

func TestOOXMLParserRejectsDirectoryFileAliases(t *testing.T) {
	_, err := newOOXMLArchive(officeFixtureBytes(map[string][]byte{
		"ppt/": nil,
		"ppt":  []byte("not a directory"),
	}), testParserLimits())
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("newOOXMLArchive error = %v", err)
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

func TestOOXMLRelationshipsAllowNestedPackagePathsButRejectRootEscape(t *testing.T) {
	relationships, err := parseOOXMLRelationships(
		context.Background(),
		[]byte(`<Relationships><Relationship Id="image" Target="../media/image1.png"/><Relationship Id="custom" Target="../../customXml/item1.xml"/></Relationships>`),
		"ppt/slides",
	)
	if err != nil || relationships["image"] != "ppt/media/image1.png" ||
		relationships["custom"] != "customXml/item1.xml" {
		t.Fatalf("relationships = %+v, %v", relationships, err)
	}
	_, err = parseOOXMLRelationships(
		context.Background(),
		[]byte(`<Relationships><Relationship Id="escape" Target="../../../outside.xml"/></Relationships>`),
		"ppt/slides",
	)
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("escape error = %v", err)
	}
	_, err = parseOOXMLRelationships(
		context.Background(),
		[]byte(`<Relationships><Relationship Id="authority" Target="//server/share.xml"/></Relationships>`),
		"word",
	)
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("authority error = %v", err)
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
		MaxSpreadsheetColumns: 32, MaxVisualAssets: 20,
		MaxVisualAssetBytes: 512 * 1024, MaxTotalVisualBytes: 1024 * 1024,
	}
}

func officeFixture(parts map[string]string) []byte {
	binaryParts := make(map[string][]byte, len(parts))
	for name, content := range parts {
		binaryParts[name] = []byte(content)
	}
	return officeFixtureBytes(binaryParts)
}

func officeFixtureBytes(parts map[string][]byte) []byte {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	allParts := map[string][]byte{
		"[Content_Types].xml": []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`),
	}
	for name, content := range parts {
		allParts[name] = content
	}
	for name, content := range allParts {
		entry, err := writer.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write(content); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return output.Bytes()
}
