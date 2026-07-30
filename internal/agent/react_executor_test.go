package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type fakeModelState struct {
	boundTools int
	arguments  string
	modelCalls int
}

type fakeToolCallingModel struct {
	state *fakeModelState
	tools []*schema.ToolInfo
}

func (m *fakeToolCallingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.state.boundTools = len(tools)
	return &fakeToolCallingModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *fakeToolCallingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.state.modelCalls++
	var response *schema.Message
	for _, message := range input {
		if message.Role == schema.Tool {
			response = schema.AssistantMessage("diagnosis complete", nil)
			return withFakeUsage(response), nil
		}
	}
	if len(m.tools) == 0 {
		return nil, errors.New("expected at least one bound tool")
	}
	response = schema.AssistantMessage("", []schema.ToolCall{{
		ID:       "call-1",
		Function: schema.FunctionCall{Name: m.tools[0].Name, Arguments: m.state.arguments},
	}})
	return withFakeUsage(response), nil
}

func withFakeUsage(message *schema.Message) *schema.Message {
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 1},
		CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 3},
	}}
	return message
}

func (m *fakeToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestReActExecutorBindsOnlySkillToolsAndRecordsTrace(t *testing.T) {
	ctx := context.Background()
	invokable, err := toolutils.InferTool(
		ToolReadExternalCase,
		"read test case",
		func(context.Context, struct {
			ExternalCaseID string `json:"externalCaseId" jsonschema:"required"`
		}) (map[string]string, error) {
			return map[string]string{"title": "timeout"}, nil
		},
	)
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}
	handoffTool, err := NewRequestCodeInvestigationTool()
	if err != nil {
		t.Fatalf("NewRequestCodeInvestigationTool: %v", err)
	}
	registry, err := NewToolRegistry(ctx, []tool.BaseTool{invokable, handoffTool}...)
	if err != nil {
		t.Fatalf("NewToolRegistry: %v", err)
	}
	state := &fakeModelState{arguments: `{"externalCaseId":"11111111-1111-1111-1111-111111111111"}`}
	executor, err := NewReActExecutor(
		ctx, testSkills()[0], &fakeToolCallingModel{state: state}, registry, nil, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewReActExecutor: %v", err)
	}
	result, err := executor.Execute(ctx, RunRequest{
		UserQuery: "分析工单", ExternalCaseID: "11111111-1111-1111-1111-111111111111",
	}, testSkills()[0])
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.boundTools != 2 {
		t.Fatalf("bound tools = %d", state.boundTools)
	}
	if result.Answer != "diagnosis complete" {
		t.Fatalf("answer = %q", result.Answer)
	}
	if len(result.ToolExecutions) != 1 || result.ToolExecutions[0].Name != ToolReadExternalCase || !result.ToolExecutions[0].Succeeded {
		t.Fatalf("tool executions = %+v", result.ToolExecutions)
	}
	if result.Usage != (ModelUsage{
		ModelCalls: 2, PromptTokens: 20, CompletionTokens: 4,
		TotalTokens: 24, CachedTokens: 2, ReasoningTokens: 6,
	}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
}
