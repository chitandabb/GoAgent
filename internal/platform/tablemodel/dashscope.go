// Package tablemodel adapts multimodal model endpoints to the strict table
// recovery contract used by the knowledge ingestion worker.
package tablemodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledgetable"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Generator interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

type Endpoint struct {
	Generator     Generator
	Provider      string
	Model         string
	Prompt        string
	PromptVersion string
}

type Processor struct {
	endpoint Endpoint
}

func NewProcessor(endpoint Endpoint) (*Processor, error) {
	if endpoint.Generator == nil || strings.TrimSpace(endpoint.Provider) == "" ||
		strings.TrimSpace(endpoint.Model) == "" || strings.TrimSpace(endpoint.Prompt) == "" ||
		strings.TrimSpace(endpoint.PromptVersion) == "" {
		return nil, errors.New("table model endpoint is incomplete")
	}
	return &Processor{endpoint: endpoint}, nil
}

func (p *Processor) Recover(ctx context.Context, request knowledgetable.Request) (knowledgetable.Result, error) {
	if p == nil || p.endpoint.Generator == nil {
		return knowledgetable.Result{}, knowledgetable.ErrUnavailable
	}
	if err := request.Validate(); err != nil {
		return knowledgetable.Result{}, err
	}
	message := buildMessage(p.endpoint.Prompt, request)
	response, err := p.endpoint.Generator.Generate(ctx, []*schema.Message{message})
	if err != nil {
		return knowledgetable.Result{}, fmt.Errorf("table model request: %w", err)
	}
	if response == nil {
		return knowledgetable.Result{}, errors.New("table model returned an empty response")
	}
	if response.ResponseMeta != nil && strings.EqualFold(strings.TrimSpace(response.ResponseMeta.FinishReason), "length") {
		return knowledgetable.Result{}, errors.New("table model response was truncated")
	}
	decoded, err := decodeResponse(response.Content)
	if err != nil {
		return knowledgetable.Result{}, err
	}
	result := knowledgetable.Result{
		Provider: p.endpoint.Provider, Model: p.endpoint.Model, PromptVersion: p.endpoint.PromptVersion,
		Markdown: strings.TrimSpace(decoded.Markdown), Confidence: decoded.Confidence,
		Cells:    make([]knowledgetable.Cell, 0, len(decoded.Cells)),
		Warnings: make([]string, 0, len(decoded.Warnings)), Usage: responseUsage(response),
	}
	for _, cell := range decoded.Cells {
		result.Cells = append(result.Cells, knowledgetable.Cell{
			Row: cell.Row, Column: cell.Column, RowSpan: cell.RowSpan, ColumnSpan: cell.ColumnSpan,
			Text: strings.TrimSpace(cell.Text), Header: cell.Header,
		})
	}
	for _, warning := range decoded.Warnings {
		result.Warnings = append(result.Warnings, strings.TrimSpace(warning))
	}
	applyStructuralQualityGuard(&result)
	if err := result.Validate(); err != nil {
		return knowledgetable.Result{}, fmt.Errorf("validate table model result: %w", err)
	}
	return result, nil
}

func applyStructuralQualityGuard(result *knowledgetable.Result) {
	if result == nil {
		return
	}
	ambiguous := strings.Contains(strings.ToLower(result.Markdown), "<br")
	for _, cell := range result.Cells {
		if strings.ContainsAny(cell.Text, "\r\n") {
			ambiguous = true
			break
		}
	}
	if ambiguous {
		markPartialTableResult(result, "multiline_cell_structure_ambiguous",
			"multiline cell text may contain collapsed visible rows")
		return
	}
	// Warnings and confidence <= 0.8 are the model's only explicit signal that
	// row bands or spans are uncertain. Preserve the cells for inspection, but
	// never publish that result as complete.
	if len(result.Warnings) > 0 || result.Confidence <= 0.8 {
		markPartialTableResult(result, "table_structure_uncertain", "table model reported structural uncertainty")
	}
}

func markPartialTableResult(result *knowledgetable.Result, reason, warning string) {
	if result == nil {
		return
	}
	result.Partial = true
	result.Reason = reason
	if result.Confidence > 0.8 {
		result.Confidence = 0.8
	}
	for _, current := range result.Warnings {
		if current == warning {
			return
		}
	}
	if len(result.Warnings) < 32 {
		result.Warnings = append(result.Warnings, warning)
	}
}

func buildMessage(prompt string, request knowledgetable.Request) *schema.Message {
	locator := fmt.Sprintf(
		"\nSource path: %s\nPage: %d\nProcessing hint: %s",
		request.Asset.SourcePath, *request.Asset.PageNumber, request.Reason,
	)
	parts := []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText, Text: strings.TrimSpace(prompt) + locator,
	}, {
		Type: schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{
			MessagePartCommon: schema.MessagePartCommon{
				Base64Data: stringPointer(base64.StdEncoding.EncodeToString(request.Asset.Content)),
				MIMEType:   request.Asset.MediaType,
			},
			Detail: schema.ImageURLDetailHigh,
		},
	}}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

type tableResponse struct {
	Markdown   string         `json:"markdown"`
	Cells      []responseCell `json:"cells"`
	Confidence float64        `json:"confidence"`
	Warnings   []string       `json:"warnings"`
}

type responseCell struct {
	Row        int    `json:"row"`
	Column     int    `json:"column"`
	RowSpan    int    `json:"rowSpan"`
	ColumnSpan int    `json:"columnSpan"`
	Text       string `json:"text"`
	Header     bool   `json:"header"`
}

func decodeResponse(content string) (tableResponse, error) {
	normalized, err := normalizeJSON(content)
	if err != nil {
		return tableResponse{}, err
	}
	var result tableResponse
	decoder := json.NewDecoder(strings.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return tableResponse{}, fmt.Errorf("decode table model JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return tableResponse{}, errors.New("decode table model JSON: trailing content")
	}
	return result, nil
}

func normalizeJSON(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	lineEnd := strings.IndexByte(trimmed, '\n')
	if lineEnd < 0 || !strings.EqualFold(strings.TrimSpace(trimmed[:lineEnd]), "```json") {
		return "", errors.New("decode table model JSON: unsupported Markdown fence")
	}
	remainder := strings.TrimSpace(trimmed[lineEnd+1:])
	closingLine := strings.LastIndexByte(remainder, '\n')
	if closingLine < 0 || strings.TrimSpace(remainder[closingLine+1:]) != "```" {
		return "", errors.New("decode table model JSON: malformed Markdown fence")
	}
	payload := strings.TrimSpace(remainder[:closingLine])
	if payload == "" || strings.Contains(payload, "```") {
		return "", errors.New("decode table model JSON: malformed Markdown fence")
	}
	return payload, nil
}

func responseUsage(response *schema.Message) *knowledgetable.Usage {
	if response == nil || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil {
		return nil
	}
	usage := response.ResponseMeta.Usage
	return &knowledgetable.Usage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens,
	}
}

func stringPointer(value string) *string { return &value }
