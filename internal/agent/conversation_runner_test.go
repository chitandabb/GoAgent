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

	"github.com/chitandabb/GoAgent/internal/agentruntime"
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
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
	readAttachmentIfAvailable  bool
	readTaskStatusIfAvailable  bool
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

type conversationTracingKnowledgeRepository struct {
	result knowledge.SearchResult
}

type rejectingSpanExporter struct{}

func (rejectingSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("fixture exporter rejected spans")
}

func (rejectingSpanExporter) Shutdown(context.Context) error { return nil }

func (r conversationTracingKnowledgeRepository) CreateDocument(context.Context, knowledge.CreateDocumentInput) (knowledge.Document, error) {
	return knowledge.Document{}, errors.New("not implemented in tracing fixture")
}

func (r conversationTracingKnowledgeRepository) PublishVersion(context.Context, knowledge.PublishVersionInput) (knowledge.DocumentVersion, error) {
	return knowledge.DocumentVersion{}, errors.New("not implemented in tracing fixture")
}

func (r conversationTracingKnowledgeRepository) SearchFTS(context.Context, uuid.UUID, string, int) ([]knowledge.SearchResult, error) {
	return []knowledge.SearchResult{r.result}, nil
}

func (r conversationTracingKnowledgeRepository) SearchVector(context.Context, uuid.UUID, uuid.UUID, []float32, int) ([]knowledge.SearchResult, error) {
	return nil, errors.New("vector is disabled in tracing fixture")
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
	hasAttachmentResult := false
	hasStatusResult := false
	knowledgeSourceRef := ""
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == ToolCreateDiagnosisTask {
			hasCreateResult = true
		}
		if message.Role == schema.Tool && message.ToolName == ToolReadAttachment {
			hasAttachmentResult = true
		}
		if message.Role == schema.Tool && message.ToolName == ToolGetDiagnosisTaskStatus {
			hasStatusResult = true
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
	if m.state.readAttachmentIfAvailable && slices.Contains(names, ToolReadAttachment) && !hasAttachmentResult {
		return runnerTestToolCall(ToolReadAttachment,
			`{"attachmentId":"11111111-1111-1111-1111-111111111111"}`), nil
	}
	if m.state.readTaskStatusIfAvailable && slices.Contains(names, ToolGetDiagnosisTaskStatus) && !hasStatusResult {
		return runnerTestToolCall(ToolGetDiagnosisTaskStatus,
			`{"taskId":"11111111-1111-1111-1111-111111111111"}`), nil
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
		!strings.Contains(inputs[0][1], `"attachmentId":"`+attachmentID.String()+`"`) ||
		!strings.Contains(inputs[0][1], `"externalCaseId":"`+caseID.String()+`"`) ||
		!strings.Contains(inputs[0][1], `"taskId":"`+taskID.String()+`"`) ||
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
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

func TestConversationRunnerProducesOneCorrelatedAgentTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	content := "连接池超时应先检查最大连接数和慢查询。"
	searchService, err := knowledge.NewSearchService(
		conversationTracingKnowledgeRepository{result: knowledge.SearchResult{
			DocumentID: uuid.New(), DocumentVersionID: uuid.New(), ChunkID: uuid.New(),
			Title: "连接池运行手册", Scope: knowledge.ScopeGlobal, Ordinal: 1,
			ElementType: knowledge.ElementText, ContentText: content,
			ContentSHA256: knowledge.SHA256Hex(content), Score: 0.9, FTSRank: 1,
		}}, nil, knowledge.EmbeddingProfile{}, 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeTool, err := NewSearchKnowledgeTool(searchService)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, KnowledgeSearch: knowledgeTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &conversationRunnerModelState{searchKnowledgeIfAvailable: true}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: state}, ToolCatalog: catalog,
		SystemInstruction: "tracing fixture", ModelProvider: "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", AvailableDependencies: []ToolDependency{ToolDependencyKnowledge},
		Logger: zap.NewNop(), MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil || response.Content == "" {
		t.Fatalf("Respond() response=%+v error=%v", response, err)
	}

	spans := exporter.GetSpans()
	var root, toolSpan tracetest.SpanStub
	var models, retrievals int
	var degradationSeen bool
	for _, span := range spans {
		switch {
		case span.Name == "agent.conversation":
			root = span
		case strings.HasPrefix(span.Name, "model."):
			models++
		case span.Name == "tool.search_knowledge":
			toolSpan = span
		case span.Name == "retrieval.knowledge_search":
			retrievals++
			for _, event := range span.Events {
				degradationSeen = degradationSeen || event.Name == "mesguard.degradation"
			}
		}
		for _, item := range span.Attributes {
			if strings.Contains(item.Value.Emit(), "连接池超时") || strings.Contains(item.Value.Emit(), content) {
				t.Fatalf("span %s captured raw content in %s", span.Name, item.Key)
			}
		}
	}
	if !root.SpanContext.IsValid() || !toolSpan.SpanContext.IsValid() || models != 2 || retrievals != 1 || !degradationSeen {
		t.Fatalf("unexpected trace spans=%#v models=%d retrievals=%d degradation=%v", spans, models, retrievals, degradationSeen)
	}
	for _, span := range spans {
		if span.SpanContext.TraceID() != root.SpanContext.TraceID() {
			t.Fatalf("span %s escaped root trace", span.Name)
		}
		if strings.HasPrefix(span.Name, "model.") || strings.HasPrefix(span.Name, "tool.") {
			if span.Parent.SpanID() != root.SpanContext.SpanID() {
				t.Fatalf("span %s parent=%s, want root=%s", span.Name, span.Parent.SpanID(), root.SpanContext.SpanID())
			}
		}
		if span.Name == "retrieval.knowledge_search" && span.Parent.SpanID() != toolSpan.SpanContext.SpanID() {
			t.Fatalf("retrieval parent=%s, want tool=%s", span.Parent.SpanID(), toolSpan.SpanContext.SpanID())
		}
	}
}

func TestConversationRunnerAnswerSurvivesTraceExporterFailure(t *testing.T) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(rejectingSpanExporter{}))
	previousProvider := otel.GetTracerProvider()
	previousHandler := otel.GetErrorHandler()
	otel.SetTracerProvider(provider)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetErrorHandler(previousHandler)
	})

	runner := newConversationRunnerTest(
		t, &conversationRunnerModelState{finalContent: "仍然返回业务答案。"}, &diagnosisToolCreatorStub{},
	)
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil || response.Content != "仍然返回业务答案。" {
		t.Fatalf("trace export failure changed response=%+v error=%v", response, err)
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
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

func TestConversationAnswerCacheEligibilityRequiresKnowledgeOnlyToolPath(t *testing.T) {
	if !conversationAnswerCacheEligible([]ToolExecution{
		{Name: ToolSkill, Succeeded: true},
		{Name: ToolReadSkillReference, Succeeded: true},
		{Name: ToolSearchKnowledge, Succeeded: true},
		{Name: ToolReadConversationToolResult, Succeeded: true},
	}) {
		t.Fatal("knowledge-only tool path should be cache eligible")
	}
	for _, executions := range [][]ToolExecution{
		{{Name: ToolWebSearch, Succeeded: true}, {Name: ToolSearchKnowledge, Succeeded: true}},
		{{Name: ToolCreateDiagnosisTask, Succeeded: true}, {Name: ToolSearchKnowledge, Succeeded: true}},
		{{Name: ToolSearchKnowledge, Succeeded: false}},
		{{Name: ToolSkill, Succeeded: true}},
	} {
		if conversationAnswerCacheEligible(executions) {
			t.Fatalf("unsafe tool path accepted: %+v", executions)
		}
	}
}

func TestConversationRunnerKeepsStableSchemaAcrossCaseReferences(t *testing.T) {
	tests := []struct {
		name       string
		references []conversation.CaseReference
	}{
		{name: "no selected case"},
		{
			name: "one selected case",
			references: []conversation.CaseReference{{
				ExternalCaseID: runnerTestCaseID, Kind: conversation.ReferenceKindSelected,
			}},
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
			// 固定 Conversation Profile：消息引用变化绝不改变模型可见 Schema。
			wantStable := []string{
				ToolCreateDiagnosisTask, ToolGetDiagnosisTaskStatus,
				ToolReadConversationToolResult, ToolReadExternalCase,
			}
			slices.Sort(wantStable)
			got := append([]string(nil), state.schemas[0]...)
			slices.Sort(got)
			if !slices.Equal(got, wantStable) {
				t.Fatalf("conversation schema = %v, want stable %v", state.schemas[0], wantStable)
			}
		})
	}
}

func TestConversationRunnerKeepsStableSchemaForTaskReferences(t *testing.T) {
	for _, test := range []struct {
		name       string
		references []conversation.TaskReference
	}{
		{name: "no task reference"},
		{
			name: "referenced task",
			references: []conversation.TaskReference{{
				TaskID: uuid.New(), Kind: conversation.ReferenceKindReferenced,
			}},
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
			if !slices.Contains(state.schemas[0], ToolGetDiagnosisTaskStatus) {
				t.Fatalf("task status Tool must stay visible with the stable profile: %v", state.schemas[0])
			}
		})
	}
}

func TestConversationRunnerRejectsTaskCreationWithoutCaseReference(t *testing.T) {
	state := &conversationRunnerModelState{createIfAvailable: true}
	creator := &diagnosisToolCreatorStub{result: conversation.CreateDiagnosisResult{
		Task: diagnosis.DiagnosisTask{ID: uuid.New(), Status: diagnosis.TaskPending},
	}}
	runner := newConversationRunnerTest(t, state, creator)
	// 无 selected case：create_diagnosis_task 仍出现在 Schema 中，
	// 但执行必须被 RunAccess（缺少 diagnosis.create）拒绝。
	request, ctx := conversationRunnerRequest(nil)
	_, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("Respond() error = %v, want ErrToolNotAllowed", err)
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0", creator.calls)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.schemas) == 0 || !slices.Contains(state.schemas[0], ToolCreateDiagnosisTask) {
		t.Fatalf("create_diagnosis_task missing from stable schema: %v", state.schemas)
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
	// 调用次数耗尽绝不能删除下一轮模型上下文中的 Tool Schema：
	// 两次模型调用必须看到完全相同的工具名单。
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.schemas) < 2 {
		t.Fatalf("model calls = %d, want at least 2 schema snapshots", len(state.schemas))
	}
	first := append([]string(nil), state.schemas[0]...)
	slices.Sort(first)
	second := append([]string(nil), state.schemas[1]...)
	slices.Sort(second)
	if !slices.Equal(first, second) {
		t.Fatalf("Tool schema changed after the run limit was exhausted: %v vs %v", state.schemas[0], state.schemas[1])
	}
	if !slices.Contains(second, ToolCreateDiagnosisTask) {
		t.Fatalf("create_diagnosis_task disappeared after the run limit: %v", state.schemas[1])
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
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
	}, current, 8, "")
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
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, CreateDiagnosisTask: creator,
		DiagnosisTaskStatus: &diagnosisTaskStatusReaderStub{},
	})
	if err != nil {
		t.Fatalf("NewConversationDefaultToolCatalog(): %v", err)
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
	if !strings.Contains(prompt, `"referenceId":"`+referenceID+`"`) || !strings.Contains(prompt, "<message_references>") {
		t.Fatalf("report reference prompt = %q", prompt)
	}
}

func TestConversationRunnerRejectsAttachmentReadWithoutReference(t *testing.T) {
	state := &conversationRunnerModelState{readAttachmentIfAvailable: true}
	reader := &attachmentReaderStub{}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, AttachmentReader: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: state}, ToolCatalog: catalog,
		SystemInstruction: "conversation attachment rejection test",
		ModelProvider:     "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 无附件引用：read_attachment 仍出现在固定 Schema 中，
	// 但执行必须被 RunAccess（缺少 attachment.read）拒绝，底层 Reader 零调用。
	request, ctx := conversationRunnerRequest(nil)
	_, err = runner.Respond(ctx, request)
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("Respond() error = %v, want ErrToolNotAllowed", err)
	}
	if reader.calls != 0 {
		t.Fatalf("attachment reader calls = %d, want 0", reader.calls)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.schemas) == 0 || !slices.Contains(state.schemas[0], ToolReadAttachment) {
		t.Fatalf("read_attachment missing from stable schema: %v", state.schemas)
	}
}

func TestConversationRunnerRejectsTaskStatusReadWithoutReference(t *testing.T) {
	state := &conversationRunnerModelState{readTaskStatusIfAvailable: true}
	statusReader := &diagnosisTaskStatusReaderStub{}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, DiagnosisTaskStatus: statusReader,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: state}, ToolCatalog: catalog,
		SystemInstruction: "conversation task status rejection test",
		ModelProvider:     "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
		MaxContextRunes: conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 无任务引用：get_diagnosis_task_status 仍出现在固定 Schema 中，
	// 但执行必须被 RunAccess（缺少 task.read）拒绝，底层 Reader 零调用。
	request, ctx := conversationRunnerRequest(nil)
	_, err = runner.Respond(ctx, request)
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("Respond() error = %v, want ErrToolNotAllowed", err)
	}
	if statusReader.calls != 0 {
		t.Fatalf("task status reader calls = %d, want 0", statusReader.calls)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.schemas) == 0 || !slices.Contains(state.schemas[0], ToolGetDiagnosisTaskStatus) {
		t.Fatalf("get_diagnosis_task_status missing from stable schema: %v", state.schemas)
	}
}

func TestNewConversationRunnerRejectsDiagnosisBoundCatalog(t *testing.T) {
	catalog := mustDiagnosisConfiguredDefaultCatalogForTest(t)
	// 会话 Runner 只能使用 conversation-default Catalog；传入诊断 Catalog 必须构造失败。
	_, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: &conversationRunnerTestModel{state: &conversationRunnerModelState{}}, ToolCatalog: catalog,
		SystemInstruction: "test", ModelProvider: "fixture", ModelID: "fixture-v1",
		PromptVersion: "conversation-test-v1", Logger: zap.NewNop(),
	})
	if err == nil || !strings.Contains(err.Error(), string(agentruntime.ToolProfileConversation)) {
		t.Fatalf("NewConversationRunner accepted a diagnosis-bound catalog, error = %v", err)
	}
}
