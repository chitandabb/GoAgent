package visualmodel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestProcessorBuildsStructuredOCRAndVLMElements(t *testing.T) {
	model := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		message := schema.AssistantMessage(`{"ocrText":"E42 timeout","description":"连接池告警"}`, nil)
		message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		}}
		return message, nil
	})
	processor, err := NewProcessor(nil, &Endpoint{
		Generator: model, Provider: "dashscope", Model: "vision-v1",
		Prompt: "prompt", PromptVersion: "vision-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage, SourcePath: "word/media/image1.png",
			SourcePart: "word/document.xml", RelationshipID: "rIdImage",
			MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Elements) != 2 || result.Elements[0].ElementType != knowledge.ElementOCRText ||
		result.Elements[1].ElementType != knowledge.ElementImageDescription {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 120 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.Elements[0].Metadata, &metadata); err != nil ||
		metadata["sourcePart"] != "word/document.xml" || metadata["relationshipId"] != "rIdImage" {
		t.Fatalf("visual metadata = %v, err = %v", metadata, err)
	}
}

func TestDecodeVisualResponseRejectsUnknownFields(t *testing.T) {
	if _, err := decodeVisualResponse(`{"ocrText":"x","description":"","unexpected":true}`); err == nil {
		t.Fatal("decodeVisualResponse accepted unknown field")
	}
}

func TestDecodeVisualResponseAcceptsSingleJSONFence(t *testing.T) {
	result, err := decodeVisualResponse("```json\r\n{\"ocrText\":\"E42\",\"description\":\"\"}\r\n```")
	if err != nil {
		t.Fatal(err)
	}
	if result.OCRText != "E42" || result.Description != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeVisualResponseRejectsFenceWithSurroundingText(t *testing.T) {
	inputs := []string{
		"result:\n```json\n{\"ocrText\":\"x\",\"description\":\"\"}\n```",
		"```json\n{\"ocrText\":\"x\",\"description\":\"\"}\n```\nresult",
		"```markdown\n{\"ocrText\":\"x\",\"description\":\"\"}\n```",
	}
	for _, input := range inputs {
		if _, err := decodeVisualResponse(input); err == nil {
			t.Fatalf("decodeVisualResponse accepted %q", input)
		}
	}
}

func TestProcessorRejectsPDFFileInputBeforeCallingGenerator(t *testing.T) {
	called := false
	generator := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		called = true
		return nil, nil
	})
	processor, err := NewProcessor(&Endpoint{
		Generator: generator, Provider: "dashscope", Model: "ocr-v1",
		Prompt: "prompt", PromptVersion: "ocr-v1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCR,
		Source: knowledgeenrichment.Source{
			MediaType: "application/pdf", OriginalName: "scan.pdf", Content: []byte("%PDF"),
		},
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetDocumentPage, PageNumber: &page,
			SourcePath: "pages/1", MediaType: "application/pdf", SizeBytes: 4,
			SHA256: "9e592300c2c56d07b07a69f4e288bff28d877050bc3968cb2eb28531b4636940",
		},
	})
	if !errors.Is(err, knowledgeenrichment.ErrUnsupportedInput) {
		t.Fatalf("Process error = %v", err)
	}
	if called {
		t.Fatal("generator was called for unsupported PDF input")
	}
}

type generatorFunc func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)

func (f generatorFunc) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return f(ctx, messages, options...)
}
