package knowledgeparser

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

type flowOptions struct {
	PageNumber     *int
	SectionPath    []string
	CaptureHeading bool
}

func parseFlowElements(
	ctx context.Context,
	content []byte,
	budget *runeBudget,
	options flowOptions,
) ([]knowledge.DocumentElement, error) {
	decoder := newStrictXMLDecoder(content)
	sectionPath := append([]string(nil), options.SectionPath...)
	var (
		elements       []knowledge.DocumentElement
		paragraph      strings.Builder
		paragraphDepth int
		paragraphStyle string
		textDepth      int
		tableDepth     int
		tableRows      [][]string
		currentRow     []string
		currentCell    strings.Builder
		tableSection   []string
	)

	appendValue := func(value string) {
		if tableDepth > 0 {
			currentCell.WriteString(value)
		} else {
			paragraph.WriteString(value)
		}
	}
	appendElement := func(elementType knowledge.ElementType, text string, path []string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		if err := budget.consume(text); err != nil {
			return err
		}
		if len(elements) >= maxParserElements {
			return fmt.Errorf("%w: element count exceeds limit %d", ErrResourceLimit, maxParserElements)
		}
		var pageNumber *int
		if options.PageNumber != nil {
			value := *options.PageNumber
			pageNumber = &value
		}
		elements = append(elements, knowledge.DocumentElement{
			Index: len(elements), PageNumber: pageNumber, ElementType: elementType,
			SectionPath: append([]string(nil), path...), ContentText: text,
		})
		return nil
	}
	flushParagraph := func() error {
		text := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if text == "" {
			return nil
		}
		if options.CaptureHeading {
			if level := ooxmlHeadingLevel(paragraphStyle); level > 0 {
				if level > len(sectionPath)+1 {
					level = len(sectionPath) + 1
				}
				sectionPath = append(append([]string(nil), sectionPath[:level-1]...), text)
				return nil
			}
		}
		return appendElement(knowledge.ElementText, text, sectionPath)
	}
	flushTable := func() error {
		rows := make([]string, 0, len(tableRows))
		for _, cells := range tableRows {
			if row := strings.TrimSpace(strings.Join(cells, " | ")); row != "" {
				rows = append(rows, row)
			}
		}
		tableRows = nil
		return appendElement(knowledge.ElementTable, strings.Join(rows, "\n"), tableSection)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode Office document XML: %v", ErrInvalidContent, err)
		}
		switch current := token.(type) {
		case xml.StartElement:
			switch current.Name.Local {
			case "tbl":
				if tableDepth == 0 {
					if paragraph.Len() > 0 {
						if err := flushParagraph(); err != nil {
							return nil, err
						}
					}
					tableRows = nil
					tableSection = append([]string(nil), sectionPath...)
				}
				tableDepth++
			case "tr":
				if tableDepth == 1 {
					currentRow = nil
				}
			case "tc":
				if tableDepth == 1 {
					currentCell.Reset()
				}
			case "p":
				paragraphDepth++
				if tableDepth == 0 && paragraphDepth == 1 {
					paragraph.Reset()
					paragraphStyle = ""
				}
			case "pStyle":
				if tableDepth == 0 && paragraphDepth == 1 {
					paragraphStyle = xmlAttribute(current.Attr, "val")
				}
			case "t":
				textDepth++
			case "tab":
				appendValue("\t")
			case "br", "cr":
				appendValue("\n")
			}
		case xml.CharData:
			if textDepth > 0 {
				appendValue(string(current))
			}
		case xml.EndElement:
			switch current.Name.Local {
			case "t":
				if textDepth > 0 {
					textDepth--
				}
			case "p":
				if tableDepth == 0 && paragraphDepth == 1 {
					if err := flushParagraph(); err != nil {
						return nil, err
					}
				} else if tableDepth > 0 && currentCell.Len() > 0 {
					currentCell.WriteString(" ")
				}
				if paragraphDepth > 0 {
					paragraphDepth--
				}
			case "tc":
				if tableDepth == 1 {
					currentRow = append(currentRow, strings.TrimSpace(currentCell.String()))
					currentCell.Reset()
				}
			case "tr":
				if tableDepth == 1 {
					tableRows = append(tableRows, append([]string(nil), currentRow...))
					currentRow = nil
				}
			case "tbl":
				if tableDepth == 1 {
					if err := flushTable(); err != nil {
						return nil, err
					}
				}
				if tableDepth > 0 {
					tableDepth--
				}
			}
		}
	}
	if tableDepth != 0 || paragraphDepth != 0 || textDepth != 0 {
		return nil, fmt.Errorf("%w: Office document XML has unbalanced content", ErrInvalidContent)
	}
	return elements, nil
}

func ooxmlHeadingLevel(style string) int {
	value := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	if value == "title" {
		return 1
	}
	if !strings.HasPrefix(value, "heading") {
		return 0
	}
	level, err := strconv.Atoi(strings.TrimPrefix(value, "heading"))
	if err != nil || level < 1 || level > 6 {
		return 0
	}
	return level
}
