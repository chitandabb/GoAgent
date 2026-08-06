package knowledgeparser

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func TestTextParserRoutesMarkdownAndPreservesElementTypes(t *testing.T) {
	router, err := NewRouter(TextParser{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Parse(context.Background(), Input{
		MediaType: "text/markdown; charset=utf-8", OriginalName: "manual.md",
		Content: []byte("# 故障手册\n\n检查连接池。\n\n| 项目 | 值 |\n| --- | --- |\n| timeout | 30s |"),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.ParserVersion != MarkdownParserVersion || len(result.Elements) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Elements[0].ElementType != knowledge.ElementText || result.Elements[1].ElementType != knowledge.ElementTable {
		t.Fatalf("elements = %+v", result.Elements)
	}
	if strings.Join(result.Elements[0].SectionPath, "/") != "故障手册" {
		t.Fatalf("section path = %#v", result.Elements[0].SectionPath)
	}
}

func TestPageObservationRejectsInvalidValues(t *testing.T) {
	tests := []PageObservation{
		{},
		{PageNumber: 1, NativeTextRunes: 1, NonWhitespaceRunes: 2},
		{PageNumber: 1, PrintableRatio: math.NaN()},
		{PageNumber: 1, VisualCandidateCount: 1},
	}
	for _, page := range tests {
		if err := page.Validate(); err == nil {
			t.Fatalf("Validate accepted %+v", page)
		}
	}
}

func TestTextParserRejectsUnsupportedAndInvalidInputs(t *testing.T) {
	router, _ := NewRouter(TextParser{})
	if _, err := router.Parse(context.Background(), Input{
		MediaType: "application/pdf", Content: []byte("%PDF"),
	}); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("unsupported error = %v", err)
	}
	if _, err := router.Parse(context.Background(), Input{
		MediaType: "text/plain", Content: []byte{0xff},
	}); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
}
