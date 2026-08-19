package visualmodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Generator interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

type Endpoint struct {
	Generator      Generator
	Provider       string
	Model          string
	Prompt         string
	PromptVersion  string
	ResponseFormat string
}

func NewOpenAICompatibleModel(ctx context.Context, cfg config.MultimodalModelConfig, owner string) (Generator, error) {
	if err := cfg.Validate(owner); err != nil {
		return nil, err
	}
	apiKey, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	maxOutputTokens := cfg.MaxOutputTokens
	temperature := float32(0)
	chatModelConfig := &openai.ChatModelConfig{
		APIKey:      apiKey,
		BaseURL:     strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		Model:       strings.TrimSpace(cfg.Model),
		Timeout:     time.Duration(cfg.TimeoutMillis) * time.Millisecond,
		MaxTokens:   &maxOutputTokens,
		Temperature: &temperature,
	}
	if effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort)); effort != "" {
		chatModelConfig.ReasoningEffort = openai.ReasoningEffortLevel(effort)
	}
	if strings.EqualFold(strings.TrimSpace(cfg.ResponseFormat), "json_object") {
		chatModelConfig.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		}
	}
	chatModel, err := openai.NewChatModel(ctx, chatModelConfig)
	if err != nil {
		return nil, fmt.Errorf("create %s model: %w", owner, err)
	}
	return chatModel, nil
}

type Processor struct {
	ocr    *Endpoint
	vision *Endpoint
}

type OCRResult struct {
	Text  string
	Usage *knowledgeenrichment.ProviderUsage
}

// ExtractOCR exposes the bounded OCR path for provider evaluation without
// constructing ingestion-only visual assets.
func (p *Processor) ExtractOCR(ctx context.Context, sourcePath, mediaType string, content []byte) (OCRResult, error) {
	if p == nil || p.ocr == nil {
		return OCRResult{}, knowledgeenrichment.ErrUnavailable
	}
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath == "" ||
		(mediaType != "image/png" && mediaType != "image/jpeg") || len(content) == 0 {
		return OCRResult{}, errors.New("OCR evaluation input is invalid")
	}
	message := buildImageMessageWithDetail(
		p.ocr.Prompt,
		fmt.Sprintf("\nSource path: %s\nProcessing hint: quality_evaluation", sourcePath),
		mediaType,
		content,
		schema.ImageURLDetailHigh,
	)
	response, err := p.ocr.Generator.Generate(ctx, []*schema.Message{message})
	if err != nil {
		return OCRResult{}, fmt.Errorf("visual model request: %w", err)
	}
	if response == nil {
		return OCRResult{}, providerFailure(
			p.ocr, nil, knowledgeenrichment.ProviderFailureEmptyResponse,
			errors.New("visual model returned an empty response"),
		)
	}
	if response.ResponseMeta != nil && strings.EqualFold(strings.TrimSpace(response.ResponseMeta.FinishReason), "length") {
		return OCRResult{}, providerFailure(
			p.ocr, response, knowledgeenrichment.ProviderFailureOutputTruncated,
			errors.New("visual model response was truncated"),
		)
	}
	decoded, err := decodeEndpointResponse(p.ocr, response.Content)
	if err != nil {
		return OCRResult{}, providerFailure(p.ocr, response, knowledgeenrichment.ProviderFailureInvalidOutput, err)
	}
	if decoded.OCRText == "" {
		return OCRResult{}, providerFailure(
			p.ocr, response, knowledgeenrichment.ProviderFailureNoUsableContent,
			errors.New("visual model returned no usable OCR text"),
		)
	}
	return OCRResult{Text: decoded.OCRText, Usage: responseUsage(response)}, nil
}

func NewProcessor(ocr, vision *Endpoint) (*Processor, error) {
	if ocr == nil && vision == nil {
		return nil, errors.New("visual model processor requires an OCR or vision endpoint")
	}
	for _, endpoint := range []*Endpoint{ocr, vision} {
		if endpoint == nil {
			continue
		}
		if endpoint.Generator == nil || strings.TrimSpace(endpoint.Provider) == "" ||
			strings.TrimSpace(endpoint.Model) == "" || strings.TrimSpace(endpoint.Prompt) == "" ||
			strings.TrimSpace(endpoint.PromptVersion) == "" {
			return nil, errors.New("visual model endpoint is incomplete")
		}
		if !supportedEndpointResponseFormat(endpoint.ResponseFormat) {
			return nil, errors.New("visual model endpoint response format is unsupported")
		}
	}
	if vision != nil && strings.EqualFold(strings.TrimSpace(vision.ResponseFormat), "text") {
		return nil, errors.New("visual semantics endpoint must use structured JSON output")
	}
	return &Processor{ocr: ocr, vision: vision}, nil
}

func (p *Processor) Process(ctx context.Context, request knowledgeenrichment.Request) (knowledgeenrichment.ProviderResult, error) {
	if p == nil {
		return knowledgeenrichment.ProviderResult{}, knowledgeenrichment.ErrUnavailable
	}
	if request.Asset.Kind == knowledgeparser.VisualAssetDocumentPage {
		return knowledgeenrichment.ProviderResult{}, errors.Join(
			knowledgeenrichment.ErrUnsupportedInput,
			errors.New("Eino OpenAI chat model does not encode PDF file_url input"),
		)
	}
	endpoint, partial, reason, err := p.endpointFor(request)
	if err != nil {
		return knowledgeenrichment.ProviderResult{}, err
	}
	message := buildMessage(endpoint.Prompt, request)
	response, generateErr := endpoint.Generator.Generate(ctx, []*schema.Message{message})
	if generateErr != nil {
		return knowledgeenrichment.ProviderResult{}, fmt.Errorf("visual model request: %w", generateErr)
	}
	if response == nil {
		return knowledgeenrichment.ProviderResult{}, providerFailure(
			endpoint, nil, knowledgeenrichment.ProviderFailureEmptyResponse,
			errors.New("visual model returned an empty response"),
		)
	}
	if response.ResponseMeta != nil && strings.EqualFold(strings.TrimSpace(response.ResponseMeta.FinishReason), "length") {
		return knowledgeenrichment.ProviderResult{}, providerFailure(
			endpoint, response, knowledgeenrichment.ProviderFailureOutputTruncated,
			errors.New("visual model response was truncated"),
		)
	}
	decoded, decodeErr := decodeEndpointResponse(endpoint, response.Content)
	if decodeErr != nil {
		return knowledgeenrichment.ProviderResult{}, providerFailure(
			endpoint, response, knowledgeenrichment.ProviderFailureInvalidOutput, decodeErr,
		)
	}
	elements := visualOutputElements(request, endpoint, decoded)
	if len(elements) == 0 {
		return knowledgeenrichment.ProviderResult{}, providerFailure(
			endpoint, response, knowledgeenrichment.ProviderFailureNoUsableContent,
			knowledgeenrichment.ErrNoUsableContent,
		)
	}
	return knowledgeenrichment.ProviderResult{
		Provider: endpoint.Provider, Model: endpoint.Model, Elements: elements,
		Partial: partial, Reason: reason, Usage: responseUsage(response),
	}, nil
}

func supportedEndpointResponseFormat(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "json_object", "text":
		return true
	default:
		return false
	}
}

func responseUsage(response *schema.Message) *knowledgeenrichment.ProviderUsage {
	if response == nil || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil {
		return nil
	}
	usage := response.ResponseMeta.Usage
	return &knowledgeenrichment.ProviderUsage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens,
	}
}

func providerFailure(
	endpoint *Endpoint,
	response *schema.Message,
	reason string,
	cause error,
) error {
	if endpoint == nil {
		return cause
	}
	return knowledgeenrichment.NewProviderFailure(
		endpoint.Provider, endpoint.Model, reason, responseUsage(response), cause,
	)
}

func visualOutputElements(
	request knowledgeenrichment.Request,
	endpoint *Endpoint,
	decoded visualResponse,
) []knowledge.DocumentElement {
	elements := make([]knowledge.DocumentElement, 0, 1)
	if decoded.OCRText != "" && request.Route == knowledgeenrichment.RouteOCR {
		elements = append(elements, knowledge.DocumentElement{
			ElementType: knowledge.ElementOCRText, ContentText: decoded.OCRText,
			Metadata: visualMetadata(request, endpoint, "ocr"),
		})
	}
	if decoded.Description != "" && request.Route == knowledgeenrichment.RouteOCRVLM {
		elements = append(elements, knowledge.DocumentElement{
			ElementType: knowledge.ElementImageDescription, ContentText: decoded.Description,
			Metadata: visualMetadata(request, endpoint, "vlm"),
		})
	}
	return elements
}

func (p *Processor) endpointFor(request knowledgeenrichment.Request) (*Endpoint, bool, string, error) {
	switch request.Route {
	case knowledgeenrichment.RouteOCR:
		if p.ocr == nil {
			return nil, false, "", knowledgeenrichment.ErrUnavailable
		}
		return p.ocr, false, "", nil
	case knowledgeenrichment.RouteOCRVLM:
		if p.vision != nil {
			return p.vision, false, "", nil
		}
		if p.ocr != nil && !strings.EqualFold(strings.TrimSpace(p.ocr.ResponseFormat), "text") {
			return p.ocr, true, "vision_not_configured", nil
		}
		return nil, false, "", knowledgeenrichment.ErrUnavailable
	default:
		return nil, false, "", errors.New("visual model route is unsupported")
	}
}

func buildMessage(prompt string, request knowledgeenrichment.Request) *schema.Message {
	locator := fmt.Sprintf("\nSource path: %s\nProcessing hint: %s", request.Asset.SourcePath, request.Reason)
	if request.Asset.PageNumber != nil {
		locator = fmt.Sprintf(
			"\nSource path: %s\nPage: %d\nProcessing hint: %s",
			request.Asset.SourcePath, *request.Asset.PageNumber, request.Reason,
		)
	}
	detail := schema.ImageURLDetailLow
	if request.Route == knowledgeenrichment.RouteOCR {
		detail = schema.ImageURLDetailHigh
	}
	return buildImageMessageWithDetail(prompt, locator, request.Asset.MediaType, request.Asset.Content, detail)
}

func buildImageMessage(prompt, locator, mediaType string, content []byte) *schema.Message {
	return buildImageMessageWithDetail(prompt, locator, mediaType, content, schema.ImageURLDetailLow)
}

func buildImageMessageWithDetail(
	prompt, locator, mediaType string,
	content []byte,
	detail schema.ImageURLDetail,
) *schema.Message {
	parts := []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText, Text: strings.TrimSpace(prompt) + locator,
	}, {
		Type: schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{
			MessagePartCommon: schema.MessagePartCommon{
				Base64Data: stringPointer(base64.StdEncoding.EncodeToString(content)),
				MIMEType:   mediaType,
			},
			Detail: detail,
		},
	}}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

type visualResponse struct {
	OCRText     string `json:"ocrText"`
	Description string `json:"description"`
}

const maxVisualOutputRunes = 100_000

func decodeEndpointResponse(endpoint *Endpoint, content string) (visualResponse, error) {
	format := ""
	if endpoint != nil {
		format = strings.ToLower(strings.TrimSpace(endpoint.ResponseFormat))
	}
	switch format {
	case "", "json_object":
		return decodeVisualResponse(content)
	case "text":
		return decodePlainTextOCRResponse(content)
	default:
		return visualResponse{}, fmt.Errorf("visual model response format %q is unsupported", format)
	}
}

func decodePlainTextOCRResponse(content string) (visualResponse, error) {
	text := strings.TrimSpace(content)
	if err := validateVisualResponseText(text); err != nil {
		return visualResponse{}, err
	}
	return visualResponse{OCRText: text}, nil
}

func decodeVisualResponse(content string) (visualResponse, error) {
	normalized, err := normalizeVisualJSON(content)
	if err != nil {
		return visualResponse{}, err
	}
	var result visualResponse
	decoder := json.NewDecoder(strings.NewReader(normalized))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return visualResponse{}, fmt.Errorf("decode visual model JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return visualResponse{}, errors.New("decode visual model JSON: trailing content")
	}
	result.OCRText = strings.TrimSpace(result.OCRText)
	result.Description = strings.TrimSpace(result.Description)
	if err := validateVisualResponseText(result.OCRText); err != nil {
		return visualResponse{}, err
	}
	if err := validateVisualResponseText(result.Description); err != nil {
		return visualResponse{}, err
	}
	return result, nil
}

func validateVisualResponseText(text string) error {
	if strings.ContainsRune(text, 0) || len([]rune(text)) > maxVisualOutputRunes {
		return errors.New("visual model output text exceeds safety limits")
	}
	return nil
}

func normalizeVisualJSON(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	lineEnd := strings.IndexByte(trimmed, '\n')
	if lineEnd < 0 || !strings.EqualFold(strings.TrimSpace(trimmed[:lineEnd]), "```json") {
		return "", errors.New("decode visual model JSON: unsupported Markdown fence")
	}
	remainder := strings.TrimSpace(trimmed[lineEnd+1:])
	closingLine := strings.LastIndexByte(remainder, '\n')
	if closingLine < 0 || strings.TrimSpace(remainder[closingLine+1:]) != "```" {
		return "", errors.New("decode visual model JSON: malformed Markdown fence")
	}
	payload := strings.TrimSpace(remainder[:closingLine])
	if payload == "" || strings.Contains(payload, "```") {
		return "", errors.New("decode visual model JSON: malformed Markdown fence")
	}
	return payload, nil
}

func visualMetadata(request knowledgeenrichment.Request, endpoint *Endpoint, method string) json.RawMessage {
	content, _ := json.Marshal(map[string]any{
		"assetIndex": request.Asset.Index, "sourcePath": request.Asset.SourcePath,
		"sourcePart": request.Asset.SourcePart, "relationshipId": request.Asset.RelationshipID,
		"method": method, "provider": endpoint.Provider, "model": endpoint.Model,
		"promptVersion": endpoint.PromptVersion, "routeReason": request.Reason,
	})
	return content
}

func stringPointer(value string) *string { return &value }
