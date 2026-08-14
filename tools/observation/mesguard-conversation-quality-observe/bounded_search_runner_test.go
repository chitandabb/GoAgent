package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type repeatedQualitySearchModelState struct {
	mu    sync.Mutex
	calls int
}

type repeatedQualitySearchModel struct {
	state *repeatedQualitySearchModelState
	tools []*schema.ToolInfo
}

func (m *repeatedQualitySearchModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &repeatedQualitySearchModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *repeatedQualitySearchModel) Generate(
	_ context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.state.mu.Lock()
	m.state.calls++
	m.state.mu.Unlock()

	toolMessages := make([]*schema.Message, 0, 2)
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == mesagent.ToolSearchKnowledge {
			toolMessages = append(toolMessages, message)
		}
	}
	toolInfos := model.GetCommonOptions(nil, opts...).Tools
	toolChoice := model.GetCommonOptions(nil, opts...).ToolChoice
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	searchAvailable := slices.ContainsFunc(toolInfos, func(info *schema.ToolInfo) bool {
		return info != nil && info.Name == mesagent.ToolSearchKnowledge
	})
	if len(toolMessages) == 0 {
		if !searchAvailable {
			return nil, errors.New("search Tool was unavailable before the first evidence result")
		}
		return qualityToolCall("quality-search-1", `{"query":"连接池等待","maxResults":3}`), nil
	}
	if searchAvailable && (toolChoice == nil || *toolChoice != schema.ToolChoiceForbidden) {
		return qualityToolCall("quality-search-2", `{"query":"数据库连接池并发上限","maxResults":3}`), nil
	}
	if toolChoice == nil || *toolChoice != schema.ToolChoiceForbidden {
		return nil, errors.New("quality wrapper did not forbid Tool calls after the first search")
	}
	var payload struct {
		CitationSources []struct {
			SourceRef string `json:"sourceRef"`
		} `json:"citationSources"`
	}
	if err := json.Unmarshal([]byte(toolMessages[len(toolMessages)-1].Content), &payload); err != nil ||
		len(payload.CitationSources) == 0 {
		return nil, errors.New("cached Tool result lost citation sources")
	}
	return qualityUsage(schema.AssistantMessage(
		"连接池达到上限时，新请求会等待连接归还。[source:"+payload.CitationSources[0].SourceRef+"]",
		nil,
	)), nil
}

func (m *repeatedQualitySearchModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type qualitySearchCounter struct {
	calls  int
	limit  int
	result knowledge.HybridSearch
}

func (s *qualitySearchCounter) Search(
	_ context.Context,
	_ uuid.UUID,
	_ string,
	limit int,
) (knowledge.HybridSearch, error) {
	s.calls++
	s.limit = limit
	return s.result, nil
}

func TestSingleSearchQualityModelLetsRunnerAnswerAfterFirstSearch(t *testing.T) {
	actorID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	documentID, versionID, chunkID := uuid.New(), uuid.New(), uuid.New()
	content := "连接池达到最大连接数后，新的获取请求会等待已有连接归还。"
	plan, err := knowledge.OriginalQueryPlan("连接池等待")
	if err != nil {
		t.Fatal(err)
	}
	searcher := &qualitySearchCounter{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
			Title: "Go 数据库连接池说明", Scope: knowledge.ScopeGlobal,
			Ordinal: 1, ElementType: knowledge.ElementText, ContentText: content,
			ContentSHA256: knowledge.SHA256Hex(content), Score: 0.9, FTSRank: 1, FusedScore: 0.03,
		}},
		QueryPlan: plan, QueryRewriteStatus: knowledge.QueryRewriteDisabled,
		Sources: []string{"fts", "vector"},
	}}
	searchTool, err := mesagent.NewSearchKnowledgeTool(searcher)
	if err != nil {
		t.Fatal(err)
	}
	searchTool, err = newBoundedQualitySearchTool(searchTool, 3)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := mesagent.NewDefaultToolCatalog(context.Background(), mesagent.DefaultToolCatalogDependencies{
		ExternalCases: unavailableExternalCaseGetter{}, KnowledgeSearch: searchTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelState := &repeatedQualitySearchModel{state: &repeatedQualitySearchModelState{}}
	qualityModel := newSingleSearchQualityModel(modelState)
	runner, err := mesagent.NewConversationRunner(mesagent.ConversationRunnerConfig{
		ChatModel: qualityModel, ToolCatalog: catalog, SystemInstruction: "answer with exact citations",
		ModelProvider: "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v2",
		AvailableDependencies: []mesagent.ToolDependency{mesagent.ToolDependencyKnowledge},
		Logger:                zap.NewNop(), MaxIterations: 3, MaxToolCalls: 2, MaxTotalTokens: 6_000,
		MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "连接池满了会怎么样？",
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: actorID},
	})
	response, err := runner.Respond(ctx, conversation.AgentRequest{
		Conversation: conversation.Conversation{
			ID: conversationID, UserID: actorID, Status: conversation.StatusActive,
		},
		UserMessage: now, History: []conversation.Message{now},
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if searcher.calls != 1 || searcher.limit != 3 || len(response.Citations) != 1 ||
		response.Citations[0].SourceRef != "knowledge:"+versionID.String()+"/"+chunkID.String() {
		t.Fatalf("search calls=%d response=%+v", searcher.calls, response)
	}
	if response.RunObservation == nil || response.RunObservation.Outcome != conversation.AgentRunAnswered ||
		len(response.RunObservation.DegradedChannels) != 0 {
		t.Fatalf("single-search result did not remain an answered run: %+v", response.RunObservation)
	}
	if response.RunObservation.Usage.ModelCalls != 2 || response.RunObservation.Usage.TotalTokens != 240 {
		t.Fatalf("usage should be recorded once per model call: %+v", response.RunObservation.Usage)
	}
	modelState.state.mu.Lock()
	modelCalls := modelState.state.calls
	modelState.state.mu.Unlock()
	if modelCalls != 2 {
		t.Fatalf("model calls = %d, want one search decision and one final answer", modelCalls)
	}
	diagnostics := qualityModel.diagnostics.snapshotFrom(0)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics[0].SearchCompleted ||
		diagnostics[0].RequestedToolChoice != string(schema.ToolChoiceForced) ||
		!slices.Contains(diagnostics[0].VisibleTools, mesagent.ToolSearchKnowledge) ||
		!slices.Contains(diagnostics[0].ReturnedToolCalls, mesagent.ToolSearchKnowledge) {
		t.Fatalf("first call diagnostics = %+v", diagnostics[0])
	}
	if !diagnostics[1].SearchCompleted ||
		diagnostics[1].RequestedToolChoice != string(schema.ToolChoiceForbidden) ||
		!slices.Contains(diagnostics[1].VisibleTools, mesagent.ToolSearchKnowledge) ||
		len(diagnostics[1].ReturnedToolCalls) != 0 || !diagnostics[1].ContentPresent {
		t.Fatalf("final call diagnostics = %+v", diagnostics[1])
	}
}

func qualityToolCall(id, arguments string) *schema.Message {
	return qualityUsage(schema.AssistantMessage("", []schema.ToolCall{{
		ID: id, Function: schema.FunctionCall{Name: mesagent.ToolSearchKnowledge, Arguments: arguments},
	}}))
}

func qualityUsage(message *schema.Message) *schema.Message {
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
	}}
	return message
}
