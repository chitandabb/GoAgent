package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

type sourceRecoveryReaderStub struct {
	request conversationmemory.SourceReadRequest
	result  conversationmemory.SourceReadResult
	err     error
	calls   int
}

type agentSourceActiveReader struct {
	snapshot conversationmemory.Snapshot
}

func (r agentSourceActiveReader) Active(
	_ context.Context,
	_ uuid.UUID,
) (*conversationmemory.Snapshot, error) {
	copy := r.snapshot
	return &copy, nil
}

type agentSourceMessageReader struct {
	messages []conversation.Message
}

func (r agentSourceMessageReader) ReadSourceMessages(
	_ context.Context,
	_ uuid.UUID,
	conversationID uuid.UUID,
	sequences []int64,
) ([]conversation.Message, error) {
	result := make([]conversation.Message, 0, len(sequences))
	for _, sequence := range sequences {
		for _, message := range r.messages {
			if message.ConversationID == conversationID && message.Seq == sequence {
				result = append(result, message)
				break
			}
		}
	}
	return result, nil
}

func (s *sourceRecoveryReaderStub) Read(
	_ context.Context,
	request conversationmemory.SourceReadRequest,
) (conversationmemory.SourceReadResult, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func TestConversationMemorySourceToolInjectsCurrentConversationScope(t *testing.T) {
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	reader := &sourceRecoveryReaderStub{result: conversationmemory.SourceReadResult{
		Messages: []conversationmemory.SourceMessage{{
			MessageRef: "conversation_message:" + uuid.NewString(),
			Seq:        4, Role: conversation.MessageRoleUser, Content: "原始错误日志",
			ContentComplete: true,
		}},
	}}
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := sourceRecoveryConversationScope(t, userID)
	ctx := conversation.WithCommandContext(WithTaskScope(context.Background(), scope), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	raw, err := current.InvokableRun(ctx, `{"entryId":"fact-1","query":"SQLSTATE HYT00"}`)
	if err != nil {
		t.Fatalf("InvokableRun(): %v", err)
	}
	if reader.calls != 1 || reader.request.Actor.UserID != userID ||
		reader.request.ConversationID != conversationID || reader.request.EntryID != "fact-1" ||
		reader.request.Query != "SQLSTATE HYT00" {
		t.Fatalf("source recovery request = %+v, calls=%d", reader.request, reader.calls)
	}
	var result conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode Tool result: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Content != "原始错误日志" {
		t.Fatalf("Tool result = %+v", result)
	}
	if strings.Contains(raw, `"messageId"`) {
		t.Fatalf("Tool result exposed database message id: %s", raw)
	}
	info, err := current.Info(ctx)
	if err != nil {
		t.Fatalf("Info(): %v", err)
	}
	encodedInfo, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal Tool info: %v", err)
	}
	if strings.Contains(strings.ToLower(string(encodedInfo)), "conversationid") {
		t.Fatalf("Tool schema exposes conversationId: %s", encodedInfo)
	}
}

func TestConversationMemorySourceToolRejectsInvalidQueryAndOffsetCombinations(t *testing.T) {
	userID := uuid.New()
	reader := &sourceRecoveryReaderStub{}
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := sourceRecoveryConversationScope(t, userID)
	ctx := conversation.WithCommandContext(WithTaskScope(context.Background(), scope), conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(),
		Actor: conversation.Actor{UserID: userID},
	})
	longQuery, err := json.Marshal(map[string]any{
		"entryId": "fact-1", "query": strings.Repeat("查", 257),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string]string{
		"query over 256 runes":      string(longQuery),
		"negative offset":           `{"sourceMessageSeqs":[1],"contentOffsetRunes":-1}`,
		"offset with entry":         `{"entryId":"fact-1","contentOffsetRunes":1}`,
		"offset with query":         `{"sourceMessageSeqs":[1],"query":"error","contentOffsetRunes":1}`,
		"offset with multiple seqs": `{"sourceMessageSeqs":[1,2],"contentOffsetRunes":1}`,
		"cursor with query":         `{"continuationCursor":"cursor-1","query":"error"}`,
		"cursor with offset":        `{"continuationCursor":"cursor-1","contentOffsetRunes":1}`,
		"zero offset with entry":    `{"entryId":"fact-1","contentOffsetRunes":0}`,
		"zero offset with query":    `{"sourceMessageSeqs":[1],"query":"error","contentOffsetRunes":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := current.InvokableRun(ctx, arguments); !errors.Is(err, conversationmemory.ErrInvalidSourceRead) {
				t.Fatalf("InvokableRun() error = %v, want ErrInvalidSourceRead", err)
			}
		})
	}
	if reader.calls != 0 {
		t.Fatalf("invalid requests reached source recovery reader: calls=%d", reader.calls)
	}
	if _, err := current.InvokableRun(ctx, `{"sourceMessageSeqs":[7],"contentOffsetRunes":1024}`); err != nil {
		t.Fatalf("valid offset InvokableRun(): %v", err)
	}
	if reader.calls != 1 || !slices.Equal(reader.request.SourceMessageSeqs, []int64{7}) ||
		reader.request.ContentOffsetRunes == nil || *reader.request.ContentOffsetRunes != 1024 {
		t.Fatalf("valid offset request = %+v, calls=%d", reader.request, reader.calls)
	}
}

func TestConversationMemorySourceToolRejectsMissingOrMismatchedRunScope(t *testing.T) {
	userID := uuid.New()
	reader := &sourceRecoveryReaderStub{}
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.InvokableRun(context.Background(), `{"entryId":"fact-1"}`); !errors.Is(err, conversation.ErrCommandContextRequired) {
		t.Fatalf("missing CommandContext error = %v", err)
	}

	command := conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(), Actor: conversation.Actor{UserID: userID},
	}
	wrongUserScope := sourceRecoveryConversationScope(t, uuid.New())
	ctx := conversation.WithCommandContext(WithTaskScope(context.Background(), wrongUserScope), command)
	if _, err := current.InvokableRun(ctx, `{"entryId":"fact-1"}`); !errors.Is(err, ErrTaskScopeRequired) {
		t.Fatalf("mismatched TaskScope error = %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("source recovery reader calls = %d, want 0", reader.calls)
	}
}

func TestDefaultToolCatalogRequiresMemoryCapabilityForSourceRecovery(t *testing.T) {
	reader := &sourceRecoveryReaderStub{}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, ConversationMemorySources: reader,
	})
	if err != nil {
		t.Fatalf("NewDefaultToolCatalog(): %v", err)
	}
	userID := uuid.New()
	withMemory := sourceRecoveryConversationScope(t, userID)
	tools, err := catalog.ToolsFor(WithTaskScope(context.Background(), withMemory), withMemory)
	if err != nil {
		t.Fatalf("ToolsFor(memory): %v", err)
	}
	found := false
	for _, current := range tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool Info(): %v", infoErr)
		}
		found = found || info.Name == ToolReadConversationMemorySources
	}
	if !found {
		t.Fatalf("memory Tool not exposed: %+v", tools)
	}

	withoutMemory, err := NewTaskScope(TaskScopeConfig{
		UserID: userID, Role: auth.RoleAnalyst, TaskType: TaskTypeConversation,
	})
	if err != nil {
		t.Fatalf("NewTaskScope(without memory): %v", err)
	}
	tools, err = catalog.ToolsFor(WithTaskScope(context.Background(), withoutMemory), withoutMemory)
	if err != nil {
		t.Fatalf("ToolsFor(without memory): %v", err)
	}
	for _, current := range tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool Info(): %v", infoErr)
		}
		if info.Name == ToolReadConversationMemorySources {
			t.Fatal("memory Tool exposed without memory capability")
		}
	}
}

func TestConversationMemorySourceToolAppliesRecoveryBoundsAndCursorEndToEnd(t *testing.T) {
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	sequences := make([]int64, 21)
	messages := make([]conversation.Message, 0, 22)
	for index := range sequences {
		sequence := int64(index + 1)
		sequences[index] = sequence
		messages = append(messages, conversation.Message{
			ID: uuid.New(), ConversationID: conversationID, Seq: sequence,
			Role: conversation.MessageRoleUser, Content: fmt.Sprintf("source-%02d", sequence),
		})
	}
	marker := "SQLSTATE HYT00 数据库连接池超时"
	messages = append(messages, conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 22,
		Role:    conversation.MessageRoleUser,
		Content: strings.Repeat("前", 12_000) + marker + strings.Repeat("后", 7_000),
	})
	snapshot := agentSourceSnapshot(t, conversationID, sequences)
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethodLocalCalibrated, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	counter, err := conversationmemory.NewSourceTokenCounter(estimator, "chat-main")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := conversationmemory.NewSourceRecovery(conversationmemory.SourceRecoveryConfig{
		ActiveSnapshots: agentSourceActiveReader{snapshot: snapshot},
		Messages:        agentSourceMessageReader{messages: messages}, TokenCounter: counter,
		MaxMessages: 20, MaxTokens: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, ConversationMemorySources: recovery,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := sourceRecoveryConversationScope(t, userID)
	ctx := conversation.WithCommandContext(WithTaskScope(context.Background(), scope), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	ctx = conversationmemory.WithSourceRecoveryRun(ctx)
	current := sourceRecoveryToolFromCatalog(t, catalog, ctx, scope)

	raw, err := current.InvokableRun(ctx, `{"entryId":"goal_sources"}`)
	if err != nil {
		t.Fatalf("first InvokableRun(): %v", err)
	}
	var first conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(raw), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 20 || first.Messages[19].Seq != 20 ||
		!first.HasMore || first.ContinuationCursor == "" {
		t.Fatalf("first bounded Tool result = %+v", first)
	}
	wrongConversationCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	if _, err := current.InvokableRun(wrongConversationCtx,
		`{"continuationCursor":"`+first.ContinuationCursor+`"}`,
	); !errors.Is(err, conversationmemory.ErrSourceCursorInvalid) {
		t.Fatalf("cross-conversation cursor error = %v", err)
	}
	raw, err = current.InvokableRun(ctx, `{"continuationCursor":"`+first.ContinuationCursor+`"}`)
	if err != nil {
		t.Fatalf("continuation InvokableRun(): %v", err)
	}
	var second conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(raw), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 1 || second.Messages[0].Seq != 21 || second.HasMore {
		t.Fatalf("continuation Tool result = %+v", second)
	}

	raw, err = current.InvokableRun(ctx, `{"sourceMessageSeqs":[22],"query":"SQLSTATE HYT00 连接池超时"}`)
	if err != nil {
		t.Fatalf("oversized InvokableRun(): %v", err)
	}
	var oversized conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(raw), &oversized); err != nil {
		t.Fatal(err)
	}
	tokens, err := counter.Count(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if tokens > 8192 || oversized.Mode != conversationmemory.SourceReadModeRelevant ||
		len(oversized.Messages) != 1 || !strings.Contains(oversized.Messages[0].Content, marker) ||
		oversized.Messages[0].ContentOffsetRunes == 0 {
		t.Fatalf("oversized bounded result tokens=%d result=%+v", tokens, oversized)
	}
}

func TestConversationRunnerEnablesMemoryCapabilityAndLimitsSourceRecoveryCalls(t *testing.T) {
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	runner := &ConversationRunner{memorySourceRecoveryEnabled: true}
	scope, err := runner.conversationScope(conversation.Actor{UserID: userID}, conversation.Message{})
	if err != nil {
		t.Fatalf("conversationScope(): %v", err)
	}
	if !scope.CapabilityAllowed(ToolCapabilityMemory) {
		t.Fatal("conversation scope does not include memory capability")
	}

	reader := &sourceRecoveryReaderStub{result: conversationmemory.SourceReadResult{
		Messages: []conversationmemory.SourceMessage{{
			MessageRef: "conversation_message:" + uuid.NewString(),
			Seq:        1, Role: conversation.MessageRoleUser, Content: "source", ContentComplete: true,
		}},
		HasMore: true, ContinuationAvailable: true, ContinuationCursor: "next-page",
	}}
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	trace := &executionTrace{}
	ctx := sourceRecoveryToolRunContext(scope, conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	}, trace)
	endpoint := newConversationToolTraceMiddleware(64 * 1024).Invokable(
		func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			raw, invokeErr := current.InvokableRun(ctx, input.Arguments)
			if invokeErr != nil {
				return nil, invokeErr
			}
			return &compose.ToolOutput{Result: raw}, nil
		},
	)
	firstOutput, err := endpoint(ctx, &compose.ToolInput{
		Name: ToolReadConversationMemorySources, Arguments: `{"entryId":"fact-1"}`,
	})
	if err != nil {
		t.Fatalf("first source recovery call: %v", err)
	}
	var first conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(firstOutput.Result), &first); err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || !first.ContinuationAvailable || first.ContinuationCursor != "next-page" ||
		first.TruncatedByTurnBudget {
		t.Fatalf("first source recovery result = %+v", first)
	}
	secondOutput, err := endpoint(ctx, &compose.ToolInput{
		Name: ToolReadConversationMemorySources, Arguments: `{"entryId":"fact-1"}`,
	})
	if err != nil {
		t.Fatalf("second source recovery call: %v", err)
	}
	var second conversationmemory.SourceReadResult
	if err := json.Unmarshal([]byte(secondOutput.Result), &second); err != nil {
		t.Fatal(err)
	}
	if !second.HasMore || second.ContinuationAvailable || second.ContinuationCursor != "" ||
		!second.TruncatedByTurnBudget {
		t.Fatalf("second source recovery result = %+v", second)
	}
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: ToolReadConversationMemorySources, Arguments: `{"entryId":"fact-1"}`,
	}); !errors.Is(err, ErrAgentToolRunLimitExhausted) {
		t.Fatalf("third source recovery call error = %v, want ErrAgentToolRunLimitExhausted", err)
	}
	entries := trace.snapshot()
	if reader.calls != 2 || len(entries) != 2 || !entries[0].Succeeded || !entries[1].Succeeded {
		t.Fatalf("reader calls=%d trace=%+v", reader.calls, entries)
	}
}

func TestConversationMemorySourceToolFailureEntersExecutionTrace(t *testing.T) {
	userID := uuid.New()
	scope := sourceRecoveryConversationScope(t, userID)
	reader := &sourceRecoveryReaderStub{err: errors.New("snapshot unavailable")}
	current, err := NewConversationMemorySourceTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	trace := &executionTrace{}
	ctx := sourceRecoveryToolRunContext(scope, conversation.CommandContext{
		ConversationID: uuid.New(), UserMessageID: uuid.New(),
		Actor: conversation.Actor{UserID: userID},
	}, trace)
	endpoint := newConversationToolTraceMiddleware(64 * 1024).Invokable(
		func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			raw, invokeErr := current.InvokableRun(ctx, input.Arguments)
			if invokeErr != nil {
				return nil, invokeErr
			}
			return &compose.ToolOutput{Result: raw}, nil
		},
	)
	if _, err := endpoint(ctx, &compose.ToolInput{
		Name: ToolReadConversationMemorySources, Arguments: `{"entryId":"fact-1"}`,
	}); err == nil {
		t.Fatal("source recovery failure unexpectedly succeeded")
	}
	entries := trace.snapshot()
	if len(entries) != 1 || entries[0].Name != ToolReadConversationMemorySources ||
		entries[0].Succeeded || entries[0].Error == "" {
		t.Fatalf("failure trace = %+v", entries)
	}
}

func sourceRecoveryToolRunContext(
	scope TaskScope,
	command conversation.CommandContext,
	trace *executionTrace,
) context.Context {
	ctx := conversation.WithCommandContext(context.Background(), command)
	ctx = WithTaskScope(ctx, scope)
	ctx = conversationmemory.WithSourceRecoveryRun(ctx)
	ctx = withAgentToolRunPolicy(ctx, newAgentToolRunPolicy(nil, map[string]int{
		ToolReadConversationMemorySources: 2,
	}))
	ctx = withExecutionBudget(ctx, newExecutionBudget(8, 16_000))
	return withExecutionTrace(ctx, trace)
}

func agentSourceSnapshot(
	t *testing.T,
	conversationID uuid.UUID,
	goalSequences []int64,
) conversationmemory.Snapshot {
	t.Helper()
	createdAt := time.Now().Add(-time.Minute).UTC()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, FromSeq: 1, ThroughSeq: 22,
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload: conversationmemory.Payload{
			ConversationGoal: &conversationmemory.Entry{
				EntryID: "goal_sources", Content: "恢复原文",
				SourceMessageSeqs: append([]int64(nil), goalSequences...),
				Status:            conversationmemory.EntryStatusActive,
			},
			Facts: []conversationmemory.Entry{{
				EntryID: "fact_oversized", Content: "超长消息",
				SourceMessageSeqs: []int64{22}, Status: conversationmemory.EntryStatusActive,
			}},
			Decisions: []conversationmemory.Entry{}, Corrections: []conversationmemory.Entry{},
			EvidenceReferences: []conversationmemory.ReferenceEntry{}, OpenQuestions: []conversationmemory.Entry{},
			Todos: []conversationmemory.Entry{}, TaskReferences: []conversationmemory.ReferenceEntry{},
			ReportReferences: []conversationmemory.ReferenceEntry{},
		},
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	activatedAt := createdAt.Add(time.Second)
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = &activatedAt
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func sourceRecoveryToolFromCatalog(
	t *testing.T,
	catalog *ToolCatalog,
	ctx context.Context,
	scope TaskScope,
) tool.InvokableTool {
	t.Helper()
	tools, err := catalog.ToolsFor(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range tools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name != ToolReadConversationMemorySources {
			continue
		}
		invokable, ok := current.(tool.InvokableTool)
		if !ok {
			t.Fatalf("memory Tool type = %T", current)
		}
		return invokable
	}
	names := make([]string, 0, len(tools))
	for _, current := range tools {
		if info, infoErr := current.Info(ctx); infoErr == nil && info != nil {
			names = append(names, info.Name)
		}
	}
	slices.Sort(names)
	t.Fatalf("memory Tool not found in %v", names)
	return nil
}

func sourceRecoveryConversationScope(t *testing.T, userID uuid.UUID) TaskScope {
	t.Helper()
	scope, err := NewTaskScope(TaskScopeConfig{
		UserID: userID, Role: auth.RoleAnalyst, TaskType: TaskTypeConversation,
		AllowedCapabilities: []ToolCapability{ToolCapabilityMemory},
	})
	if err != nil {
		t.Fatalf("NewTaskScope(): %v", err)
	}
	return scope
}
