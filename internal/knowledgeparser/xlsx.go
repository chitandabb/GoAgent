package knowledgeparser

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

type workbookSheet struct {
	Name           string
	RelationshipID string
}

func (p OOXMLParser) parseXLSX(ctx context.Context, archive *ooxmlArchive, budget *runeBudget) (Result, error) {
	workbookXML, err := archive.readXML("xl/workbook.xml")
	if err != nil {
		return Result{}, err
	}
	relationshipXML, err := archive.readXML("xl/_rels/workbook.xml.rels")
	if err != nil {
		return Result{}, err
	}
	relationships, err := parseOOXMLRelationships(ctx, relationshipXML, "xl")
	if err != nil {
		return Result{}, err
	}
	sheets, err := parseWorkbookSheets(ctx, workbookXML)
	if err != nil {
		return Result{}, err
	}
	if len(sheets) == 0 {
		return Result{}, fmt.Errorf("%w: XLSX contains no worksheets", ErrInvalidContent)
	}
	if len(sheets) > p.limits.MaxDocumentUnits {
		return Result{}, fmt.Errorf("%w: XLSX worksheet count %d exceeds limit %d", ErrResourceLimit, len(sheets), p.limits.MaxDocumentUnits)
	}
	sharedStrings, err := p.parseSharedStrings(ctx, archive, budget)
	if err != nil {
		return Result{}, err
	}

	elements := make([]knowledge.DocumentElement, 0, len(sheets))
	totalRows := 0
	for _, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		partName := relationships[sheet.RelationshipID]
		if partName == "" {
			return Result{}, fmt.Errorf("%w: XLSX worksheet relationship %q is missing", ErrInvalidContent, sheet.RelationshipID)
		}
		worksheetXML, err := archive.readXML(partName)
		if err != nil {
			return Result{}, err
		}
		text, rowCount, err := p.parseWorksheet(ctx, worksheetXML, sharedStrings, budget)
		if err != nil {
			return Result{}, fmt.Errorf("worksheet %q: %w", sheet.Name, err)
		}
		totalRows += rowCount
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		elements = append(elements, knowledge.DocumentElement{
			Index: len(elements), ElementType: knowledge.ElementTable,
			SectionPath: []string{sheet.Name}, ContentText: text,
		})
	}
	if len(elements) == 0 {
		return Result{}, fmt.Errorf("%w: XLSX contains no extractable cell values", ErrInvalidContent)
	}
	metadata, err := json.Marshal(map[string]any{
		"worksheetCount": len(sheets), "nonEmptyWorksheetCount": len(elements),
		"rowCount": totalRows, "sharedStringCount": len(sharedStrings),
		"extractionMode": "ooxml_spreadsheet",
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ParserVersion: XLSXParserVersion, Elements: elements, Metadata: metadata}, nil
}

func parseWorkbookSheets(ctx context.Context, content []byte) ([]workbookSheet, error) {
	decoder := newStrictXMLDecoder(content)
	var result []workbookSheet
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode XLSX workbook: %v", ErrInvalidContent, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		name := strings.TrimSpace(xmlAttribute(start.Attr, "name"))
		id := xmlRelationshipID(start.Attr)
		if name == "" || len([]rune(name)) > 256 || id == "" {
			return nil, fmt.Errorf("%w: XLSX worksheet identity is invalid", ErrInvalidContent)
		}
		result = append(result, workbookSheet{Name: name, RelationshipID: id})
	}
}

func (p OOXMLParser) parseSharedStrings(
	ctx context.Context,
	archive *ooxmlArchive,
	budget *runeBudget,
) ([]string, error) {
	if archive.files["xl/sharedStrings.xml"] == nil {
		return nil, nil
	}
	content, err := archive.readXML("xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	decoder := newStrictXMLDecoder(content)
	var (
		result    []string
		item      strings.Builder
		itemDepth int
		textDepth int
	)
	maxStrings := int64(p.limits.MaxSpreadsheetRows) * int64(p.limits.MaxSpreadsheetColumns)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode XLSX shared strings: %v", ErrInvalidContent, err)
		}
		switch current := token.(type) {
		case xml.StartElement:
			switch current.Name.Local {
			case "si":
				itemDepth++
				if itemDepth == 1 {
					item.Reset()
				}
			case "t":
				if itemDepth > 0 {
					textDepth++
				}
			}
		case xml.CharData:
			if itemDepth > 0 && textDepth > 0 {
				item.Write(current)
			}
		case xml.EndElement:
			switch current.Name.Local {
			case "t":
				if textDepth > 0 {
					textDepth--
				}
			case "si":
				if itemDepth == 1 {
					value := strings.TrimSpace(item.String())
					if err := budget.consume(value); err != nil {
						return nil, err
					}
					result = append(result, value)
					if int64(len(result)) > maxStrings {
						return nil, fmt.Errorf("%w: XLSX shared string count exceeds configured cell bounds", ErrResourceLimit)
					}
				}
				if itemDepth > 0 {
					itemDepth--
				}
			}
		}
	}
}

func (p OOXMLParser) parseWorksheet(
	ctx context.Context,
	content []byte,
	sharedStrings []string,
	budget *runeBudget,
) (string, int, error) {
	decoder := newStrictXMLDecoder(content)
	var (
		output      strings.Builder
		rowCells    []string
		occupied    map[int]struct{}
		rowCount    int
		rowDepth    int
		cellDepth   int
		cellType    string
		cellColumn  int
		valueDepth  int
		textDepth   int
		rawValue    strings.Builder
		inlineValue strings.Builder
	)
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return output.String(), rowCount, nil
		}
		if err != nil {
			return "", 0, fmt.Errorf("%w: decode XLSX worksheet: %v", ErrInvalidContent, err)
		}
		switch current := token.(type) {
		case xml.StartElement:
			switch current.Name.Local {
			case "row":
				rowDepth++
				if rowDepth == 1 {
					rowCount++
					if rowCount > p.limits.MaxSpreadsheetRows {
						return "", 0, fmt.Errorf("%w: worksheet row count exceeds limit %d", ErrResourceLimit, p.limits.MaxSpreadsheetRows)
					}
					rowCells = nil
					occupied = make(map[int]struct{})
				}
			case "c":
				if rowDepth != 1 {
					continue
				}
				cellDepth++
				if cellDepth == 1 {
					cellType = xmlAttribute(current.Attr, "t")
					cellColumn, err = spreadsheetColumnIndex(xmlAttribute(current.Attr, "r"))
					if err != nil {
						return "", 0, fmt.Errorf("%w: XLSX %v", ErrInvalidContent, err)
					}
					if cellColumn < 0 {
						cellColumn = len(rowCells)
					}
					if cellColumn >= p.limits.MaxSpreadsheetColumns {
						return "", 0, fmt.Errorf("%w: worksheet column index exceeds limit %d", ErrResourceLimit, p.limits.MaxSpreadsheetColumns)
					}
					if _, exists := occupied[cellColumn]; exists {
						return "", 0, fmt.Errorf("%w: worksheet contains duplicate cells", ErrInvalidContent)
					}
					rawValue.Reset()
					inlineValue.Reset()
				}
			case "v":
				if cellDepth > 0 {
					valueDepth++
				}
			case "t":
				if cellDepth > 0 {
					textDepth++
				}
			}
		case xml.CharData:
			if cellDepth > 0 && valueDepth > 0 {
				rawValue.Write(current)
			}
			if cellDepth > 0 && textDepth > 0 {
				inlineValue.Write(current)
			}
		case xml.EndElement:
			switch current.Name.Local {
			case "v":
				if valueDepth > 0 {
					valueDepth--
				}
			case "t":
				if textDepth > 0 {
					textDepth--
				}
			case "c":
				if cellDepth == 1 {
					value, err := spreadsheetCellValue(cellType, rawValue.String(), inlineValue.String(), sharedStrings)
					if err != nil {
						return "", 0, fmt.Errorf("%w: XLSX %v", ErrInvalidContent, err)
					}
					for len(rowCells) <= cellColumn {
						rowCells = append(rowCells, "")
					}
					rowCells[cellColumn] = value
					occupied[cellColumn] = struct{}{}
				}
				if cellDepth > 0 {
					cellDepth--
				}
			case "row":
				if rowDepth == 1 {
					for len(rowCells) > 0 && strings.TrimSpace(rowCells[len(rowCells)-1]) == "" {
						rowCells = rowCells[:len(rowCells)-1]
					}
					if row := strings.TrimSpace(strings.Join(rowCells, " | ")); row != "" {
						if output.Len() > 0 {
							row = "\n" + row
						}
						if err := budget.consume(row); err != nil {
							return "", 0, err
						}
						output.WriteString(row)
					}
				}
				if rowDepth > 0 {
					rowDepth--
				}
			}
		}
	}
}

func spreadsheetColumnIndex(reference string) (int, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return -1, nil
	}
	value := 0
	letters := 0
	letterRunes := 0
	for _, current := range reference {
		if !unicode.IsLetter(current) {
			break
		}
		current = unicode.ToUpper(current)
		if current < 'A' || current > 'Z' {
			return 0, errors.New("cell reference contains unsupported letters")
		}
		value = value*26 + int(current-'A'+1)
		letters++
		letterRunes++
	}
	if letters == 0 || value < 1 || value > 16384 {
		return 0, errors.New("cell reference is invalid")
	}
	row := reference[letterRunes:]
	if row == "" {
		return 0, errors.New("cell reference has no row number")
	}
	rowNumber, err := strconv.Atoi(row)
	if err != nil || rowNumber < 1 || rowNumber > 1_048_576 {
		return 0, errors.New("cell reference row number is invalid")
	}
	return value - 1, nil
}

func spreadsheetCellValue(cellType, rawValue, inlineValue string, sharedStrings []string) (string, error) {
	switch cellType {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(rawValue))
		if err != nil || index < 0 || index >= len(sharedStrings) {
			return "", errors.New("shared string index is invalid")
		}
		return sharedStrings[index], nil
	case "inlineStr":
		return strings.TrimSpace(inlineValue), nil
	case "b":
		if strings.TrimSpace(rawValue) == "1" {
			return "true", nil
		}
		return "false", nil
	case "", "n", "str", "e", "d":
		return strings.TrimSpace(rawValue), nil
	default:
		return "", fmt.Errorf("unsupported cell type %q", cellType)
	}
}
