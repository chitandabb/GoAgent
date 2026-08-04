// Package knowledgeparser converts bounded source bytes into typed document
// elements. Parsers do not write databases, object storage, or invoke tools.
package knowledgeparser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"

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
	Metadata      json.RawMessage
}

func (r Result) Validate() error {
	if strings.TrimSpace(r.ParserVersion) == "" || r.ParserVersion != strings.TrimSpace(r.ParserVersion) ||
		len(r.ParserVersion) > 128 {
		return errors.New("knowledge parser version is invalid")
	}
	if len(r.Elements) == 0 || len(r.Elements) > 10000 {
		return errors.New("knowledge parser elements are required and bounded")
	}
	for _, element := range r.Elements {
		if err := element.Validate(); err != nil {
			return err
		}
	}
	var metadata map[string]any
	if len(r.Metadata) == 0 || json.Unmarshal(r.Metadata, &metadata) != nil || metadata == nil {
		return errors.New("knowledge parser metadata must be a JSON object")
	}
	return nil
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
