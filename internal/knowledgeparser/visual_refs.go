package knowledgeparser

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
)

type visualReference struct {
	RelationshipID string
	SourcePart     string
	PageNumber     *int
}

func (a *ooxmlArchive) visualReferences(ctx context.Context) (map[string][]visualReference, error) {
	names := make([]string, 0)
	for name := range a.files {
		if strings.HasSuffix(name, ".rels") && strings.Contains(name, "/_rels/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	pageBySlide, err := a.pptxSlidePages(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]visualReference)
	for _, relationshipPart := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourcePart, ok := relationshipSourcePart(relationshipPart)
		if !ok || a.files[sourcePart] == nil {
			continue
		}
		relationshipXML, err := a.readXML(relationshipPart)
		if err != nil {
			return nil, err
		}
		relationships, err := parseOOXMLRelationships(ctx, relationshipXML, path.Dir(sourcePart))
		if err != nil {
			return nil, err
		}
		sourceXML, err := a.readXML(sourcePart)
		if err != nil {
			return nil, err
		}
		usedIDs, err := visualRelationshipIDs(ctx, sourceXML, relationships)
		if err != nil {
			return nil, err
		}
		for _, relationshipID := range usedIDs {
			mediaPath := relationships[relationshipID]
			if officeVisualMediaType(mediaPath) == "" {
				continue
			}
			var pageNumber *int
			if page := pageBySlide[sourcePart]; page > 0 {
				pageNumber = &page
			}
			result[mediaPath] = append(result[mediaPath], visualReference{
				RelationshipID: relationshipID, SourcePart: sourcePart, PageNumber: pageNumber,
			})
		}
	}
	for mediaPath := range result {
		sort.SliceStable(result[mediaPath], func(i, j int) bool {
			left, right := result[mediaPath][i], result[mediaPath][j]
			if left.PageNumber != nil && right.PageNumber != nil && *left.PageNumber != *right.PageNumber {
				return *left.PageNumber < *right.PageNumber
			}
			if left.PageNumber != nil && right.PageNumber == nil {
				return true
			}
			if left.PageNumber == nil && right.PageNumber != nil {
				return false
			}
			if left.SourcePart != right.SourcePart {
				return left.SourcePart < right.SourcePart
			}
			return left.RelationshipID < right.RelationshipID
		})
	}
	return result, nil
}

func relationshipSourcePart(relationshipPart string) (string, bool) {
	directory := path.Dir(relationshipPart)
	if path.Base(directory) != "_rels" || !strings.HasSuffix(relationshipPart, ".rels") {
		return "", false
	}
	baseDir := path.Dir(directory)
	fileName := strings.TrimSuffix(path.Base(relationshipPart), ".rels")
	if fileName == "" || fileName == "." || baseDir == "." {
		return "", false
	}
	return path.Join(baseDir, fileName), true
}

func visualRelationshipIDs(ctx context.Context, content []byte, relationships map[string]string) ([]string, error) {
	decoder := newStrictXMLDecoder(content)
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, fmtInvalidVisualXML(err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attribute := range start.Attr {
			if attribute.Name.Local != "embed" && attribute.Name.Local != "link" {
				continue
			}
			if _, ok := relationships[attribute.Value]; !ok {
				continue
			}
			if _, exists := seen[attribute.Value]; !exists {
				seen[attribute.Value] = struct{}{}
				result = append(result, attribute.Value)
			}
		}
	}
}

func (a *ooxmlArchive) pptxSlidePages(ctx context.Context) (map[string]int, error) {
	result := make(map[string]int)
	_, hasPresentation := a.files["ppt/presentation.xml"]
	_, hasRelationships := a.files["ppt/_rels/presentation.xml.rels"]
	if !hasPresentation && !hasRelationships {
		return result, nil
	}
	if !hasPresentation || !hasRelationships {
		return nil, errors.Join(ErrInvalidContent, errors.New("PPTX presentation relationship parts are incomplete"))
	}
	presentation, err := a.readXML("ppt/presentation.xml")
	if err != nil {
		return nil, err
	}
	relationshipXML, err := a.readXML("ppt/_rels/presentation.xml.rels")
	if err != nil {
		return nil, err
	}
	relationships, err := parseOOXMLRelationships(ctx, relationshipXML, "ppt")
	if err != nil {
		return nil, err
	}
	ids, err := parsePresentationSlideIDs(ctx, presentation)
	if err != nil {
		return nil, err
	}
	for index, relationshipID := range ids {
		if slidePath := relationships[relationshipID]; slidePath != "" {
			result[slidePath] = index + 1
		}
	}
	return result, nil
}

func fmtInvalidVisualXML(err error) error {
	return errors.Join(ErrInvalidContent, errors.New("decode Office visual relationship source XML: "+err.Error()))
}
