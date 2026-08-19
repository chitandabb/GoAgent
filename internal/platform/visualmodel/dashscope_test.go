package visualmodel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeenrichment"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestOpenAICompatibleModelUsesConfiguredReasoningAndJSONMode(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"ocrText\":\"ok\",\"description\":\"\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
		}`))
	}))
	defer server.Close()

	t.Setenv("MESGUARD_TEST_VISUAL_KEY", "test-key")
	generator, err := NewOpenAICompatibleModel(context.Background(), config.MultimodalModelConfig{
		Enabled: true, Provider: "custom-openai", BaseURL: server.URL + "/v1",
		APIKeyEnv: "MESGUARD_TEST_VISUAL_KEY", Model: "vision-v1", PromptFile: "test-prompt.md",
		PromptVersion: "vision-v1", ReasoningEffort: "low", ResponseFormat: "json_object",
		TimeoutMillis: 10_000, MaxOutputTokens: 8192,
	}, "models.vision")
	if err != nil {
		t.Fatal(err)
	}
	message, err := generator.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != `{"ocrText":"ok","description":""}` {
		t.Fatalf("message = %+v", message)
	}
	if requestBody["reasoning_effort"] != "low" || requestBody["max_tokens"] != float64(8192) {
		t.Fatalf("request body = %#v", requestBody)
	}
	format, ok := requestBody["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("response_format = %#v", requestBody["response_format"])
	}
}

func TestOpenAICompatibleModelOmitsJSONModeForPlainText(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"plain OCR text"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	t.Setenv("MESGUARD_TEST_VISUAL_KEY", "test-key")
	generator, err := NewOpenAICompatibleModel(context.Background(), config.MultimodalModelConfig{
		Enabled: true, Provider: "custom-openai", BaseURL: server.URL + "/v1",
		APIKeyEnv: "MESGUARD_TEST_VISUAL_KEY", Model: "ocr-v1", PromptFile: "test-prompt.md",
		PromptVersion: "ocr-v1", ResponseFormat: "text", TimeoutMillis: 10_000, MaxOutputTokens: 1024,
	}, "models.ocr")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := requestBody["response_format"]; ok {
		t.Fatalf("request must omit response_format for plain-text OCR: %#v", requestBody)
	}
}

func TestBuildImageMessageUsesLowDetail(t *testing.T) {
	message := buildImageMessage("prompt", "locator", "image/png", []byte("image"))
	if len(message.UserInputMultiContent) != 2 || message.UserInputMultiContent[1].Image == nil ||
		message.UserInputMultiContent[1].Image.Detail != schema.ImageURLDetailLow {
		t.Fatalf("message = %+v", message)
	}
}

func TestBuildMessageUsesHighDetailForOCR(t *testing.T) {
	message := buildMessage("prompt", knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCR,
		Asset: knowledgeparser.VisualAsset{SourcePath: "pages/1", MediaType: "image/png", Content: []byte("image")},
	})
	if message.UserInputMultiContent[1].Image.Detail != schema.ImageURLDetailHigh {
		t.Fatalf("detail = %q", message.UserInputMultiContent[1].Image.Detail)
	}
}

func TestExtractOCRUsesHighDetail(t *testing.T) {
	var detail schema.ImageURLDetail
	generator := generatorFunc(func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		detail = messages[0].UserInputMultiContent[1].Image.Detail
		return schema.AssistantMessage(`{"ocrText":"E42","description":""}`, nil), nil
	})
	processor, err := NewProcessor(&Endpoint{
		Generator: generator, Provider: "stepfun", Model: "step-3.7-flash",
		Prompt: "prompt", PromptVersion: "ocr-v1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.ExtractOCR(context.Background(), "page.png", "image/png", []byte("image"))
	if err != nil || result.Text != "E42" || detail != schema.ImageURLDetailHigh {
		t.Fatalf("result=%+v err=%v detail=%q", result, err, detail)
	}
}

func TestExtractOCRAcceptsPlainTextResponse(t *testing.T) {
	var detail schema.ImageURLDetail
	generator := generatorFunc(func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		detail = messages[0].UserInputMultiContent[1].Image.Detail
		return schema.AssistantMessage("E42\nF01", nil), nil
	})
	processor, err := NewProcessor(&Endpoint{
		Generator: generator, Provider: "dashscope", Model: "qwen-vl-ocr-latest",
		Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.ExtractOCR(context.Background(), "page.png", "image/png", []byte("image"))
	if err != nil || result.Text != "E42\nF01" || detail != schema.ImageURLDetailHigh {
		t.Fatalf("result=%+v err=%v detail=%q", result, err, detail)
	}
}

func TestProcessorRejectsTruncatedPlainTextOCR(t *testing.T) {
	processor, err := NewProcessor(&Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			message := schema.AssistantMessage("truncated", nil)
			message.ResponseMeta = &schema.ResponseMeta{
				FinishReason: "length",
				Usage:        &schema.TokenUsage{PromptTokens: 1200, CompletionTokens: 4096, TotalTokens: 5296},
			}
			return message, nil
		}),
		Provider: "dashscope", Model: "qwen-vl-ocr-latest", Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.ExtractOCR(context.Background(), "page.png", "image/png", []byte("image"))
	var failure *knowledgeenrichment.ProviderFailure
	if err == nil || !strings.Contains(err.Error(), "truncated") || !errors.As(err, &failure) ||
		failure.Reason != knowledgeenrichment.ProviderFailureOutputTruncated || failure.Usage == nil ||
		failure.Usage.TotalTokens != 5296 {
		t.Fatalf("ExtractOCR error = %v failure = %+v", err, failure)
	}
}

func TestProcessorPreservesUsageForTruncatedOCRRoute(t *testing.T) {
	processor, err := NewProcessor(&Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			message := schema.AssistantMessage("truncated", nil)
			message.ResponseMeta = &schema.ResponseMeta{
				FinishReason: "length",
				Usage:        &schema.TokenUsage{PromptTokens: 1200, CompletionTokens: 4096, TotalTokens: 5296},
			}
			return message, nil
		}),
		Provider: "dashscope", Model: "qwen-vl-ocr-latest", Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCR,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetLayoutRegion, PageNumber: &page,
			SourcePath: "pages/1/layout-regions/0", MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	var failure *knowledgeenrichment.ProviderFailure
	if err == nil || !errors.As(err, &failure) || failure.Provider != "dashscope" ||
		failure.Model != "qwen-vl-ocr-latest" || failure.Reason != knowledgeenrichment.ProviderFailureOutputTruncated ||
		failure.Usage == nil || failure.Usage.TotalTokens != 5296 {
		t.Fatalf("Process error = %v failure = %+v", err, failure)
	}
}

func TestProcessorRejectsPlainTextOCRFallbackForVision(t *testing.T) {
	processor, err := NewProcessor(&Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("text", nil), nil
		}),
		Provider: "dashscope", Model: "qwen-vl-ocr-latest", Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage, SourcePath: "word/media/image1.png",
			SourcePart: "word/document.xml", RelationshipID: "rIdImage", MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if !errors.Is(err, knowledgeenrichment.ErrUnavailable) {
		t.Fatalf("Process error = %v", err)
	}
}

func TestProcessorRejectsPDFBeforePlainTextOCRRouteSelection(t *testing.T) {
	processor, err := NewProcessor(&Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			t.Fatal("generator must not receive raw PDF input")
			return nil, nil
		}),
		Provider: "dashscope", Model: "qwen-vl-ocr-latest", Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetDocumentPage, PageNumber: &page, SourcePath: "pages/1",
			MediaType: "application/pdf", Content: []byte("%PDF"), SizeBytes: 4,
		},
	})
	if !errors.Is(err, knowledgeenrichment.ErrUnsupportedInput) {
		t.Fatalf("Process error = %v", err)
	}
}

func TestProcessorRejectsPlainTextVisionEndpoint(t *testing.T) {
	_, err := NewProcessor(nil, &Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("text", nil), nil
		}),
		Provider: "dashscope", Model: "vision-v1", Prompt: "prompt", PromptVersion: "vision-v1", ResponseFormat: "text",
	})
	if err == nil {
		t.Fatal("NewProcessor accepted a plain-text vision endpoint")
	}
}

func TestProcessorPublishesPlainTextOCR(t *testing.T) {
	processor, err := NewProcessor(&Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("E42\nF01", nil), nil
		}),
		Provider: "dashscope", Model: "qwen-vl-ocr-latest", Prompt: "prompt", PromptVersion: "ocr-v2", ResponseFormat: "text",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCR,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetLayoutRegion, SourcePath: "pages/1/layout-regions/1",
			MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Elements) != 1 || result.Elements[0].ElementType != knowledge.ElementOCRText ||
		result.Elements[0].ContentText != "E42\nF01" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProcessorPublishesOnlyDescriptionForVLM(t *testing.T) {
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
	if len(result.Elements) != 1 || result.Elements[0].ElementType != knowledge.ElementImageDescription ||
		result.Elements[0].ContentText != "连接池告警" {
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

func TestProcessorDoesNotRetryMalformedModelOutput(t *testing.T) {
	calls := 0
	model := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		return schema.AssistantMessage("", nil), nil
	})
	processor, err := NewProcessor(nil, &Endpoint{
		Generator: model, Provider: "stepfun", Model: "step-3.7-flash",
		Prompt: "prompt", PromptVersion: "vision-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage, SourcePath: "ppt/media/image1.png",
			SourcePart: "ppt/slides/slide1.xml", RelationshipID: "rIdImage",
			MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestProcessorClassifiesEmptyStructuredOutputWithoutRetry(t *testing.T) {
	calls := 0
	model := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		return schema.AssistantMessage(`{"ocrText":"","description":""}`, nil), nil
	})
	processor, err := NewProcessor(nil, &Endpoint{
		Generator: model, Provider: "stepfun", Model: "step-3.7-flash",
		Prompt: "prompt", PromptVersion: "vision-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage, SourcePath: "ppt/media/image3.png",
			SourcePart: "ppt/slides/slide1.xml", RelationshipID: "rIdImage",
			MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if !errors.Is(err, knowledgeenrichment.ErrNoUsableContent) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestProcessorDoesNotRetryGeneratorFailure(t *testing.T) {
	calls := 0
	model := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		return nil, errors.New("provider unavailable")
	})
	processor, err := NewProcessor(nil, &Endpoint{
		Generator: model, Provider: "stepfun", Model: "step-3.7-flash",
		Prompt: "prompt", PromptVersion: "vision-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), knowledgeenrichment.Request{
		Route: knowledgeenrichment.RouteOCRVLM,
		Asset: knowledgeparser.VisualAsset{
			Index: 0, Kind: knowledgeparser.VisualAssetEmbeddedImage, SourcePath: "ppt/media/image1.png",
			SourcePart: "ppt/slides/slide1.xml", RelationshipID: "rIdImage",
			MediaType: "image/png", Content: []byte("image"), SizeBytes: 5,
		},
	})
	if err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
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
