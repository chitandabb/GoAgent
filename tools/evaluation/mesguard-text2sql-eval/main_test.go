package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// 公共测试装配

func textToSQLTestConfig(dataSourceID uuid.UUID) config.Config {
	return config.Config{
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			Enabled: true, ActiveProfileName: "fixture",
			Profiles: map[string]config.ChatModelProfileConfig{
				"fixture": {
					Provider: "stepfun", BaseURL: "https://api.stepfun.example/v1",
					APIKeyEnv: "MESGUARD_TEST_API_KEY", Model: "step-3.7-flash",
					ReasoningEffort: "low", TimeoutMillis: 60000, MaxOutputTokens: 2048,
				},
			},
		}},
		SQLServer: config.SQLServerConfig{Enabled: true, ID: dataSourceID.String()},
	}
}

func textToSQLConversationCase() mesagent.TextToSQLEvaluationCase {
	return mesagent.TextToSQLEvaluationCase{
		DatasetVersion: "text-to-sql-v1", CaseID: "case-1",
		UserQuery:       "查询工单 TKT-999 的实时状态",
		ExpectedColumns: []string{"Status"}, ExpectedRows: [][]any{{"处理中"}},
		ResultOrder: mesagent.SQLResultOrdered,
	}
}

func conversationEvaluationIdentityForTest(t *testing.T, toolSchemaFingerprint string) conversationEvaluationIdentity {
	t.Helper()
	if len(toolSchemaFingerprint) != 64 {
		t.Fatalf("tool schema fingerprint = %q, want 64 hex chars", toolSchemaFingerprint)
	}
	return conversationEvaluationIdentity{
		modelProvider:           "stepfun",
		modelID:                 "step-3.7-flash",
		reasoningEffort:         "low",
		promptVersion:           "conversation-test-v1",
		modelProfileFingerprint: strings.Repeat("a", 64),
		implementationRevision:  "git:test-revision",
		implementationDirty:     false,
		toolProfileID:           string(agentruntime.ToolProfileConversation),
		toolSchemaFingerprint:   toolSchemaFingerprint,
	}
}

// textToSQLFixtureExecutor 是评测 harness 的底层执行 seam：先用真实
// platform/sqlserver QueryGuard 分析，拒绝写入/DDL/跨库等危险 SQL，再记录
// 调用与执行期 RunAccess 并返回固定只读结果。真实生产路径中这个位置是
// platform/sqlserver.ReadonlyQueryExecutor（真实 SQL Server + QueryGuard）。
type textToSQLFixtureExecutor struct {
	guard *platformsqlserver.ReadonlyQueryGuard
	mu    sync.Mutex
	calls int
	// rejected 记录被 QueryGuard 拒绝的查询（底层执行零调用）。
	rejected     []string
	seenAccess   agentruntime.RunAccess
	seenAccessOK bool
}

func newTextToSQLFixtureExecutor(t *testing.T) *textToSQLFixtureExecutor {
	t.Helper()
	guard, err := platformsqlserver.NewReadonlyQueryGuard([]string{"dbo"}, 16*1024)
	if err != nil {
		t.Fatalf("NewReadonlyQueryGuard: %v", err)
	}
	return &textToSQLFixtureExecutor{guard: guard}
}

func (e *textToSQLFixtureExecutor) Execute(
	ctx context.Context, _ uuid.UUID, query string,
) (repository.ReadonlyQueryResult, error) {
	if _, err := e.guard.Analyze(query); err != nil {
		e.mu.Lock()
		e.rejected = append(e.rejected, query)
		e.mu.Unlock()
		return repository.ReadonlyQueryResult{}, fmt.Errorf("%w: %v", platformsqlserver.ErrReadonlyQueryRejected, err)
	}
	access, accessOK := agentruntime.RunAccessFromContext(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.seenAccess, e.seenAccessOK = access, accessOK
	return repository.ReadonlyQueryResult{
		PolicyVersion: "fixture-v1", CatalogVersion: 1,
		Columns: []string{"Status"}, Rows: [][]any{{"处理中"}}, ReturnedRows: 1,
	}, nil
}

func (e *textToSQLFixtureExecutor) snapshot() (calls int, rejected int, access agentruntime.RunAccess, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, len(e.rejected), e.seenAccess, e.seenAccessOK
}

type evalFixtureSchemaSearcher struct{}

func (evalFixtureSchemaSearcher) SearchPublished(
	_ context.Context, _ uuid.UUID, _ string, _ int,
) ([]repository.SchemaCatalogEntry, error) {
	return []repository.SchemaCatalogEntry{{
		CatalogVersion: 1, ObjectSchema: "dbo", ObjectName: "v_MESGuardExternalCases",
		ObjectType: "VIEW", ColumnName: "Status", DataType: "nvarchar",
		Comment: "工单状态", SensitivityLevel: "internal",
	}}, nil
}

// ---------------------------------------------------------------------------
// 脚本化模型：纯步进驱动，模型自主决定 Tool 顺序与最终自然语言答案。

type evalScriptedSQLStep struct {
	toolName  string // search_schema_catalog / execute_readonly_query / read_external_case；空表示最终答案
	arguments string
}

type evalScriptedSQLState struct {
	mu      sync.Mutex
	calls   int
	schemas [][]string
	inputs  [][]string
}

type evalScriptedSQLModel struct {
	state  *evalScriptedSQLState
	tools  []*schema.ToolInfo
	steps  []evalScriptedSQLStep
	answer string
}

func newEvalScriptedSQLModel(steps []evalScriptedSQLStep, answer string) *evalScriptedSQLModel {
	return &evalScriptedSQLModel{
		state: &evalScriptedSQLState{}, steps: steps, answer: answer,
	}
}

func (m *evalScriptedSQLModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &evalScriptedSQLModel{
		state: m.state, tools: append([]*schema.ToolInfo(nil), tools...),
		steps: m.steps, answer: m.answer,
	}, nil
}

func (m *evalScriptedSQLModel) Generate(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	toolInfos := common.Tools
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	names := make([]string, 0, len(toolInfos))
	for _, info := range toolInfos {
		names = append(names, info.Name)
	}
	inputSnapshot := make([]string, 0, len(input))
	for _, message := range input {
		inputSnapshot = append(inputSnapshot, string(message.Role)+"\x00"+message.ToolName+"\x00"+message.Content)
	}
	m.state.mu.Lock()
	m.state.calls++
	m.state.schemas = append(m.state.schemas, names)
	m.state.inputs = append(m.state.inputs, inputSnapshot)
	callIndex := m.state.calls - 1
	m.state.mu.Unlock()
	if callIndex < len(m.steps) && m.steps[callIndex].toolName != "" {
		return evalToolCallMessage(m.steps[callIndex].toolName, m.steps[callIndex].arguments), nil
	}
	return evalUsageMessage(schema.AssistantMessage(m.answer, nil)), nil
}

func (m *evalScriptedSQLModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func evalToolCallMessage(name, arguments string) *schema.Message {
	return evalUsageMessage(schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-" + name, Function: schema.FunctionCall{Name: name, Arguments: arguments},
	}}))
}

func evalUsageMessage(message *schema.Message) *schema.Message {
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
	}}
	return message
}

func conversationEvaluationDepsForTest(
	t *testing.T,
	modelInstance model.ToolCallingChatModel,
	executor *textToSQLFixtureExecutor,
	sqlDataSourceID uuid.UUID,
) conversationEvaluationDependencies {
	t.Helper()
	return conversationEvaluationDependencies{
		chatModel:         modelInstance,
		externalCases:     unavailableExternalCaseGetter{},
		schemaSearcher:    evalFixtureSchemaSearcher{},
		readonlyExecutor:  executor,
		logger:            zap.NewNop(),
		systemInstruction: "你是 MESGuard 助手，回答用户的业务数据问题。",
		modelProvider:     "stepfun",
		modelID:           "step-3.7-flash",
		promptVersion:     "conversation-test-v1",
		maxIterations:     8,
		maxToolCalls:      8,
		maxTotalTokens:    16000,
		maxContextRunes:   conversation.MaxContentRunes,
		timeout:           30 * time.Second,
		sqlDataSourceID:   sqlDataSourceID,
	}
}

// ---------------------------------------------------------------------------
// 红测试：conversation 模式

func TestConversationEvaluationRunsNaturalLanguageThroughRealAssembly(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	if !observation.Correct || observation.ErrorType != "" {
		t.Fatalf("observation = correct:%t errorType:%q", observation.Correct, observation.ErrorType)
	}
	if len(observation.ActualToolCalls) != 2 ||
		observation.ActualToolCalls[0].ToolName != mesagent.ToolSearchSchemaCatalog ||
		observation.ActualToolCalls[1].ToolName != mesagent.ToolExecuteReadonlyQuery {
		t.Fatalf("actual tool order = %+v, want search_schema_catalog -> execute_readonly_query", observation.ActualToolCalls)
	}
	if observation.ActualToolCallCount != 2 || !observation.ToolTraceComplete || !observation.ToolSequenceCorrect {
		t.Fatalf("tool trace = count:%d complete:%t sequence:%t", observation.ActualToolCallCount,
			observation.ToolTraceComplete, observation.ToolSequenceCorrect)
	}
	if !strings.HasPrefix(observation.QueryHash, "sha256:") || !strings.Contains(observation.GeneratedQuery, "SELECT") {
		t.Fatalf("SQL hash/query not recorded: hash=%q query=%q", observation.QueryHash, observation.GeneratedQuery)
	}
	if len(observation.Columns) != 1 || observation.Columns[0] != "Status" ||
		len(observation.Rows) != 1 || observation.Rows[0][0] != "处理中" {
		t.Fatalf("execution result not recorded: %v / %v", observation.Columns, observation.Rows)
	}
	if !strings.Contains(observation.Answer, "处理中") {
		t.Fatalf("answer = %q", observation.Answer)
	}
	if observation.Usage.ModelCalls != 3 {
		t.Fatalf("usage model calls = %d, want 3 (schema search + query + final answer)", observation.Usage.ModelCalls)
	}
	if observation.ToolProfileID != string(agentruntime.ToolProfileConversation) ||
		observation.ImplementationRevision != "git:test-revision" || observation.ImplementationDirty {
		t.Fatalf("identity fields = %+v", observation)
	}
	// 底层执行器确实执行了授权数据源上的查询。
	calls, _, _, _ := executor.snapshot()
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

func TestConversationEvaluationRejectsWrongNaturalLanguageAnswer(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为已解决。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if observation.Correct || observation.ErrorType != "answer_mismatch" {
		t.Fatalf("wrong final answer accepted: correct=%t errorType=%q answer=%q",
			observation.Correct, observation.ErrorType, observation.Answer)
	}
}

func TestConversationEvaluationFirstTurnModelVisibleSQLPair(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	modelInstance.state.mu.Lock()
	defer modelInstance.state.mu.Unlock()
	if len(modelInstance.state.schemas) == 0 {
		t.Fatal("model never saw a Tool schema")
	}
	firstTurn := modelInstance.state.schemas[0]
	if !toolNameListed(firstTurn, mesagent.ToolSearchSchemaCatalog) ||
		!toolNameListed(firstTurn, mesagent.ToolExecuteReadonlyQuery) {
		t.Fatalf("first-turn model-visible schemas = %v, want the SQL pair", firstTurn)
	}
}

func toolNameListed(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestConversationEvaluationTurnContextAtTailCarriesAuthorizedDataSource(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	modelInstance.state.mu.Lock()
	defer modelInstance.state.mu.Unlock()
	firstInput := strings.Join(modelInstance.state.inputs[0], "\n")
	if !strings.Contains(firstInput, "</turn_context>") ||
		!strings.Contains(firstInput, `"dataSourceId":"`+sqlDataSourceID.String()+`"`) {
		t.Fatalf("turn_context missing the authorized dataSourceId from first user message: %q", firstInput)
	}
	userIndex := -1
	for index, message := range modelInstance.state.inputs[0] {
		if strings.HasPrefix(message, string(schema.User)+"\x00") {
			userIndex = index
		}
	}
	if userIndex < 0 {
		t.Fatal("no user message in the first model input")
	}
	userMessage := modelInstance.state.inputs[0][userIndex]
	if !strings.HasPrefix(userMessage, string(schema.User)+"\x00\x00查询工单 TKT-999 的实时状态") ||
		!strings.HasSuffix(userMessage, "</turn_context>") {
		t.Fatalf("turn_context must be appended at the tail of the current user message: %q", userMessage)
	}
}

func TestConversationEvaluationRunAccessCarriesSQLReadAndGrant(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	_, _, seenAccess, seenAccessOK := executor.snapshot()
	if !seenAccessOK || !seenAccess.Allows(agentruntime.PermissionSQLRead) ||
		!uuidInList(seenAccess.Grants().DataSourceIDs(), sqlDataSourceID) {
		t.Fatalf("executor did not observe sql.read with the granted data source: %+v ok=%v", seenAccess, seenAccessOK)
	}
}

func uuidInList(values []uuid.UUID, want uuid.UUID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestConversationEvaluationUnauthorizedDataSourceZeroExecutorCalls(t *testing.T) {
	// 未配置 SQLDataSourceID：SQL Tool 仍在固定 Schema 中，但 RunAccess 缺
	// sql.read，执行期 fail-closed，底层 executor 零调用。
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, uuid.Nil))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	if observation.Correct || observation.ErrorType != "tool_not_allowed" {
		t.Fatalf("observation = correct:%t errorType:%q calls:%d recorded:%+v, want tool_not_allowed",
			observation.Correct, observation.ErrorType, observation.ActualToolCallCount, observation.ActualToolCalls)
	}
	if calls, rejected, _, _ := executor.snapshot(); calls != 0 || rejected != 0 {
		t.Fatalf("executor calls = %d rejected = %d, want 0/0", calls, rejected)
	}
}

func TestConversationEvaluationWrongDataSourceZeroExecutorCalls(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	wrongID := uuid.New()
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"dataSourceId":"` + wrongID.String() + `","query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	if observation.Correct {
		t.Fatal("wrong data source must not produce a correct observation")
	}
	if calls, rejected, _, _ := executor.snapshot(); calls != 0 || rejected != 0 {
		t.Fatalf("executor calls = %d rejected = %d, want 0/0 for an unauthorized data source", calls, rejected)
	}
}

func TestConversationEvaluationQueryGuardRejectsWriteDDLCrossDatabase(t *testing.T) {
	queries := map[string]string{
		"write":   "INSERT INTO dbo.v_MESGuardExternalCases (TicketID) VALUES ('TKT-1')",
		"ddl":     "DROP TABLE dbo.v_MESGuardExternalCases",
		"crossdb": "SELECT Status FROM otherdb.dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'",
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			sqlDataSourceID := uuid.New()
			executor := newTextToSQLFixtureExecutor(t)
			modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
				{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
				{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"` + query + `"}`},
			}, "无法回答。")
			assembly, err := buildConversationEvaluation(context.Background(),
				conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
			if err != nil {
				t.Fatalf("buildConversationEvaluation: %v", err)
			}
			observation := observeConversationCase(context.Background(), assembly,
				textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
			if err := observation.Validate(); err != nil {
				t.Fatalf("observation Validate: %v", err)
			}
			if observation.Correct || observation.ErrorType != "guard_rejected" {
				t.Fatalf("observation = correct:%t errorType:%q, want guard_rejected", observation.Correct, observation.ErrorType)
			}
			if calls, rejected, _, _ := executor.snapshot(); calls != 0 || rejected != 1 {
				t.Fatalf("executor calls = %d rejected = %d, want 0/1 (QueryGuard must block before execution)", calls, rejected)
			}
		})
	}
}

func TestConversationEvaluationNoSQLQueryWhenModelSkipsSQLTools(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	// read_conversation_tool_result 由 Catalog 内部构造并注册（模型可见），
	// SQL recorder 无法记录它；Runner 的总调用数必须使本 case fail-closed，
	// 不能将不完整轨迹当成 no_sql_query。
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolReadConversationToolResult, arguments: `{"ref":"sha256:1111111111111111111111111111111111111111111111111111111111111111","offsetBytes":0,"maxBytes":128}`},
	}, "无法回答。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	if observation.Correct || observation.ErrorType != "unobserved_tool_call" {
		t.Fatalf("observation = correct:%t errorType:%q, want unobserved_tool_call", observation.Correct, observation.ErrorType)
	}
	if len(observation.ActualToolCalls) != 0 {
		t.Fatalf("actual SQL tool calls = %+v, want none", observation.ActualToolCalls)
	}
	if observation.ActualToolCallCount != 1 || observation.ToolTraceComplete {
		t.Fatalf("tool trace = count:%d complete:%t, want 1/false",
			observation.ActualToolCallCount, observation.ToolTraceComplete)
	}
	if calls, rejected, _, _ := executor.snapshot(); calls != 0 || rejected != 0 {
		t.Fatalf("executor calls = %d rejected = %d, want 0/0", calls, rejected)
	}
}

func TestConversationEvaluationRejectsQueryWithoutSchemaSearch(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if observation.Correct || observation.ErrorType != "invalid_tool_sequence" {
		t.Fatalf("query-only turn = correct:%t errorType:%q, want invalid_tool_sequence",
			observation.Correct, observation.ErrorType)
	}
	if !observation.ToolTraceComplete || observation.ToolSequenceCorrect {
		t.Fatalf("tool trace = complete:%t sequence:%t", observation.ToolTraceComplete, observation.ToolSequenceCorrect)
	}
}

func TestConversationEvaluationAllowsRepeatedSchemaSearchesBeforeSingleQuery(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TicketID","limit":5}`},
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"Status","limit":5}`},
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "工单 TKT-999 当前状态为 处理中。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation Validate: %v", err)
	}
	if !observation.Correct || !observation.ToolSequenceCorrect || observation.ActualToolCallCount != 4 {
		t.Fatalf("repeated-search observation = %+v", observation)
	}
}

func TestConversationEvaluationFailurePreservesRunUsage(t *testing.T) {
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`},
	}, "无法回答。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, uuid.Nil))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if observation.ErrorType != "tool_not_allowed" {
		t.Fatalf("errorType = %q calls:%d recorded:%+v, want tool_not_allowed",
			observation.ErrorType, observation.ActualToolCallCount, observation.ActualToolCalls)
	}
	if observation.Usage.ModelCalls == 0 || observation.Usage.TotalTokens == 0 {
		t.Fatalf("failed turn lost Provider usage: %+v", observation.Usage)
	}
	if observation.ActualToolCallCount != 1 {
		t.Fatalf("failed turn tool calls = %d, want 1 (schema search denied before SQL execution)",
			observation.ActualToolCallCount)
	}
}

func TestConversationEvaluationMalformedSQLArgumentsBecomeCaseFailure(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := newEvalScriptedSQLModel([]evalScriptedSQLStep{
		{toolName: mesagent.ToolSearchSchemaCatalog, arguments: `{"keyword":"TKT-999","limit":5}`},
		{toolName: mesagent.ToolExecuteReadonlyQuery, arguments: `{"query":`},
	}, "无法回答。")
	assembly, err := buildConversationEvaluation(context.Background(),
		conversationEvaluationDepsForTest(t, modelInstance, executor, sqlDataSourceID))
	if err != nil {
		t.Fatalf("buildConversationEvaluation: %v", err)
	}
	observation := observeConversationCase(context.Background(), assembly,
		textToSQLConversationCase(), conversationEvaluationIdentityForTest(t, assembly.toolSchemaFingerprint))
	if observation.ErrorType != "invalid_tool_arguments" || observation.Correct {
		t.Fatalf("malformed arguments = correct:%t errorType:%q", observation.Correct, observation.ErrorType)
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("malformed-argument observation must remain reducible: %v", err)
	}
	if len(observation.ActualToolCalls) != 2 ||
		observation.ActualToolCalls[1].QueryHash != "" ||
		observation.ActualToolCalls[1].ErrorType != "invalid_tool_arguments" {
		t.Fatalf("malformed call = %+v", observation.ActualToolCalls)
	}
	if calls, rejected, _, _ := executor.snapshot(); calls != 0 || rejected != 0 {
		t.Fatalf("executor calls = %d rejected = %d, want 0/0", calls, rejected)
	}
}

// ---------------------------------------------------------------------------
// 红测试：direct 模式语义保持 + 成本护栏在 Provider 创建前检查

// directSingleToolModel 只返回一次强制 execute_readonly_query Tool Call，
// 锁定 direct 模式的历史能力测试语义（强制 Tool Calling、单次 Generate）。
type directSingleToolModel struct {
	query string
}

func (m *directSingleToolModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *directSingleToolModel) Generate(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.Message, error) {
	return evalToolCallMessage(mesagent.ToolExecuteReadonlyQuery,
		`{"query":"SELECT Status FROM dbo.v_MESGuardExternalCases WHERE TicketID='TKT-999'"}`), nil
}

func (m *directSingleToolModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestDirectModeForcedToolCallingSemanticsUnchanged(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := newTextToSQLFixtureExecutor(t)
	modelInstance := &directSingleToolModel{}
	observation := observeTextToSQL(context.Background(), modelInstance, executor,
		textToSQLTestConfig(sqlDataSourceID), sqlDataSourceID, textToSQLConversationCase())
	if err := observation.Validate(); err != nil {
		t.Fatalf("direct observation Validate: %v", err)
	}
	if observation.ToolCallCount != 1 || observation.SelectedTool != mesagent.ToolExecuteReadonlyQuery {
		t.Fatalf("direct mode must keep the forced single Tool Call: count=%d tool=%q",
			observation.ToolCallCount, observation.SelectedTool)
	}
	if !observation.Correct || observation.ErrorType != "" {
		t.Fatalf("observation = correct:%t errorType:%q", observation.Correct, observation.ErrorType)
	}
	if !strings.HasPrefix(observation.QueryHash, "sha256:") || observation.Usage.ModelCalls != 1 {
		t.Fatalf("hash=%q usage=%+v", observation.QueryHash, observation.Usage)
	}
	if calls, _, _, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

func TestValidateTextToSQLProviderBudget(t *testing.T) {
	if _, err := validateTextToSQLProviderBudget(1, 1, 1000, false, 1, 10, 10000); err == nil ||
		!strings.Contains(err.Error(), "allow-provider-calls") {
		t.Fatalf("missing -allow-provider-calls must be refused, got %v", err)
	}
	if _, err := validateTextToSQLProviderBudget(1, 1, 1000, true, 0, 10, 10000); err == nil {
		t.Fatal("non-positive max-cases must be refused")
	}
	if _, err := validateTextToSQLProviderBudget(2, 1, 1000, true, 1, 10, 10000); err == nil ||
		!strings.Contains(err.Error(), "max-cases") {
		t.Fatalf("cases above max-cases must be refused, got %v", err)
	}
	if _, err := validateTextToSQLProviderBudget(1, 8, 16000, true, 1, 4, 100000); err == nil ||
		!strings.Contains(err.Error(), "max-provider-calls") {
		t.Fatalf("Provider call upper bound above the cap must be refused, got %v", err)
	}
	if _, err := validateTextToSQLProviderBudget(1, 8, 16000, true, 1, 100, 1000); err == nil ||
		!strings.Contains(err.Error(), "max-provider-tokens") {
		t.Fatalf("Token upper bound above the cap must be refused, got %v", err)
	}
	budget, err := validateTextToSQLProviderBudget(3, 8, 16000, true, 3, 24, 48000)
	if err != nil {
		t.Fatalf("valid budget: %v", err)
	}
	if budget.Cases != 3 || budget.ProviderCalls != 24 || budget.TotalTokens != 48000 {
		t.Fatalf("budget = %+v", budget)
	}
}

type recordingModelFactory struct {
	calls int
}

func (f *recordingModelFactory) build(
	_ context.Context, _ config.ChatModelConfig,
) (model.ToolCallingChatModel, error) {
	f.calls++
	return &directSingleToolModel{}, nil
}

func writeTextToSQLTestDataset(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "text-to-sql-v1.jsonl")
	encoded, err := json.Marshal(textToSQLConversationCase())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withCleanGitSeams(t *testing.T) {
	t.Helper()
	originalRevParse := evaluationidentity.GitRevParseShortHead
	originalStatus := evaluationidentity.GitStatusPorcelain
	evaluationidentity.GitRevParseShortHead = func(context.Context) (string, error) { return "abc123", nil }
	evaluationidentity.GitStatusPorcelain = func(context.Context) (string, error) { return "", nil }
	t.Cleanup(func() {
		evaluationidentity.GitRevParseShortHead = originalRevParse
		evaluationidentity.GitStatusPorcelain = originalStatus
	})
}

func withDirtyGitSeams(t *testing.T) {
	t.Helper()
	originalRevParse := evaluationidentity.GitRevParseShortHead
	originalStatus := evaluationidentity.GitStatusPorcelain
	evaluationidentity.GitRevParseShortHead = func(context.Context) (string, error) { return "abc123", nil }
	evaluationidentity.GitStatusPorcelain = func(context.Context) (string, error) { return " M main.go\n", nil }
	t.Cleanup(func() {
		evaluationidentity.GitRevParseShortHead = originalRevParse
		evaluationidentity.GitStatusPorcelain = originalStatus
	})
}

func TestRunEvaluationRefusesProviderWithoutAllowFlag(t *testing.T) {
	withCleanGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	err := runEvaluation([]string{
		"-mode", "direct", "-dataset", writeTextToSQLTestDataset(t, dir),
		"-max-cases", "1", "-max-provider-calls", "10", "-max-provider-tokens", "100000",
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil || !strings.Contains(err.Error(), "allow-provider-calls") {
		t.Fatalf("run must refuse without -allow-provider-calls, got %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("Provider was created %d times before the guard, want 0", factory.calls)
	}
}

func TestRunEvaluationRefusesProviderOnTightTokenBudget(t *testing.T) {
	withCleanGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	err := runEvaluation([]string{
		"-mode", "direct", "-dataset", writeTextToSQLTestDataset(t, dir),
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "100",
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil || !strings.Contains(err.Error(), "max-provider-tokens") {
		t.Fatalf("run must refuse an insufficient Token cap, got %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("Provider was created %d times before the guard, want 0", factory.calls)
	}
}

func TestRunEvaluationRefusesProviderOnDirtyRevision(t *testing.T) {
	withDirtyGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	err := runEvaluation([]string{
		"-mode", "direct", "-dataset", writeTextToSQLTestDataset(t, dir),
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "100000",
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil || !strings.Contains(err.Error(), "dirty or unknown") {
		t.Fatalf("formal mode must refuse a dirty revision, got %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("Provider was created %d times before the identity guard, want 0", factory.calls)
	}
}

func TestRunEvaluationConversationRefusesProviderOnCallBudget(t *testing.T) {
	withCleanGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	// conversation 模式每 case 上限 8 次模型调用；授权 4 次必须拒绝。
	err := runEvaluation([]string{
		"-mode", "conversation", "-dataset", writeTextToSQLTestDataset(t, dir),
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "4", "-max-provider-tokens", "100000",
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil || !strings.Contains(err.Error(), "max-provider-calls") {
		t.Fatalf("conversation run must refuse an insufficient Provider call cap, got %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("Provider was created %d times before the guard, want 0", factory.calls)
	}
}

func TestRunEvaluationReachesProviderAfterCleanPreflight(t *testing.T) {
	withCleanGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	// 预检通过后创建 Provider，随后在 SQL Server 阶段失败：证明护栏先于
	// Provider，且护栏通过后 Provider 确实被创建。
	err := runEvaluation([]string{
		"-mode", "direct", "-dataset", writeTextToSQLTestDataset(t, dir),
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "100000",
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil {
		t.Fatal("run must fail later at the SQL Server stage with an empty test config")
	}
	if factory.calls != 1 {
		t.Fatalf("Provider must be created exactly once after a clean preflight, got %d", factory.calls)
	}
}

func TestRunEvaluationRejectsUnknownMode(t *testing.T) {
	withCleanGitSeams(t)
	dataSourceID := uuid.New()
	dir := t.TempDir()
	factory := &recordingModelFactory{}
	err := runEvaluation([]string{
		"-mode", "bogus", "-dataset", writeTextToSQLTestDataset(t, dir),
	}, func() (config.Config, error) {
		return textToSQLTestConfig(dataSourceID), nil
	}, factory.build)
	if err == nil || !strings.Contains(err.Error(), "mode must be direct or conversation") {
		t.Fatalf("unknown mode must be refused, got %v", err)
	}
	if factory.calls != 0 {
		t.Fatalf("Provider was created %d times for an unknown mode, want 0", factory.calls)
	}
}

var _ = errors.New
var _ = fmt.Sprintf
