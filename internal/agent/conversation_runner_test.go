package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type conversationRunnerModelState struct {
	mu                         sync.Mutex
	createIfAvailable          bool
	repeatCreate               bool
	searchKnowledgeIfAvailable bool
	omitKnowledgeCitation      bool
	finalContent               string
	schemas                    [][]string
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
	hasKnowledgeResult := false
	knowledgeSourceRef := ""
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == ToolCreateDiagnosisTask {
			hasCreateResult = true
		}
		if message.Role == schema.Tool && message.ToolName == ToolSearchKnowledge {
			hasKnowledgeResult = true
			var payload struct {
				CitationSources []conversation.MessageCitation `json:"citationSources"`
			}
			if json.Unmarshal([]byte(message.Content), &payload) == nil && len(payload.CitationSources) > 0 {
				knowledgeSourceRef = payload.CitationSources[0].SourceRef
			}
		}
	}
	if m.state.createIfAvailable && slices.Contains(names, ToolCreateDiagnosisTask) &&
		(!hasCreateResult || m.state.repeatCreate) {
		return runnerTestToolCall(ToolCreateDiagnosisTask,
			`{"externalCaseId":"`+runnerTestCaseID.String()+`","diagnosisGoal":"请诊断这个工单"}`), nil
	}
	if m.state.searchKnowledgeIfAvailable && slices.Contains(names, ToolSearchKnowledge) && !hasKnowledgeResult {
		return runnerTestToolCall(ToolSearchKnowledge, `{"query":"连接池超时","maxResults":3}`), nil
	}
	if knowledgeSourceRef != "" {
		if m.state.omitKnowledgeCitation {
			return withRunnerTestUsage(schema.AssistantMessage("应先检查连接池配置。", nil)), nil
		}
		return withRunnerTestUsage(schema.AssistantMessage(
			"应先检查连接池配置。[source:"+knowledgeSourceRef+"]", nil,
		)), nil
	}
	if m.state.finalContent != "" {
		return withRunnerTestUsage(schema.AssistantMessage(m.state.finalContent, nil)), nil
	}
	return withRunnerTestUsage(schema.AssistantMessage("已处理当前会话请求。", nil)), nil
}

type conversationCitationRepairerStub struct {
	calls int
}

func (s *conversationCitationRepairerStub) Repair(
	_ context.Context,
	request ConversationCitationRepairRequest,
) (ConversationCitationRepairResult, error) {
	s.calls++
	if len(request.Evidence) == 0 || len(request.Sources) == 0 {
		return ConversationCitationRepairResult{}, errors.New("repair evidence missing")
	}
	marker, err := conversation.FormatAnswerCitationMarker(request.Sources[0])
	if err != nil {
		return ConversationCitationRepairResult{}, err
	}
	return ConversationCitationRepairResult{
		Answer: "应先检查连接池配置。" + marker,
		Usage:  ModelUsage{ModelCalls: 1, PromptTokens: 6, CompletionTokens: 2, TotalTokens: 8},
	}, nil
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

func TestConversationRunnerScopesAttachmentToolToReferencedMessages(t *testing.T) {
	runner := &ConversationRunner{availableDependencies: []ToolDependency{ToolDependencyAttachment}}
	actor := conversation.Actor{UserID: uuid.New()}
	withoutAttachment, err := runner.conversationScope(actor, conversation.Message{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutAttachment.CapabilityAllowed(ToolCapabilityAttachment) {
		t.Fatal("attachment capability was exposed without a message attachment")
	}
	withAttachment, err := runner.conversationScope(actor, conversation.Message{Attachments: []conversation.MessageAttachment{{
		AttachmentID: uuid.New(), Position: 0, Purpose: "context", Status: "uploaded",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !withAttachment.CapabilityAllowed(ToolCapabilityAttachment) ||
		!withAttachment.DependencyAvailable(ToolDependencyAttachment) {
		t.Fatalf("attachment scope=%+v", withAttachment)
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
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || failure.ErrorType != "agent_tool_run_limit_exhausted" ||
		failure.Observation.Outcome != conversation.AgentRunFailed {
		t.Fatalf("failure record = %+v, present=%v", failure, ok)
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls = %d, want 1", creator.calls)
	}
}

func TestConversationRunnerPersistsOnlyCitedSameRunKnowledgeSource(t *testing.T) {
	versionID, chunkID := uuid.New(), uuid.New()
	content := "连接池耗尽时应先检查最大连接数和事务超时。"
	queryPlan, err := knowledge.OriginalQueryPlan("连接池超时")
	if err != nil {
		t.Fatal(err)
	}
	knowledgeTool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: uuid.New(), DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "数据库运行手册", Scope: knowledge.ScopeGlobal, ElementType: knowledge.ElementText,
			ContentText: content, ContentSHA256: knowledge.SHA256Hex(content), Score: 0.91,
		}},
		QueryPlan: queryPlan, QueryRewriteStatus: knowledge.QueryRewriteDisabled, Sources: []string{"fts"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, KnowledgeSearch: knowledgeTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: &conversationRunnerModelState{
			searchKnowledgeIfAvailable: true,
		}},
		ToolCatalog: catalog, SystemInstruction: "conversation citation test",
		ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
		AvailableDependencies: []ToolDependency{ToolDependencyKnowledge}, Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	wantRef := "knowledge:" + versionID.String() + "/" + chunkID.String()
	if len(response.Citations) != 1 || response.Citations[0].SourceRef != wantRef ||
		response.Citations[0].ContentSHA256 != knowledge.SHA256Hex(content) ||
		response.Content != "应先检查连接池配置。[source:"+wantRef+"]" {
		t.Fatalf("response = %+v", response)
	}
	if response.RunObservation == nil || response.RunObservation.Outcome != conversation.AgentRunAnswered ||
		response.RunObservation.ModelProvider != "fixture" || len(response.RunObservation.RetrievedSources) != 1 ||
		response.RunObservation.RetrievedSources[0].SourceRef != wantRef ||
		response.RunObservation.Usage.TotalTokens != 24 {
		t.Fatalf("run observation = %+v", response.RunObservation)
	}
}

func TestConversationRunnerRepairsZeroCitationAnswerOnce(t *testing.T) {
	versionID, chunkID := uuid.New(), uuid.New()
	content := "连接池耗尽时应先检查最大连接数和事务超时。"
	queryPlan, err := knowledge.OriginalQueryPlan("连接池超时")
	if err != nil {
		t.Fatal(err)
	}
	knowledgeTool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: uuid.New(), DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "数据库运行手册", Scope: knowledge.ScopeGlobal, ElementType: knowledge.ElementText,
			ContentText: content, ContentSHA256: knowledge.SHA256Hex(content), Score: 0.91,
		}},
		QueryPlan: queryPlan, QueryRewriteStatus: knowledge.QueryRewriteDisabled, Sources: []string{"fts"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, KnowledgeSearch: knowledgeTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	repairer := &conversationCitationRepairerStub{}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: &conversationRunnerModelState{
			searchKnowledgeIfAvailable: true, omitKnowledgeCitation: true,
		}},
		CitationRepairer: repairer,
		ToolCatalog:      catalog, SystemInstruction: "conversation citation repair test",
		ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
		AvailableDependencies: []ToolDependency{ToolDependencyKnowledge}, Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wantRef := "knowledge:" + versionID.String() + "/" + chunkID.String()
	if repairer.calls != 1 || len(response.Citations) != 1 || response.Citations[0].SourceRef != wantRef ||
		response.RunObservation == nil || response.RunObservation.Outcome != conversation.AgentRunAnswered ||
		response.RunObservation.Usage.TotalTokens != 32 {
		t.Fatalf("repairer calls = %d, response = %+v", repairer.calls, response)
	}
}

func TestConversationRunnerRejectsFabricatedCitation(t *testing.T) {
	state := &conversationRunnerModelState{
		finalContent: "这是一个没有工具来源的结论。[source:https://example.com/fabricated]",
	}
	runner := newConversationRunnerTest(t, state, &diagnosisToolCreatorStub{})
	request, ctx := conversationRunnerRequest(nil)
	_, err := runner.Respond(ctx, request)
	if !errors.Is(err, conversation.ErrAgentResponseInvalid) {
		t.Fatalf("Respond() error = %v, want ErrAgentResponseInvalid", err)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || failure.ErrorType != "agent_response_invalid" ||
		failure.Observation.Outcome != conversation.AgentRunFailed || failure.Observation.Usage.ModelCalls != 1 {
		t.Fatalf("failure record = %+v, present=%v", failure, ok)
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
		ModelProvider:         "fixture",
		ModelID:               "fixture-v1",
		PromptVersion:         "conversation-test-v1",
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
