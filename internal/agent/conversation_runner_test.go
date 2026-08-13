package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/contextgovernance"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/conversationmemory"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type conversationRunnerModelState struct {
	mu                         sync.Mutex
	generateCalls              int
	streamCalls                int
	createIfAvailable          bool
	repeatCreate               bool
	searchKnowledgeIfAvailable bool
	omitKnowledgeCitation      bool
	finalContent               string
	schemas                    [][]string
	inputs                     [][]string
	firstDeadlineRemaining     time.Duration
}

type conversationRunnerTestModel struct {
	state *conversationRunnerModelState
	tools []*schema.ToolInfo
}

type failingConversationTokenBudgetPlanner struct {
	waitForCancellation bool
}

type conversationExactRuneCounter struct{}

type conversationMemoryStub struct {
	active          *conversationmemory.Snapshot
	prepared        conversationmemory.Snapshot
	prepareErr      error
	prepareRequests []conversationmemory.PrepareActiveRequest
}

func (s *conversationMemoryStub) Active(context.Context, uuid.UUID) (*conversationmemory.Snapshot, error) {
	if s.active == nil {
		return nil, conversationmemory.ErrSnapshotNotFound
	}
	copy := *s.active
	return &copy, nil
}

func (s *conversationMemoryStub) PrepareActive(
	ctx context.Context,
	request conversationmemory.PrepareActiveRequest,
) (conversationmemory.Snapshot, error) {
	s.prepareRequests = append(s.prepareRequests, request)
	if s.prepareErr != nil {
		return conversationmemory.Snapshot{}, s.prepareErr
	}
	prepared := s.prepared
	if request.ActivationGate != nil {
		candidate := prepared
		candidate.Status = conversationmemory.SnapshotStatusCandidate
		candidate.ActivatedAt = nil
		if err := request.ActivationGate.ValidateForActivation(ctx, candidate); err != nil {
			return conversationmemory.Snapshot{}, err
		}
	}
	s.active = &prepared
	return prepared, nil
}

func (conversationExactRuneCounter) CountTokens(
	_ context.Context,
	input contextgovernance.PromptInput,
) (int, error) {
	total := 0
	for _, segment := range input.Segments {
		total += len([]rune(segment.Content))
	}
	return total, nil
}

func (p failingConversationTokenBudgetPlanner) Plan(
	ctx context.Context,
	_ contextgovernance.TokenBudgetRequest,
) (contextgovernance.TokenBudgetPlan, error) {
	if p.waitForCancellation {
		<-ctx.Done()
	}
	return contextgovernance.TokenBudgetPlan{}, errors.New("fixture token estimate failed")
}

func (m *conversationRunnerTestModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &conversationRunnerTestModel{state: m.state, tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

func (m *conversationRunnerTestModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
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
	m.state.generateCalls++
	if len(m.state.inputs) == 0 {
		if deadline, ok := ctx.Deadline(); ok {
			m.state.firstDeadlineRemaining = time.Until(deadline)
		}
	}
	m.state.schemas = append(m.state.schemas, names)
	inputSnapshot := make([]string, 0, len(input))
	for _, message := range input {
		inputSnapshot = append(inputSnapshot, string(message.Role)+"\x00"+message.ToolName+"\x00"+message.Content)
	}
	m.state.inputs = append(m.state.inputs, inputSnapshot)
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

func TestConversationRunnerShadowPreflightRecordsManifestWithoutChangingModelInput(t *testing.T) {
	baselineState := &conversationRunnerModelState{}
	baseline := newConversationRunnerTest(t, baselineState, &diagnosisToolCreatorStub{})
	references := []conversation.CaseReference{{
		ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected,
	}}
	request, baselineCtx := conversationRunnerRequest(references)
	baselineResponse, err := baseline.Respond(baselineCtx, request)
	if err != nil {
		t.Fatalf("baseline Respond(): %v", err)
	}

	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethodLocalCalibrated, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := contextgovernance.NewContinuousTailSelector(estimator)
	if err != nil {
		t.Fatal(err)
	}
	shadowState := &conversationRunnerModelState{}
	shadow := newConversationRunnerTestWithPreflight(t, shadowState, &diagnosisToolCreatorStub{},
		ConversationContextPreflightConfig{
			Enabled: true, Planner: planner, TailSelector: selector,
			ContinuousTailEnabled: true, TailMaxRatio: 0.15,
			ModelProfile: contextgovernance.ModelProfile{
				Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
				ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
			},
			SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
			ToolGrowthReserveTokens: 256,
		},
	)
	shadowRequest, shadowCtx := conversationRunnerRequest(references)
	shadowRequest.Conversation.ID = request.Conversation.ID
	shadowRequest.Conversation.UserID = request.Conversation.UserID
	shadowRequest.UserMessage = request.UserMessage
	shadowRequest.History = request.History
	shadowCtx = conversation.WithCommandContext(shadowCtx, conversation.CommandContext{
		ConversationID: request.Conversation.ID,
		UserMessageID:  request.UserMessage.ID,
		Actor:          conversation.Actor{UserID: request.Conversation.UserID},
	})
	shadowResponse, err := shadow.Respond(shadowCtx, shadowRequest)
	if err != nil {
		t.Fatalf("shadow Respond(): %v", err)
	}
	manifest := shadowResponse.RunObservation.PromptManifest
	if manifest == nil || manifest.Validate() != nil || manifest.ActualPromptTokens != 10 ||
		manifest.CompletionTokens != 2 || manifest.TailFromSeq != 1 || manifest.TailThroughSeq != 1 ||
		manifest.EstimatedPromptTokens < 1 || manifest.ToolGrowthReserveTokens != 256 ||
		manifest.PromptEpochID == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if baselineResponse.Content != shadowResponse.Content ||
		!slices.Equal(baselineState.schemas[0], shadowState.schemas[0]) ||
		!slices.Equal(baselineState.inputs[0], shadowState.inputs[0]) {
		t.Fatalf("shadow changed model input: baseline schemas/input=%v/%v shadow=%v/%v",
			baselineState.schemas, baselineState.inputs, shadowState.schemas, shadowState.inputs)
	}
}

func TestConversationRunnerHardThresholdActivatesSummaryAndUsesSummaryTail(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 8192, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 3
	request.UserMessage.Content = "现在给出结论"
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: strings.Repeat("旧", 7000)},
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "上轮回答"},
		request.UserMessage,
	}
	reportReferenceID := "report:" + uuid.NewString()
	request.History[0].ReportReferences = []conversation.ReportReference{{ReferenceID: reportReferenceID}}
	memory := &conversationMemoryStub{prepared: conversationActiveSummaryFixture(t, request.Conversation.ID, 2)}
	preflight.SummaryTailEnabled = true
	preflight.Memory = memory
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	state.mu.Lock()
	inputs := append([][]string(nil), state.inputs...)
	state.mu.Unlock()
	if len(inputs) != 1 || len(inputs[0]) < 3 || !strings.Contains(inputs[0][1], "conversation_memory") ||
		!strings.HasPrefix(inputs[0][1], string(schema.User)+"\x00\x00") ||
		!strings.Contains(inputs[0][1], "保留用户已经确认的目标") ||
		strings.Contains(strings.Join(inputs[0], "\n"), strings.Repeat("旧", 100)) ||
		!strings.Contains(inputs[0][len(inputs[0])-1], "现在给出结论") {
		t.Fatalf("summary-tail model input = %v", inputs)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || manifest.SummarySnapshotID != memory.prepared.ID.String() ||
		!manifest.HardCompactionTriggered || manifest.SummaryFingerprint == contextgovernance.SHA256Hex("") ||
		manifest.TailFromSeq != 2 || manifest.TailThroughSeq != 3 || manifest.ExceedsHardWindow {
		t.Fatalf("summary-tail manifest = %+v", manifest)
	}
	if len(memory.prepareRequests) != 1 || len(memory.prepareRequests[0].CompletedMessages) != 2 ||
		memory.prepareRequests[0].CompletedMessages[1].Seq != 2 ||
		len(memory.prepareRequests[0].CompletedMessages[0].ReportReferences) != 1 ||
		memory.prepareRequests[0].CompletedMessages[0].ReportReferences[0].ReferenceID != reportReferenceID {
		t.Fatalf("hard-compaction completed messages = %+v", memory.prepareRequests)
	}
	for _, message := range memory.prepareRequests[0].CompletedMessages {
		if message.ID == request.UserMessage.ID || message.Seq >= request.UserMessage.Seq {
			t.Fatalf("current message leaked into hard compaction input: %+v", message)
		}
	}
}

func TestConversationRunnerKeepsFullContinuousHistoryBeforeFirstHardCompaction(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 8192, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 3
	oldBody := "first-uncompressed-message-" + strings.Repeat("旧", 2000)
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: oldBody},
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "recent history"},
		request.UserMessage,
	}
	memory := &conversationMemoryStub{}
	preflight.SummaryTailEnabled = true
	preflight.Memory = memory
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond() before first hard compaction error = %v", err)
	}
	state.mu.Lock()
	modelInput := strings.Join(state.inputs[0], "\n")
	state.mu.Unlock()
	manifest := response.RunObservation.PromptManifest
	if !strings.Contains(modelInput, oldBody) || len(memory.prepareRequests) != 0 || manifest == nil ||
		manifest.TailFromSeq != 1 || manifest.SummarySnapshotID != "" || manifest.HardCompactionTriggered {
		t.Fatalf("pre-hard input/requests/manifest = %t/%d/%+v", strings.Contains(modelInput, oldBody), len(memory.prepareRequests), manifest)
	}
}

func TestConversationRunnerRefreshesActiveSummaryBeforeTailWouldCreateCoverageGap(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 8192, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 3
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: "covered"},
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: strings.Repeat("增量", 900)},
		request.UserMessage,
	}
	active := conversationActiveSummaryFixture(t, request.Conversation.ID, 1)
	prepared := conversationActiveSummaryFixture(t, request.Conversation.ID, 2)
	memory := &conversationMemoryStub{active: &active, prepared: prepared}
	preflight.SummaryTailEnabled = true
	preflight.Memory = memory
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond() coverage refresh error = %v", err)
	}
	manifest := response.RunObservation.PromptManifest
	if len(memory.prepareRequests) != 1 || manifest == nil || manifest.SummarySnapshotID != prepared.ID.String() ||
		manifest.HardCompactionTriggered || manifest.TailFromSeq > prepared.ThroughSeq+1 {
		t.Fatalf("coverage refresh requests/manifest = %d/%+v", len(memory.prepareRequests), manifest)
	}
}

func TestConversationRunnerFailsClosedWhenPreviousActiveCannotCoverTailGapAfterRefreshFailure(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 8192, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 3
	request.UserMessage.Content = "继续回答"
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: "已经被摘要的目标"},
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: strings.Repeat("新", 7000)},
		request.UserMessage,
	}
	active := conversationActiveSummaryFixture(t, request.Conversation.ID, 1)
	memory := &conversationMemoryStub{active: &active, prepareErr: conversationmemory.ErrCompactionFailed}
	preflight.SummaryTailEnabled = true
	preflight.Memory = memory
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationContextPreparationFailed) {
		t.Fatalf("Respond() with uncovered old Active error = %v, want context preparation failure", err)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || manifest.SummarySnapshotID != active.ID.String() ||
		!manifest.HardCompactionTriggered || !manifest.ContextDegraded ||
		!slices.Contains(manifest.DegradedReasons, "summary_refresh_failed") ||
		!slices.Contains(manifest.DegradedReasons, "summary_tail_coverage_gap") {
		t.Fatalf("uncovered old-Active manifest = %+v", manifest)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
}

func TestConversationRunnerHardCompactionFailureWithoutActiveSummaryIsRetryable(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 8192, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 2
	request.UserMessage.Content = "当前问题"
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: strings.Repeat("旧", 7000)},
		request.UserMessage,
	}
	preflight.SummaryTailEnabled = true
	preflight.Memory = &conversationMemoryStub{prepareErr: conversationmemory.ErrCompactionFailed}
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationContextPreparationFailed) {
		t.Fatalf("Respond() error = %v, want context preparation failure", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || response.RunObservation == nil || failure.ErrorType != "context_preparation_failed" ||
		failure.Observation.PromptManifest == nil ||
		!failure.Observation.PromptManifest.HardCompactionTriggered ||
		!slices.Contains(failure.Observation.PromptManifest.DegradedReasons, "summary_compaction_failed") {
		t.Fatalf("retryable hard-compaction failure response=%+v record=%+v present=%v", response, failure, ok)
	}
}

func TestConversationRunnerDoesNotActivateGeneratedSummaryThatExceedsMainProfileBudget(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 3
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: strings.Repeat("旧", 7000)},
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: "历史回答"},
		request.UserMessage,
	}
	prepared := conversationActiveSummaryFixture(t, request.Conversation.ID, 2)
	prepared.Payload.Facts = append(prepared.Payload.Facts, conversationmemory.Entry{
		EntryID: "fact_oversized", Content: strings.Repeat("摘要", 300),
		SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive,
	})
	prepared = rebuildConversationActiveSummaryFixture(t, prepared)
	memory := &conversationMemoryStub{prepared: prepared}
	preflight.SummaryTailEnabled = true
	preflight.Memory = memory
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	_, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationContextPreparationFailed) || memory.active != nil {
		t.Fatalf("Respond() error/active = %v/%+v, want context failure/no activation", err, memory.active)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
}

func TestConversationRunnerOldSummaryFallbackStillFailsClosedWhenCurrentMessageExceedsWindow(t *testing.T) {
	state := &conversationRunnerModelState{}
	profile := contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 512,
	}
	preflight := newConversationContinuousPreflightForTest(t, profile, 0.15, 128)
	request, ctx := conversationRunnerRequest(nil)
	request.UserMessage.Seq = 2
	request.UserMessage.Content = strings.Repeat("当前问题", 2000)
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: request.Conversation.ID, Seq: 1, Role: conversation.MessageRoleUser, Content: "已摘要的旧目标"},
		request.UserMessage,
	}
	active := conversationActiveSummaryFixture(t, request.Conversation.ID, 1)
	preflight.SummaryTailEnabled = true
	preflight.Memory = &conversationMemoryStub{active: &active, prepareErr: conversationmemory.ErrCompactionFailed}
	preflight.MemoryMaxRatio = 0.20
	preflight.SummaryMaxRatio = 0.05
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)

	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationContextPreparationFailed) {
		t.Fatalf("Respond() error = %v, want context preparation failure", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || response.RunObservation == nil || failure.Observation.PromptManifest == nil ||
		!failure.Observation.PromptManifest.ExceedsHardWindow ||
		!slices.Contains(failure.Observation.PromptManifest.DegradedReasons, "summary_refresh_failed") {
		t.Fatalf("unsafe fallback manifest=%+v response manifest=%+v present=%v",
			failure.Observation.PromptManifest, response.RunObservation.PromptManifest, ok)
	}
}

func conversationActiveSummaryFixture(t *testing.T, conversationID uuid.UUID, throughSeq int64) conversationmemory.Snapshot {
	t.Helper()
	payload := conversationmemory.Payload{
		ConversationGoal: &conversationmemory.Entry{
			EntryID: "goal_context", Content: "保留用户已经确认的目标",
			SourceMessageSeqs: []int64{1}, Status: conversationmemory.EntryStatusActive,
		},
		Facts: []conversationmemory.Entry{}, Decisions: []conversationmemory.Entry{},
		Corrections: []conversationmemory.Entry{}, EvidenceReferences: []conversationmemory.ReferenceEntry{},
		OpenQuestions: []conversationmemory.Entry{}, Todos: []conversationmemory.Entry{},
		TaskReferences: []conversationmemory.ReferenceEntry{}, ReportReferences: []conversationmemory.ReferenceEntry{},
	}
	createdAt := time.Now().Add(-time.Minute).UTC()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: uuid.New(), ConversationID: conversationID, FromSeq: 1, ThroughSeq: throughSeq,
		SchemaVersion: conversationmemory.CurrentSchemaVersion,
		Provenance: conversationmemory.SummaryProvenance{
			ModelProfile: "conversation-memory", ModelProvider: "dashscope",
			ModelID: "qwen3.6-flash", PromptVersion: "conversation-memory-v1",
		},
		Payload:   payload,
		Usage:     conversationmemory.SummaryUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("NewCandidateSnapshot() error = %v", err)
	}
	activatedAt := createdAt.Add(time.Second)
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = &activatedAt
	snapshot := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: 1}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("active summary fixture error = %v", err)
	}
	return snapshot
}

func rebuildConversationActiveSummaryFixture(t *testing.T, source conversationmemory.Snapshot) conversationmemory.Snapshot {
	t.Helper()
	candidate, err := conversationmemory.NewCandidateSnapshot(conversationmemory.NewCandidateSnapshotInput{
		ID: source.ID, ConversationID: source.ConversationID,
		SupersedesSnapshotID: source.SupersedesSnapshotID,
		FromSeq:              source.FromSeq, ThroughSeq: source.ThroughSeq,
		SchemaVersion: source.SchemaVersion, Provenance: source.Provenance,
		Payload: source.Payload, Usage: source.Usage, CreatedAt: source.CreatedAt,
	})
	if err != nil {
		t.Fatalf("rebuild active Summary candidate error = %v", err)
	}
	candidate.Status = conversationmemory.SnapshotStatusActive
	candidate.ActivatedAt = source.ActivatedAt
	result := conversationmemory.Snapshot{CandidateSnapshot: candidate, Version: source.Version}
	if err := result.Validate(); err != nil {
		t.Fatalf("rebuild active Summary error = %v", err)
	}
	return result
}

func TestConversationRunnerContinuousTokenTailStopsAtFirstOversizedMessage(t *testing.T) {
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		newConversationContinuousPreflightForTest(t, contextgovernance.ModelProfile{
			Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
			ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
		}, 0.012, 256),
	)
	request, ctx := conversationRunnerRequest(nil)
	conversationID := request.Conversation.ID
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "older"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: strings.Repeat("中", 80)},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 3, Role: conversation.MessageRoleAssistant, Content: "recent"},
	}
	request.UserMessage.Seq = 4
	request.UserMessage.Content = "now"
	request.History = append(request.History, request.UserMessage)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	state.mu.Lock()
	inputs := append([][]string(nil), state.inputs...)
	state.mu.Unlock()
	want := []string{
		string(schema.System) + "\x00\x00conversation test instruction",
		string(schema.Assistant) + "\x00\x00recent",
		string(schema.User) + "\x00\x00now",
	}
	if len(inputs) != 1 || !slices.Equal(inputs[0], want) {
		t.Fatalf("model inputs = %v, want %v", inputs, want)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || manifest.TailFromSeq != 3 || manifest.TailThroughSeq != 4 ||
		slices.Contains(manifest.DegradedReasons, "non_continuous_tail") {
		t.Fatalf("continuous-tail manifest = %+v", manifest)
	}
}

func TestConversationRunnerHardWindowEnforcementBlocksFullHistoryWithoutTail(t *testing.T) {
	state := &conversationRunnerModelState{}
	preflight := newConversationContinuousPreflightForTest(t, contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 1024, MaxOutputTokens: 128, SafetyMarginTokens: 128,
	}, 0.15, 128)
	preflight.ContinuousTailEnabled = false
	preflight.TailSelector = nil
	preflight.HardWindowEnforced = true
	preflight.FullHistoryEnabled = true
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{}, preflight)
	request, ctx := conversationRunnerRequest(nil)
	conversationID := request.Conversation.ID
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: strings.Repeat("中", 900)},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: strings.Repeat("中", 900)},
	}
	request.UserMessage.Seq = 3
	request.UserMessage.Content = "现在给出结论"

	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationPromptWindowExceeded) {
		t.Fatalf("Respond() error = %v, want hard-window block", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || response.RunObservation == nil || failure.Observation.PromptManifest == nil ||
		!failure.Observation.PromptManifest.ExceedsHardWindow {
		t.Fatalf("failure observation = %+v response=%+v present=%v", failure, response.RunObservation, ok)
	}
}

func TestConversationRunnerContinuousTailCountsBoundedAttachmentReferencesAndKeepsCurrentMessage(t *testing.T) {
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		newConversationContinuousPreflightForTest(t, contextgovernance.ModelProfile{
			Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
			ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
		}, 0.01, 256),
	)
	request, ctx := conversationRunnerRequest(nil)
	conversationID := request.Conversation.ID
	request.History = []conversation.Message{{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleAssistant, Content: "previous",
	}}
	request.UserMessage.Seq = 2
	request.UserMessage.Content = "inspect"
	attachmentID := uuid.New()
	caseID := uuid.New()
	taskID := uuid.New()
	request.UserMessage.CaseReferences = []conversation.CaseReference{{
		ExternalCaseID: caseID, Kind: conversation.ReferenceKindSelected,
	}}
	request.UserMessage.TaskReferences = []conversation.TaskReference{{
		TaskID: taskID, Kind: conversation.ReferenceKindCreated,
	}}
	request.UserMessage.Attachments = []conversation.MessageAttachment{{
		AttachmentID: attachmentID, Position: 0, Purpose: "diagnosis-context",
		OriginalName: strings.Repeat("a", 80) + ".pdf", MediaType: "application/pdf",
		SizeBytes: 1024, ContentSHA256: strings.Repeat("a", 64), Status: "uploaded",
	}}
	request.History = append(request.History, request.UserMessage)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	state.mu.Lock()
	inputs := append([][]string(nil), state.inputs...)
	state.mu.Unlock()
	if len(inputs) != 1 || len(inputs[0]) != 2 || strings.Contains(inputs[0][1], "previous") ||
		!strings.Contains(inputs[0][1], "attachment id="+attachmentID.String()) ||
		!strings.Contains(inputs[0][1], "case id="+caseID.String()) ||
		!strings.Contains(inputs[0][1], "task id="+taskID.String()) ||
		!strings.Contains(inputs[0][1], "inspect") {
		t.Fatalf("attachment-aware model input = %v", inputs)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || manifest.TailFromSeq != 2 || manifest.TailThroughSeq != 2 {
		t.Fatalf("attachment-aware manifest = %+v", manifest)
	}
}

func TestConversationRunnerContinuousTailFlagOffUsesRuneCompatibilityPath(t *testing.T) {
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethodLocalExact, conversationExactRuneCounter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		t.Fatal(err)
	}
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		ConversationContextPreflightConfig{
			Enabled: true, Planner: planner,
			ModelProfile: contextgovernance.ModelProfile{
				Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
				ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
			},
			SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
			ToolGrowthReserveTokens: 256, PreflightTimeout: 100 * time.Millisecond,
		},
	)
	runner.maxContextRunes = 20
	request, ctx := conversationRunnerRequest(nil)
	conversationID := request.Conversation.ID
	request.History = []conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "older"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleAssistant, Content: strings.Repeat("中", 30)},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 3, Role: conversation.MessageRoleAssistant, Content: "recent"},
	}
	request.UserMessage.Seq = 4
	request.UserMessage.Content = "now"
	request.History = append(request.History, request.UserMessage)

	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	state.mu.Lock()
	inputs := append([][]string(nil), state.inputs...)
	state.mu.Unlock()
	if len(inputs) != 1 || len(inputs[0]) != 4 || !strings.Contains(inputs[0][1], "older") ||
		!strings.Contains(inputs[0][2], "recent") || !strings.Contains(inputs[0][3], "now") {
		t.Fatalf("Rune compatibility input = %v", inputs)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || !slices.Contains(manifest.DegradedReasons, "non_continuous_tail") {
		t.Fatalf("Rune compatibility manifest = %+v", manifest)
	}
}

func TestConversationRunnerContinuousTailBlocksPromptAboveHardWindowBeforeProvider(t *testing.T) {
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		newConversationContinuousPreflightForTest(t, contextgovernance.ModelProfile{
			Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
			ContextWindowTokens: 90, MaxOutputTokens: 20, SafetyMarginTokens: 10,
		}, 0.15, 32),
	)
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationPromptWindowExceeded) {
		t.Fatalf("Respond() error = %v, want hard-window rejection", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || response.RunObservation == nil || failure.ErrorType != "prompt_window_exceeded" ||
		failure.Observation.PromptManifest == nil ||
		!failure.Observation.PromptManifest.ExceedsHardWindow ||
		failure.Observation.PromptManifest.ActualUsageAvailable {
		t.Fatalf("hard-window failure response=%+v record=%+v present=%v", response, failure, ok)
	}
}

func TestConversationRunnerShadowPreflightFailureIsBoundedObservableAndDoesNotCancelModel(t *testing.T) {
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		ConversationContextPreflightConfig{
			Enabled: true,
			Planner: failingConversationTokenBudgetPlanner{waitForCancellation: true},
			ModelProfile: contextgovernance.ModelProfile{
				Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
				ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
			},
			SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
			ToolGrowthReserveTokens: 256,
			PreflightTimeout:        10 * time.Millisecond,
		},
	)
	core, logs := observer.New(zap.WarnLevel)
	runner.log = zap.New(core)
	runner.timeout = time.Second
	request, ctx := conversationRunnerRequest(nil)

	startedAt := time.Now()
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond(): %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= runner.timeout {
		t.Fatalf("shadow preflight consumed model timeout: elapsed=%s timeout=%s", elapsed, runner.timeout)
	}
	state.mu.Lock()
	modelDeadlineRemaining := state.firstDeadlineRemaining
	state.mu.Unlock()
	if modelDeadlineRemaining < 900*time.Millisecond {
		t.Fatalf("model timeout was not reset after preflight: remaining=%s", modelDeadlineRemaining)
	}
	manifest := response.RunObservation.PromptManifest
	if manifest == nil || manifest.Validate() != nil || !manifest.ContextDegraded ||
		manifest.PreflightStatus != contextgovernance.PreflightStatusFailed ||
		!manifest.PromptIdentityAvailable || manifest.EstimateAvailable ||
		!slices.Contains(manifest.DegradedReasons, "preflight_failed") ||
		!slices.Contains(manifest.DegradedReasons, "token_estimation_failed") {
		t.Fatalf("failure manifest = %+v", manifest)
	}
	entries := logs.FilterMessage("conversation prompt preflight failed").All()
	if len(entries) != 1 {
		t.Fatalf("preflight failure logs = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	for key, want := range map[string]any{
		"user_id": request.Conversation.UserID.String(), "conversation_id": request.Conversation.ID.String(),
		"message_id": request.UserMessage.ID.String(), "model_profile": "fixture-main",
		"service_role": "conversation_agent",
	} {
		if fields[key] != want {
			t.Fatalf("log field %s = %v, want %v; fields=%v", key, fields[key], want, fields)
		}
	}
}

func TestConversationRunnerContinuousTailFailsClosedWhenPreflightIsUnavailable(t *testing.T) {
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethodLocalExact, conversationExactRuneCounter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := contextgovernance.NewContinuousTailSelector(estimator)
	if err != nil {
		t.Fatal(err)
	}
	state := &conversationRunnerModelState{}
	runner := newConversationRunnerTestWithPreflight(t, state, &diagnosisToolCreatorStub{},
		ConversationContextPreflightConfig{
			Enabled: true, Planner: failingConversationTokenBudgetPlanner{}, TailSelector: selector,
			ContinuousTailEnabled: true, TailMaxRatio: 0.15,
			ModelProfile: contextgovernance.ModelProfile{
				Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
				ContextWindowTokens: 4096, MaxOutputTokens: 512, SafetyMarginTokens: 256,
			},
			SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
			ToolGrowthReserveTokens: 256, PreflightTimeout: 100 * time.Millisecond,
		},
	)
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationContextPreparationFailed) {
		t.Fatalf("Respond() error = %v, want context preparation failure", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || failure.ErrorType != "context_preparation_failed" ||
		response.RunObservation == nil || response.RunObservation.PromptManifest == nil ||
		response.RunObservation.PromptManifest.PreflightStatus != contextgovernance.PreflightStatusFailed {
		t.Fatalf("preflight failure response=%+v record=%+v present=%v", response, failure, ok)
	}
}

func TestConversationRunnerGuardsEveryReActModelCallAfterToolGrowth(t *testing.T) {
	content := strings.Repeat("数据库连接池诊断证据。", 400)
	queryPlan, err := knowledge.OriginalQueryPlan("连接池超时")
	if err != nil {
		t.Fatal(err)
	}
	knowledgeTool, err := NewSearchKnowledgeTool(&knowledgeSearcherStub{result: knowledge.HybridSearch{
		Results: []knowledge.SearchResult{{
			DocumentID: uuid.New(), DocumentVersionID: uuid.New(), ChunkID: uuid.New(),
			Title: "大型运行手册", Scope: knowledge.ScopeGlobal, ElementType: knowledge.ElementText,
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
	preflight := newConversationContinuousPreflightForTest(t, contextgovernance.ModelProfile{
		Name: "fixture-main", Provider: "fixture", ModelID: "fixture-v1",
		ContextWindowTokens: 3000, MaxOutputTokens: 128, SafetyMarginTokens: 128,
	}, 0.15, 64)
	state := &conversationRunnerModelState{searchKnowledgeIfAvailable: true}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: state}, ToolCatalog: catalog,
		SystemInstruction: "conversation runtime window guard test",
		ModelProvider:     "fixture", ModelID: "fixture-v1", PromptVersion: "conversation-test-v1",
		AvailableDependencies: []ToolDependency{ToolDependencyKnowledge}, Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes, ContextPreflight: preflight,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrConversationPromptWindowExceeded) {
		t.Fatalf("Respond() error = %v, want ReAct hard-window rejection", err)
	}
	state.mu.Lock()
	providerCalls := len(state.inputs)
	state.mu.Unlock()
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want only the pre-Tool call", providerCalls)
	}
	failure, ok := conversation.AgentRunFailureRecordFrom(err)
	if !ok || failure.ErrorType != "prompt_window_exceeded" || response.RunObservation == nil ||
		response.RunObservation.PromptManifest == nil ||
		!response.RunObservation.PromptManifest.ExceedsHardWindow ||
		response.RunObservation.PromptManifest.ActualUsageAvailable ||
		!slices.Contains(response.RunObservation.PromptManifest.DegradedReasons, "react_prompt_blocked") {
		t.Fatalf("runtime guard response=%+v record=%+v present=%v", response, failure, ok)
	}
}

func TestConversationToolResultTruncationIncludesStableHashHandle(t *testing.T) {
	original := strings.Repeat("x", 2048)
	store := newConversationToolResultStore(8, 1024)
	ctx := withConversationToolResultStore(context.Background(), store)
	endpoint := newConversationToolTraceMiddleware(1024).Invokable(
		func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
			return &compose.ToolOutput{Result: original}, nil
		},
	)
	output, err := endpoint(ctx, &compose.ToolInput{Name: "fixture_tool", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	wantRef := "ref=sha256:" + knowledge.SHA256Hex(original)
	if len(output.Result) > 1024 || !strings.Contains(output.Result, wantRef) ||
		!strings.Contains(output.Result, "original_bytes=2048") ||
		!strings.Contains(output.Result, ToolReadConversationToolResult) {
		t.Fatalf("bounded Tool result = %q", output.Result)
	}
	reader, err := NewReadConversationToolResultTool()
	if err != nil {
		t.Fatal(err)
	}
	rawChunk, err := reader.InvokableRun(ctx, `{"ref":"sha256:`+knowledge.SHA256Hex(original)+`","maxBytes":64}`)
	if err != nil {
		t.Fatal(err)
	}
	var chunk conversationToolResultChunk
	if json.Unmarshal([]byte(rawChunk), &chunk) != nil || chunk.Ref != "sha256:"+knowledge.SHA256Hex(original) ||
		chunk.Content != strings.Repeat("x", 64) || chunk.NextOffsetBytes != 64 || !chunk.Truncated {
		t.Fatalf("resolved Tool result chunk = %s", rawChunk)
	}
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
	m.state.mu.Lock()
	m.state.streamCalls++
	m.state.mu.Unlock()
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestConversationRunnerUsesStreamingOnlyWhenExplicitlyEnabled(t *testing.T) {
	state := &conversationRunnerModelState{}
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: state}, ToolCatalog: catalog,
		SystemInstruction: "streaming observer test", ModelProvider: "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes, EnableStreaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	if _, err := runner.Respond(ctx, request); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	streamCalls, generateCalls := state.streamCalls, state.generateCalls
	state.mu.Unlock()
	if streamCalls != 1 || generateCalls != 1 {
		t.Fatalf("stream/generate calls = %d/%d, want 1/1 through the stream-backed fixture", streamCalls, generateCalls)
	}
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
			if !slices.Contains(state.schemas[0], ToolReadConversationToolResult) {
				t.Fatalf("bounded Tool result reader is absent from the frozen conversation schema: %v", state.schemas[0])
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
		CitationRepairer: repairer, CitationRepairPolicy: resilience.PolicyRepairThenFail,
		ToolCatalog: catalog, SystemInstruction: "conversation citation repair test",
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

func TestConversationRunnerRequiresCitationRepairPolicy(t *testing.T) {
	catalog, err := NewDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewConversationRunner(ConversationRunnerConfig{
		ChatModel:        &conversationRunnerTestModel{state: &conversationRunnerModelState{}},
		CitationRepairer: &conversationCitationRepairerStub{}, ToolCatalog: catalog,
		SystemInstruction: "test", ModelProvider: "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
	})
	if err == nil {
		t.Fatal("NewConversationRunner accepted citation repair without repair_then_fail policy")
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
	projection := buildRuneConversationPromptProjection([]conversation.Message{
		{ID: uuid.New(), ConversationID: conversationID, Seq: 1, Role: conversation.MessageRoleUser, Content: "这是一条应被预算舍弃的很长旧消息"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 2, Role: conversation.MessageRoleSystem, Content: "不可信系统消息"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 3, Role: conversation.MessageRoleAssistant, Content: "上轮回答"},
		{ID: uuid.New(), ConversationID: conversationID, Seq: 4, Role: conversation.MessageRoleTool, Content: "原始工具结果"},
		current,
	}, current, 8)
	messages := projection.messages

	if len(messages) != 2 || messages[0].Role != schema.Assistant || messages[0].Content != "上轮回答" ||
		messages[1].Role != schema.User || messages[1].Content != "当前问题" {
		t.Fatalf("model messages = %+v", messages)
	}
	if projection.tailContinuous {
		t.Fatal("Rune compatibility projection hid its non-continuous tail")
	}
}

func newConversationRunnerTest(
	t *testing.T,
	state *conversationRunnerModelState,
	creator DiagnosisTaskCreator,
) *ConversationRunner {
	return newConversationRunnerTestWithPreflight(t, state, creator, ConversationContextPreflightConfig{})
}

func newConversationRunnerTestWithPreflight(
	t *testing.T,
	state *conversationRunnerModelState,
	creator DiagnosisTaskCreator,
	preflight ConversationContextPreflightConfig,
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
		ContextPreflight:      preflight,
	})
	if err != nil {
		t.Fatalf("NewConversationRunner(): %v", err)
	}
	return runner
}

func newConversationContinuousPreflightForTest(
	t *testing.T,
	profile contextgovernance.ModelProfile,
	tailMaxRatio float64,
	toolGrowthReserveTokens int,
) ConversationContextPreflightConfig {
	t.Helper()
	estimator, err := contextgovernance.NewLocalTokenEstimator(
		contextgovernance.EstimationMethodLocalExact, conversationExactRuneCounter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := contextgovernance.NewTokenBudgetPlanner(estimator)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := contextgovernance.NewContinuousTailSelector(estimator)
	if err != nil {
		t.Fatal(err)
	}
	return ConversationContextPreflightConfig{
		Enabled: true, Planner: planner, TailSelector: selector,
		ContinuousTailEnabled: true, TailMaxRatio: tailMaxRatio,
		ModelProfile: profile, SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		ToolGrowthReserveTokens: toolGrowthReserveTokens, PreflightTimeout: 100 * time.Millisecond,
		SyncCompactionTimeout: time.Second,
	}
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

func TestConversationMessageReferencePromptIncludesStructuredReportReference(t *testing.T) {
	referenceID := "report:" + uuid.NewString()
	prompt := conversationMessageReferencePrompt(conversation.Message{
		ReportReferences: []conversation.ReportReference{{ReferenceID: referenceID}},
	})
	if !strings.Contains(prompt, "report id="+referenceID) || !strings.Contains(prompt, "<message_references>") {
		t.Fatalf("report reference prompt = %q", prompt)
	}
}
