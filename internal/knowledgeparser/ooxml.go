package knowledgeparser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	DOCXMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	XLSXMediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	PPTXMediaType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

	DOCXParserVersion = "docx-elements-v1"
	XLSXParserVersion = "xlsx-elements-v1"
	PPTXParserVersion = "pptx-elements-v1"
)

type OOXMLParser struct {
	limits Limits
}

func NewOOXMLParser(limits Limits) (OOXMLParser, error) {
	if err := limits.Validate(); err != nil {
		return OOXMLParser{}, err
	}
	return OOXMLParser{limits: limits}, nil
}

func (OOXMLParser) Supports(mediaType string) bool {
	return mediaType == DOCXMediaType || mediaType == XLSXMediaType || mediaType == PPTXMediaType
}

func (p OOXMLParser) Parse(ctx context.Context, input Input) (Result, error) {
	if err := p.limits.Validate(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	archive, err := newOOXMLArchive(input.Content, p.limits)
	if err != nil {
		return Result{}, err
	}
	if err := archive.validateContentTypes(ctx); err != nil {
		return Result{}, err
	}
	budget := newRuneBudget(p.limits.MaxExtractedRunes)
	var result Result
	switch input.MediaType {
	case DOCXMediaType:
		result, err = p.parseDOCX(ctx, archive, budget)
	case XLSXMediaType:
		result, err = p.parseXLSX(ctx, archive, budget)
	case PPTXMediaType:
		result, err = p.parsePPTX(ctx, archive, budget)
	default:
		return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, input.MediaType)
	}
	if err != nil {
		return Result{}, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.Metadata, &metadata); err != nil {
		return Result{}, err
	}
	metadata["mediaType"] = input.MediaType
	metadata["archiveEntries"] = len(archive.files)
	metadata["expandedBytes"] = archive.expandedBytes
	metadata["visualAssetCount"] = archive.visualAssetCount
	metadata["visualEnrichmentRequired"] = archive.visualAssetCount > 0
	result.Metadata, err = json.Marshal(metadata)
	return result, err
}

type ooxmlArchive struct {
	files            map[string]*zip.File
	limits           Limits
	expandedBytes    int64
	visualAssetCount int
}

func newOOXMLArchive(content []byte, limits Limits) (*ooxmlArchive, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("%w: Office package is not a valid ZIP archive", ErrInvalidContent)
	}
	if len(reader.File) == 0 {
		return nil, fmt.Errorf("%w: Office package is empty", ErrInvalidContent)
	}
	if len(reader.File) > limits.MaxArchiveEntries {
		return nil, fmt.Errorf("%w: archive entry count %d exceeds limit %d", ErrResourceLimit, len(reader.File), limits.MaxArchiveEntries)
	}
	result := &ooxmlArchive{files: make(map[string]*zip.File, len(reader.File)), limits: limits}
	for _, file := range reader.File {
		name := file.Name
		if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") ||
			path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("%w: Office package contains an unsafe entry name", ErrInvalidContent)
		}
		if file.Flags&0x1 != 0 {
			return nil, fmt.Errorf("%w: encrypted Office packages are not supported", ErrInvalidContent)
		}
		if _, exists := result.files[name]; exists {
			return nil, fmt.Errorf("%w: Office package contains duplicate entries", ErrInvalidContent)
		}
		if file.UncompressedSize64 > uint64(limits.MaxExpandedBytes) ||
			result.expandedBytes > limits.MaxExpandedBytes-int64(file.UncompressedSize64) {
			return nil, fmt.Errorf("%w: Office package expanded size exceeds configured limit", ErrResourceLimit)
		}
		result.expandedBytes += int64(file.UncompressedSize64)
		result.files[name] = file
		if !file.FileInfo().IsDir() && (strings.HasPrefix(name, "word/media/") ||
			strings.HasPrefix(name, "xl/media/") || strings.HasPrefix(name, "ppt/media/")) {
			result.visualAssetCount++
		}
	}
	return result, nil
}

func (a *ooxmlArchive) readXML(name string) ([]byte, error) {
	file := a.files[name]
	if file == nil {
		return nil, fmt.Errorf("%w: required Office part %q is missing", ErrInvalidContent, name)
	}
	if file.UncompressedSize64 > uint64(a.limits.MaxXMLBytes) {
		return nil, fmt.Errorf("%w: Office XML part %q exceeds configured limit", ErrResourceLimit, name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open Office part %q: %v", ErrInvalidContent, name, err)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, a.limits.MaxXMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read Office part %q: %v", ErrInvalidContent, name, err)
	}
	if int64(len(content)) > a.limits.MaxXMLBytes {
		return nil, fmt.Errorf("%w: Office XML part %q exceeds configured limit", ErrResourceLimit, name)
	}
	return content, nil
}

func (a *ooxmlArchive) validateContentTypes(ctx context.Context) error {
	content, err := a.readXML("[Content_Types].xml")
	if err != nil {
		return err
	}
	decoder := newStrictXMLDecoder(content)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: Office content types document has no root element", ErrInvalidContent)
		}
		if err != nil {
			return fmt.Errorf("%w: decode Office content types: %v", ErrInvalidContent, err)
		}
		if start, ok := token.(xml.StartElement); ok {
			if start.Name.Local != "Types" {
				return fmt.Errorf("%w: Office content types root is invalid", ErrInvalidContent)
			}
			return nil
		}
	}
}

func newStrictXMLDecoder(content []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	return decoder
}

func xmlAttribute(attributes []xml.Attr, localName string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == localName {
			return attribute.Value
		}
	}
	return ""
}

func parseOOXMLRelationships(ctx context.Context, content []byte, baseDir string) (map[string]string, error) {
	decoder := newStrictXMLDecoder(content)
	result := make(map[string]string)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: decode Office relationships: %v", ErrInvalidContent, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" || strings.EqualFold(xmlAttribute(start.Attr, "TargetMode"), "External") {
			continue
		}
		id, target := strings.TrimSpace(xmlAttribute(start.Attr, "Id")), strings.TrimSpace(xmlAttribute(start.Attr, "Target"))
		if id == "" || target == "" || strings.Contains(target, "\\") || strings.Contains(target, "://") {
			return nil, fmt.Errorf("%w: Office relationship is invalid", ErrInvalidContent)
		}
		resolved := ""
		if strings.HasPrefix(target, "/") {
			resolved = path.Clean(strings.TrimPrefix(target, "/"))
		} else {
			resolved = path.Clean(path.Join(baseDir, target))
		}
		if resolved == baseDir || !strings.HasPrefix(resolved, strings.TrimSuffix(baseDir, "/")+"/") {
			return nil, fmt.Errorf("%w: Office relationship escapes its package root", ErrInvalidContent)
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("%w: duplicate Office relationship id", ErrInvalidContent)
		}
		result[id] = resolved
	}
}

func xmlRelationshipID(attributes []xml.Attr) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == "id" &&
			(strings.Contains(strings.ToLower(attribute.Name.Space), "relationships") ||
				strings.HasPrefix(attribute.Value, "rId")) {
			return attribute.Value
		}
	}
	return ""
}
