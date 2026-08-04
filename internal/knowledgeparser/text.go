package knowledgeparser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

const (
	PlainTextParserVersion = "plain-text-elements-v1"
	MarkdownParserVersion  = "markdown-elements-v1"
)

type TextParser struct{}

func (TextParser) Supports(mediaType string) bool {
	return mediaType == "text/plain" || mediaType == "text/markdown"
}

func (TextParser) Parse(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !utf8.Valid(input.Content) {
		return Result{}, fmt.Errorf("%w: text is not valid UTF-8", ErrInvalidContent)
	}
	content := bytes.TrimPrefix(input.Content, []byte{0xef, 0xbb, 0xbf})
	if bytes.IndexByte(content, 0) >= 0 {
		return Result{}, fmt.Errorf("%w: text contains NUL bytes", ErrInvalidContent)
	}

	var (
		elements []knowledge.DocumentElement
		version  string
		err      error
	)
	switch input.MediaType {
	case "text/plain":
		version = PlainTextParserVersion
		elements, err = knowledge.ParsePlainTextElements(string(content))
	case "text/markdown":
		version = MarkdownParserVersion
		elements, err = knowledge.ParseMarkdownElements(string(content))
	default:
		return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, input.MediaType)
	}
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidContent, err)
	}
	metadata, err := json.Marshal(map[string]any{
		"charset": "utf-8", "elementCount": len(elements), "mediaType": input.MediaType,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ParserVersion: version, Elements: elements, Metadata: metadata}, nil
}
