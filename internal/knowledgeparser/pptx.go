package knowledgeparser

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

func (p OOXMLParser) parsePPTX(ctx context.Context, archive *ooxmlArchive, budget *runeBudget) (Result, error) {
	presentation, err := archive.readXML("ppt/presentation.xml")
	if err != nil {
		return Result{}, err
	}
	relationshipXML, err := archive.readXML("ppt/_rels/presentation.xml.rels")
	if err != nil {
		return Result{}, err
	}
	relationships, err := parseOOXMLRelationships(ctx, relationshipXML, "ppt")
	if err != nil {
		return Result{}, err
	}
	partIDs, err := parsePresentationSlideIDs(ctx, presentation)
	if err != nil {
		return Result{}, err
	}
	if len(partIDs) == 0 {
		return Result{}, fmt.Errorf("%w: PPTX contains no slides", ErrInvalidContent)
	}
	if len(partIDs) > p.limits.MaxDocumentUnits {
		return Result{}, fmt.Errorf("%w: PPTX slide count %d exceeds limit %d", ErrResourceLimit, len(partIDs), p.limits.MaxDocumentUnits)
	}

	var elements []knowledge.DocumentElement
	for index, relationshipID := range partIDs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		partName := relationships[relationshipID]
		if partName == "" {
			return Result{}, fmt.Errorf("%w: PPTX slide relationship %q is missing", ErrInvalidContent, relationshipID)
		}
		slideXML, err := archive.readXML(partName)
		if err != nil {
			return Result{}, err
		}
		pageNumber := index + 1
		slideElements, err := parseFlowElements(ctx, slideXML, budget, flowOptions{
			PageNumber: &pageNumber, SectionPath: []string{fmt.Sprintf("Slide %d", pageNumber)},
		})
		if err != nil {
			return Result{}, err
		}
		if len(elements)+len(slideElements) > maxParserElements {
			return Result{}, fmt.Errorf("%w: element count exceeds limit %d", ErrResourceLimit, maxParserElements)
		}
		for _, element := range slideElements {
			element.Index = len(elements)
			elements = append(elements, element)
		}
	}
	if len(elements) == 0 {
		return Result{}, fmt.Errorf("%w: PPTX contains no extractable text or tables", ErrInvalidContent)
	}
	metadata, err := json.Marshal(map[string]any{
		"slideCount": len(partIDs), "elementCount": len(elements), "extractionMode": "ooxml_presentation",
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ParserVersion: PPTXParserVersion, Elements: elements, Metadata: metadata}, nil
}

func parsePresentationSlideIDs(ctx context.Context, content []byte) ([]string, error) {
	decoder := newStrictXMLDecoder(content)
	var result []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode PPTX presentation: %v", ErrInvalidContent, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sldId" {
			continue
		}
		id := xmlRelationshipID(start.Attr)
		if id == "" {
			return nil, fmt.Errorf("%w: PPTX slide has no relationship id", ErrInvalidContent)
		}
		result = append(result, id)
	}
}
