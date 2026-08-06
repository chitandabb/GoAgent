// Package knowledgeparser converts bounded source bytes into typed document
// elements. Parsers do not write databases, object storage, or invoke tools.
package knowledgeparser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"strings"
	"unicode"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

var (
	ErrUnsupportedMediaType = errors.New("knowledge parser media type is unsupported")
	ErrInvalidContent       = errors.New("knowledge parser content is invalid")
)

type Input struct {
	MediaType    string
	OriginalName string
	Content      []byte
}

func (i Input) Validate() error {
	if strings.TrimSpace(i.MediaType) == "" || len(i.Content) == 0 {
		return fmt.Errorf("%w: media type and content are required", ErrInvalidContent)
	}
	if len([]rune(i.OriginalName)) > 512 {
		return fmt.Errorf("%w: original name is too long", ErrInvalidContent)
	}
	return nil
}

type Result struct {
	ParserVersion string
	Elements      []knowledge.DocumentElement
	VisualAssets  []VisualAsset
	Pages         []PageObservation
	Metadata      json.RawMessage
}

func (r Result) Validate() error {
	if strings.TrimSpace(r.ParserVersion) == "" || r.ParserVersion != strings.TrimSpace(r.ParserVersion) ||
		len(r.ParserVersion) > 128 {
		return errors.New("knowledge parser version is invalid")
	}
	if len(r.Elements) == 0 && len(r.VisualAssets) == 0 {
		return errors.New("knowledge parser elements or visual assets are required")
	}
	if len(r.Elements) > 10000 || len(r.VisualAssets) > 10000 {
		return errors.New("knowledge parser results are not bounded")
	}
	for _, element := range r.Elements {
		if err := element.Validate(); err != nil {
			return err
		}
	}
	for index, asset := range r.VisualAssets {
		if asset.Index != index {
			return errors.New("knowledge parser visual asset indexes must be contiguous")
		}
		if err := asset.Validate(); err != nil {
			return err
		}
	}
	lastPageNumber := 0
	for _, page := range r.Pages {
		if page.PageNumber <= lastPageNumber {
			return errors.New("knowledge parser page observations must be strictly ordered")
		}
		if err := page.Validate(); err != nil {
			return err
		}
		lastPageNumber = page.PageNumber
	}
	var metadata map[string]any
	if len(r.Metadata) == 0 || json.Unmarshal(r.Metadata, &metadata) != nil || metadata == nil {
		return errors.New("knowledge parser metadata must be a JSON object")
	}
	return nil
}

type PageObservation struct {
	PageNumber            int
	NativeTextRunes       int
	NonWhitespaceRunes    int
	PrintableRatio        float64
	ExtractionComplete    bool
	VisualCandidateCount  int
	VisualCandidatesKnown bool
}

func (p PageObservation) Validate() error {
	if p.PageNumber < 1 || p.NativeTextRunes < 0 || p.NonWhitespaceRunes < 0 ||
		p.NonWhitespaceRunes > p.NativeTextRunes || math.IsNaN(p.PrintableRatio) ||
		math.IsInf(p.PrintableRatio, 0) || p.PrintableRatio < 0 || p.PrintableRatio > 1 ||
		p.VisualCandidateCount < 0 || (p.VisualCandidateCount > 0 && !p.VisualCandidatesKnown) {
		return errors.New("knowledge parser page observation is invalid")
	}
	return nil
}

func observePageText(pageNumber int, text string, extractionComplete bool) PageObservation {
	runes := []rune(text)
	nonWhitespace := 0
	printable := 0
	for _, value := range runes {
		if !unicode.IsSpace(value) {
			nonWhitespace++
		}
		if unicode.IsPrint(value) || unicode.IsSpace(value) {
			printable++
		}
	}
	ratio := 0.0
	if len(runes) > 0 {
		ratio = float64(printable) / float64(len(runes))
	}
	return PageObservation{
		PageNumber: pageNumber, NativeTextRunes: len(runes),
		NonWhitespaceRunes: nonWhitespace, PrintableRatio: ratio,
		ExtractionComplete: extractionComplete,
	}
}

type Parser interface {
	Supports(mediaType string) bool
	Parse(context.Context, Input) (Result, error)
}

type Router struct {
	parsers []Parser
}

func NewRouter(parsers ...Parser) (*Router, error) {
	if len(parsers) == 0 {
		return nil, errors.New("knowledge parser router requires at least one parser")
	}
	for _, parser := range parsers {
		if parser == nil {
			return nil, errors.New("knowledge parser router contains a nil parser")
		}
	}
	return &Router{parsers: append([]Parser(nil), parsers...)}, nil
}

func (r *Router) Parse(ctx context.Context, input Input) (Result, error) {
	if r == nil {
		return Result{}, errors.New("knowledge parser router is unavailable")
	}
	if err := input.Validate(); err != nil {
		return Result{}, err
	}
	mediaType, err := canonicalMediaType(input.MediaType)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidContent, err)
	}
	input.MediaType = mediaType
	for _, parser := range r.parsers {
		if !parser.Supports(mediaType) {
			continue
		}
		result, err := parser.Parse(ctx, input)
		if err != nil {
			return Result{}, err
		}
		if err := result.Validate(); err != nil {
			return Result{}, fmt.Errorf("%w: parser result: %v", ErrInvalidContent, err)
		}
		return result, nil
	}
	return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, mediaType)
}

func canonicalMediaType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || mediaType == "" {
		return "", errors.New("media type is malformed")
	}
	return strings.ToLower(mediaType), nil
}
