package knowledgeparser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	pdfreader "github.com/ledongthuc/pdf"
)

const PDFParserVersion = "pdf-embedded-text-elements-v1"

type PDFParser struct {
	limits Limits
}

func NewPDFParser(limits Limits) (PDFParser, error) {
	if err := limits.Validate(); err != nil {
		return PDFParser{}, err
	}
	return PDFParser{limits: limits}, nil
}

func (PDFParser) Supports(mediaType string) bool { return mediaType == "application/pdf" }

func (p PDFParser) Parse(ctx context.Context, input Input) (result Result, err error) {
	if err := p.limits.Validate(); err != nil {
		return Result{}, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{}
			err = fmt.Errorf("%w: PDF parser panic: %v", ErrInvalidContent, recovered)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	reader, err := pdfreader.NewReader(bytes.NewReader(input.Content), int64(len(input.Content)))
	if err != nil {
		if errors.Is(err, pdfreader.ErrInvalidPassword) {
			return Result{}, fmt.Errorf("%w: encrypted PDF is not supported", ErrInvalidContent)
		}
		return Result{}, fmt.Errorf("%w: open PDF: %v", ErrInvalidContent, err)
	}
	pageCount := reader.NumPage()
	if pageCount < 1 {
		return Result{}, fmt.Errorf("%w: PDF has no pages", ErrInvalidContent)
	}
	if pageCount > p.limits.MaxDocumentUnits {
		return Result{}, fmt.Errorf("%w: PDF page count %d exceeds limit %d", ErrResourceLimit, pageCount, p.limits.MaxDocumentUnits)
	}

	budget := newRuneBudget(p.limits.MaxExtractedRunes)
	elements := make([]knowledge.DocumentElement, 0, pageCount)
	visualAssets := make([]VisualAsset, 0)
	pages := make([]PageObservation, 0, pageCount)
	emptyPages := 0
	appendPageAsset := func(pageNumber int) error {
		if len(visualAssets) >= p.limits.MaxVisualAssets {
			return fmt.Errorf("%w: visual asset count exceeds limit %d", ErrResourceLimit, p.limits.MaxVisualAssets)
		}
		asset, err := newDocumentPageVisualAsset(len(visualAssets), pageNumber, input.Content)
		if err != nil {
			return err
		}
		visualAssets = append(visualAssets, asset)
		return nil
	}
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		page := reader.Page(pageNumber)
		if page.V.IsNull() || page.V.Key("Contents").Kind() == pdfreader.Null {
			emptyPages++
			pages = append(pages, observePageText(pageNumber, "", true))
			if err := appendPageAsset(pageNumber); err != nil {
				return Result{}, err
			}
			continue
		}
		rows, err := page.GetTextByRow()
		if err != nil {
			return Result{}, fmt.Errorf("%w: extract PDF page %d: %v", ErrInvalidContent, pageNumber, err)
		}
		var lines []string
		for _, row := range rows {
			words := make([]string, 0, len(row.Content))
			for _, item := range row.Content {
				value := strings.TrimSpace(strings.ReplaceAll(item.S, "\x00", ""))
				if value != "" {
					words = append(words, value)
				}
			}
			if line := strings.TrimSpace(strings.Join(words, " ")); line != "" {
				lines = append(lines, line)
			}
		}
		text := strings.TrimSpace(strings.Join(lines, "\n"))
		if text == "" {
			emptyPages++
			pages = append(pages, observePageText(pageNumber, "", true))
			if err := appendPageAsset(pageNumber); err != nil {
				return Result{}, err
			}
			continue
		}
		if err := budget.consume(text); err != nil {
			return Result{}, err
		}
		pages = append(pages, observePageText(pageNumber, text, true))
		pageValue := pageNumber
		elements = append(elements, knowledge.DocumentElement{
			Index: len(elements), PageNumber: &pageValue,
			ElementType: knowledge.ElementText, ContentText: text,
		})
	}
	if len(elements) == 0 && len(visualAssets) == 0 {
		return Result{}, fmt.Errorf("%w: PDF contains no extractable embedded text", ErrInvalidContent)
	}
	metadata, err := json.Marshal(map[string]any{
		"mediaType": input.MediaType, "pageCount": pageCount,
		"textPageCount": len(elements), "emptyPageCount": emptyPages,
		"visualAssetCount": len(visualAssets), "extractionMode": "embedded_text",
		"visualEnrichmentRequired": len(visualAssets) > 0,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		ParserVersion: PDFParserVersion, Elements: elements,
		VisualAssets: visualAssets, Pages: pages, Metadata: metadata,
	}, nil
}
