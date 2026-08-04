package knowledgeparser

import (
	"context"
	"encoding/json"
	"fmt"
)

func (p OOXMLParser) parseDOCX(ctx context.Context, archive *ooxmlArchive, budget *runeBudget) (Result, error) {
	content, err := archive.readXML("word/document.xml")
	if err != nil {
		return Result{}, err
	}
	elements, err := parseFlowElements(ctx, content, budget, flowOptions{CaptureHeading: true})
	if err != nil {
		return Result{}, err
	}
	if len(elements) == 0 {
		return Result{}, fmt.Errorf("%w: DOCX contains no extractable text or tables", ErrInvalidContent)
	}
	metadata, err := json.Marshal(map[string]any{
		"elementCount": len(elements), "extractionMode": "ooxml_document",
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ParserVersion: DOCXParserVersion, Elements: elements, Metadata: metadata}, nil
}
