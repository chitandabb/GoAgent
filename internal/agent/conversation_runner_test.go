package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type conversationRunnerModelState struct {
	mu                sync.Mutex
	createIfAvailable bool
	repeatCreate      bool
	schemas           [][]string
}

type conversationRunnerTestModel struct {
	state *conversationRunnerModelState
	tools []*schema.ToolInfo
}

func (m *conversationRunnerTestModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &conversationRunnerTestModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *conversationRunnerTestModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	toolInfos := common.Tools
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	names := make([]string, 0, len(toolInfos))
	for _, info := range toolInfos {
		names = append(names, info.Name)
	}
	m.state.mu.Lock()
	m.state.schemas = append(m.state.schemas, names)
	m.state.mu.Unlock()

	hasCreateResult := false
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == ToolCreateDiagnosisTask {
			hasCreateResult = true
		}
	}
	if m.state.createIfAvailable && slices.Contains(names, ToolCreateDiagnosisTask) &&
		(!hasCreateResult || m.state.repeatCreate) {
		return runnerTestToolCall(ToolCreateDiagnosisTask,
			`{"externalCaseId":"`+runnerTestCaseID.String()+`","diagnosisGoal":"请诊断这个工单"}`), nil
	}
	return withRunnerTestUsage(schema.AssistantMessage("已处理当前会话请求。", nil)), nil
}

func (m *conversationRunnerTestModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestConversationRunnerOnlyExposesCaseToolsForOneSelectedCase(t *testing.T) {
	tests := []struct {
		name       string
		references []conversation.CaseReference
		wantCase   bool
	}{
		{name: "no selected case"},
		{
			name: "one selected case",
			references: []conversation.CaseReference{{
				ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected,
			}},
			wantCase: true,
		},
		{
			name: "multiple selected cases",
			references: []conversation.CaseReference{
				{ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected},
				{ExternalCaseID: uuid.New(), Kind: conversation.ReferenceKindSelected},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &conversationRunnerModelState{}
			creator := &diagnosisToolCreatorStub{}
			runner := newConversationRunnerTest(t, state, creator)
			request, ctx := conversationRunnerRequest(test.references)
			if _, err := runner.Respond(ctx, request); err != nil {
				t.Fatalf("Respond(): %v", err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.schemas) == 0 {
				t.Fatal("model received no Tool schema snapshot")
			}
			for _, name := range []string{ToolReadExternalCase, ToolCreateDiagnosisTask} {
				if got := slices.Contains(state.schemas[0], name); got != test.wantCase {
					t.Fatalf("Tool %s exposed=%v, want %v; schema=%v", name, got, test.wantCase, state.schemas[0])
				}
			}
		})
	}
}

func TestConversationRunnerOnlyExposesTaskStatusForReferencedTask(t *testing.T) {
	for _, test := range []struct {
		name       string
		references []conversation.TaskReference
		want       bool
	}{
		{name: "no task reference"},
		{
			name: "referenced task",
			references: []conversation.TaskReference{{
				TaskID: uuid.New(), Kind: conversation.ReferenceKindReferenced,
			}},
			want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &conversationRunnerModelState{}
			runner := newConversationRunnerTest(t, state, &diagnosisToolCreatorStub{})
			request, ctx := conversationRunnerRequest(nil)
			request.UserMessage.TaskReferences = test.references
			request.History[0] = request.UserMessage
			if _, err := runner.Respond(ctx, request); err != nil {
				t.Fatalf("Respond(): %v", err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if got := slices.Contains(state.schemas[0], ToolGetDiagnosisTaskStatus); got != test.want {
				t.Fatalf("task status Tool exposed=%v, want %v; schema=%v", got, test.want, state.schemas[0])
			}
		})
	}
}

func TestConversationRunnerExecutesCreateDiagnosisTaskOnce(t *testing.T) {
	state := &conversationRunnerModelState{createIfAvailable: true}
	creator := &diagnosisToolCreatorStub{result: conversation.CreateDiagnosisResult{
		Task: diagnosis.DiagnosisTask{ID: uuid.New(), Status: diagnosis.TaskPending},
	}}
	runner := newConversationRunnerTest(t, state, creator)
	request, ctx := conversationRunnerRequest([]conversation.CaseReference{{
		ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected,
	}})

	result, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	if result.Content != "已处理当前会话请求。" || creator.calls != 1 || creator.input.ExternalCaseID != runnerTestCaseID {
		t.Fatalf("result=%+v creator calls=%d input=%+v", result, creator.calls, creator.input)
	}
}

func TestConversationRunnerRejectsSecondCreateDiagnosisTaskCall(t *testing.T) {
	state := &conversationRunnerModelState{createIfAvailable: true, repeatCreate: true}
	creator := &diagnosisToolCreatorStub{result: conversation.CreateDiagnosisResult{
		Task: diagnosis.DiagnosisTask{ID: uuid.New(), Status: diagnosis.TaskPending},
	}}
	runner := newConversationRunnerTest(t, state, creator)
	request, ctx := conversationRunnerRequest([]conversation.CaseReference{{
		ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected,
	}})

	_, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrAgentToolRunLimitExhausted) {
		t.Fatalf("Respond() error = %v, want ErrAgentToolRunLimitExhausted", err)
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls = %d, want 1", creator.calls)
	}
}

func TestConversationModelMessagesBoundsHistoryAndDropsInternalRoles(t *testing.T) {
	conversationID := uuid.New()
	current := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 5,
		Role: conversation.MessageRoleUser, Content: "当前问题",
	}
	messages := conversationModelMessages([]conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "这是一条应被预算舍弃的很长旧消息"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleSystem, Content: "不可信系统消息"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 3, Role: conversation.MessageRoleAssistant, Content: "上轮回答"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleTool, Content: "原始工具结果"},
		current,
	}, current, 8)

	if len(messages) != 2 || messages[0].Role != schema.Assistant || messages[0].Content != "上轮回答" ||
		messages[1].Role != schema.User || messages[1].Content != "当前问题" {
		t.Fatalf("model messages = %+v", messages)
	}
}

func newConversationRunnerTest(
	t *testing.T,
	state *conversationRunnerModelState,
	creator DiagnosisTaskCreator,
) *ConversationRunner {
	t.Helper()
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, CreateDiagnosisTask: creator,
		DiagnosisTaskStatus: &diagnosisTaskStatusReaderStub{},
	})
	if err != nil {
		t.Fatalf("NewDefaultToolCatalog(): %v", err)
	}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel:             &conversationRunnerTestModel{state: state},
		ToolCatalog:           catalog,
		SystemInstruction:     "conversation test instruction",
		AvailableDependencies: []ToolDependency{ToolDependencyExternalCase},
		Logger:                zap.NewNop(),
		MaxContextRunes:       conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatalf("NewConversationRunner(): %v", err)
	}
	return runner
}

func conversationRunnerRequest(references []conversation.CaseReference) (conversation.AgentRequest, context.Context) {
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	current := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "请诊断这个工单", CaseReferences: references,
	}
	request := conversation.AgentRequest{
		Conversation: conversation.Conversation{ID: conversationID, UserID: userID, Status: conversation.StatusActive},
		UserMessage:  current,
		History:      []conversation.Message{current},
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID,
		UserMessageID:  messageID,
		Actor:          conversation.Actor{UserID: userID},
	})
	return request, ctx
}
