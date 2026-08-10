package tablemodel

import (
	"context"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/knowledgeparser"
	"github.com/chitandabb/GoAgent/internal/knowledgetable"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestProcessorRecoversStrictTableStructure(t *testing.T) {
	generator := generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		message := schema.AssistantMessage(`{
          "markdown":"| alarm | count |\n| --- | ---: |\n| E42 | 3 |",
          "cells":[
            {"row":0,"column":0,"rowSpan":1,"columnSpan":1,"text":"alarm","header":true},
            {"row":0,"column":1,"rowSpan":1,"columnSpan":1,"text":"count","header":true},
            {"row":1,"column":0,"rowSpan":1,"columnSpan":1,"text":"E42","header":false},
            {"row":1,"column":1,"rowSpan":1,"columnSpan":1,"text":"3","header":false}
          ],
          "confidence":0.93,
          "warnings":[]
        }`, nil)
		message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140,
		}}
		return message, nil
	})
	processor, err := NewProcessor(Endpoint{
		Generator: generator, Provider: "dashscope", Model: "qwen3-vl-plus",
		Prompt: "recover table", PromptVersion: "table-recovery-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Recover(context.Background(), tableRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cells) != 4 || result.Cells[0].Text != "alarm" || result.Confidence != 0.93 ||
		result.Usage == nil || result.Usage.TotalTokens != 140 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProcessorRejectsUnknownFieldsAndInvalidCells(t *testing.T) {
	responses := []string{
		`{"markdown":"| a |","cells":[],"confidence":1,"warnings":[],"unexpected":true}`,
		`{"markdown":"| a |","cells":[{"row":0,"column":0,"rowSpan":0,"columnSpan":1,"text":"a","header":true}],"confidence":1,"warnings":[]}`,
		`{"markdown":"| a |","cells":[{"row":0,"column":0,"rowSpan":1,"columnSpan":1,"text":"a","header":true}],"confidence":1,"warnings":[]} trailing`,
	}
	for _, response := range responses {
		processor, err := NewProcessor(Endpoint{
			Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage(response, nil), nil
			}),
			Provider: "dashscope", Model: "qwen3-vl-plus", Prompt: "recover table",
			PromptVersion: "table-recovery-v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := processor.Recover(context.Background(), tableRequest()); err == nil {
			t.Fatalf("expected response rejection: %s", response)
		}
	}
}

func TestProcessorBuildsBoundedImageMessage(t *testing.T) {
	processor, err := NewProcessor(Endpoint{
		Generator: generatorFunc(func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			if len(messages) != 1 || len(messages[0].UserInputMultiContent) != 2 ||
				!strings.Contains(messages[0].UserInputMultiContent[0].Text, "Page: 1") ||
				messages[0].UserInputMultiContent[1].Image == nil ||
				messages[0].UserInputMultiContent[1].Image.MIMEType != "image/png" {
				t.Fatalf("messages = %+v", messages)
			}
			return schema.AssistantMessage(`{"markdown":"| a |\n| --- |\n| 1 |","cells":[{"row":0,"column":0,"rowSpan":1,"columnSpan":1,"text":"a","header":true},{"row":1,"column":0,"rowSpan":1,"columnSpan":1,"text":"1","header":false}],"confidence":1,"warnings":[]}`, nil), nil
		}),
		Provider: "dashscope", Model: "qwen3-vl-plus", Prompt: "recover table",
		PromptVersion: "table-recovery-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Recover(context.Background(), tableRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorMarksCollapsedVisibleRowsPartial(t *testing.T) {
	processor, err := NewProcessor(Endpoint{
		Generator: generatorFunc(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage(`{
              "markdown":"| standard | description |\n|---|---|\n| ISO | 207 a<br>224 b |",
              "cells":[
                {"row":0,"column":0,"rowSpan":1,"columnSpan":1,"text":"standard","header":true},
                {"row":0,"column":1,"rowSpan":1,"columnSpan":1,"text":"description","header":true},
                {"row":1,"column":0,"rowSpan":1,"columnSpan":1,"text":"ISO","header":false},
                {"row":1,"column":1,"rowSpan":1,"columnSpan":1,"text":"207 a\n224 b","header":false}
              ],
              "confidence":1,
              "warnings":[]
            }`, nil), nil
		}),
		Provider: "dashscope", Model: "qwen3-vl-plus", Prompt: "recover table",
		PromptVersion: "table-recovery-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Recover(context.Background(), tableRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.Reason != "multiline_cell_structure_ambiguous" ||
		result.Confidence != 0.8 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

type generatorFunc func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)

func (f generatorFunc) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return f(ctx, messages, options...)
}

func tableRequest() knowledgetable.Request {
	page := 1
	content := []byte("png")
	return knowledgetable.Request{
		Asset: knowledgeparser.VisualAsset{
			Index: 1, Kind: knowledgeparser.VisualAssetLayoutRegion, PageNumber: &page,
			SourcePath: "pages/1/layout-regions/2", MediaType: "image/png",
			SizeBytes: int64(len(content)), SHA256: knowledge.SHA256Hex(string(content)),
			Width: 1, Height: 1, Content: content,
		},
		Reason: "table_structure_required",
	}
}
