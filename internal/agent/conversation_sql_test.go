package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// countingReadonlyQueryExecutor 记录调用次数并暴露执行期 Context，
// 用于证明未授权时底层 executor 零调用、授权时看到权威 RunAccess。
type countingReadonlyQueryExecutor struct {
	mu           sync.Mutex
	calls        int
	dataSourceID uuid.UUID
	query        string
	seenAccess   agentruntime.RunAccess
	seenAccessOK bool
}

func (s *countingReadonlyQueryExecutor) Execute(
	ctx context.Context, dataSourceID uuid.UUID, query string,
) (repository.ReadonlyQueryResult, error) {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.dataSourceID, s.query = dataSourceID, query
	s.seenAccess, s.seenAccessOK = access, ok
	return repository.ReadonlyQueryResult{
		PolicyVersion: ReadonlyQueryPolicyVersionForTest,
		Columns:       []string{"Status"},
		Rows:          [][]any{{"处理中"}},
		ReturnedRows:  1,
	}, nil
}

func (s *countingReadonlyQueryExecutor) snapshot() (int, uuid.UUID, string, agentruntime.RunAccess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.dataSourceID, s.query, s.seenAccess, s.seenAccessOK
}

type countingSchemaCatalogSearcher struct {
	calls int
}

func (s *countingSchemaCatalogSearcher) SearchPublished(
	_ context.Context, _ uuid.UUID, _ string, _ int,
) ([]repository.SchemaCatalogEntry, error) {
	s.calls++
	return []repository.SchemaCatalogEntry{{
		CatalogVersion: 1, ObjectSchema: "dbo", ObjectName: "Tickets", ObjectType: "TABLE",
		ColumnName: "Status", Comment: "工单状态", SensitivityLevel: "internal",
	}}, nil
}

func mustConversationSQLAccess(t *testing.T, dataSourceIDs []uuid.UUID) agentruntime.RunAccess {
	t.Helper()
	permissions, err := agentruntime.NewPermissionSet(agentruntime.PermissionSQLRead)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		DataSourceIDs: dataSourceIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.NewConversationRunAccess(
		agentruntime.Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}, permissions, grants,
	)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func TestConversationProfileIncludesConstructedSQLToolsOnly(t *testing.T) {
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
		SchemaCatalog: mustSchemaCatalogToolForTest(t),
		ReadonlyQuery: mustReadonlyQueryToolForTest(t),
	})
	if err != nil {
		t.Fatalf("NewConversationDefaultToolCatalog: %v", err)
	}
	if got := catalog.BoundProfileID(); got != agentruntime.ToolProfileConversation {
		t.Fatalf("bound profile = %q", got)
	}
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	names := resolved.ModelVisibleNames
	if !slices.Contains(names, ToolSearchSchemaCatalog) || !slices.Contains(names, ToolExecuteReadonlyQuery) {
		t.Fatalf("conversation profile must include constructed SQL Tools: %v", names)
	}
	if slices.Contains(names, ToolDatabaseObjectDefinition) {
		t.Fatalf("get_database_object_definition must stay Diagnosis-only in the minimal Tool set: %v", names)
	}
	if !slices.Contains(resolved.ModelVisibleNames, ToolExecuteReadonlyQuery) {
		t.Fatalf("SQL Tool missing from model-visible names: %v", resolved.ModelVisibleNames)
	}
}

func TestConversationProfileSchemaAndFingerprintStableAcrossReferences(t *testing.T) {
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
		SchemaCatalog: mustSchemaCatalogToolForTest(t),
		ReadonlyQuery: mustReadonlyQueryToolForTest(t),
	})
	if err != nil {
		t.Fatalf("NewConversationDefaultToolCatalog: %v", err)
	}
	base, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileConversation)
	if err != nil {
		t.Fatal(err)
	}
	baseFingerprint, err := CanonicalToolContractFingerprint(context.Background(), base.Tools)
	if err != nil {
		t.Fatal(err)
	}

	// 引用变化、RunAccess 变化都不改变 Schema 或指纹：Profile 是启动期固定的。
	for _, ctxBuilder := range []func() context.Context{
		func() context.Context { return context.Background() },
		func() context.Context {
			access := mustConversationSQLAccess(t, []uuid.UUID{uuid.New()})
			return agentruntime.WithRunAccess(context.Background(), access)
		},
		func() context.Context {
			access := mustConversationTestRunAccess(t, uuid.New(),
				[]agentruntime.Permission{agentruntime.PermissionKnowledgeRead},
				agentruntime.ResourceGrantsConfig{},
			)
			return agentruntime.WithRunAccess(context.Background(), access)
		},
	} {
		resolved, resolveErr := catalog.ResolveProfile(ctxBuilder(), agentruntime.ToolProfileConversation)
		if resolveErr != nil {
			t.Fatalf("ResolveProfile: %v", resolveErr)
		}
		if !slices.Equal(resolved.ModelVisibleNames, base.ModelVisibleNames) {
			t.Fatalf("schema changed across references: %v vs %v", resolved.ModelVisibleNames, base.ModelVisibleNames)
		}
		fingerprint, fingerprintErr := CanonicalToolContractFingerprint(context.Background(), resolved.Tools)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		if fingerprint != baseFingerprint {
			t.Fatalf("Tool contract fingerprint changed across references: %s vs %s", fingerprint, baseFingerprint)
		}
	}
}

func mustReadonlyQueryToolForTest(t *testing.T) tool.InvokableTool {
	t.Helper()
	current, err := NewExecuteReadonlyQueryTool(&countingReadonlyQueryExecutor{})
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	return current
}

func TestExecuteReadonlyQueryExplicitGrantedDataSource(t *testing.T) {
	dataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID}))
	result, err := current.InvokableRun(ctx, `{"dataSourceId":"`+dataSourceID.String()+`","query":"SELECT Status FROM dbo.v_Tickets"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	calls, gotID, gotQuery, seenAccess, seenAccessOK := executor.snapshot()
	if calls != 1 || gotID != dataSourceID || gotQuery != "SELECT Status FROM dbo.v_Tickets" {
		t.Fatalf("executor call = %d/%s/%s", calls, gotID, gotQuery)
	}
	if !seenAccessOK || !seenAccess.Allows(agentruntime.PermissionSQLRead) {
		t.Fatalf("executor did not observe authoritative RunAccess: %+v ok=%v", seenAccess, seenAccessOK)
	}
	if result == "" {
		t.Fatal("readonly query result is empty")
	}
}

func TestExecuteReadonlyQueryExplicitOverPrivilegeRejected(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{uuid.New()}))
	if _, err := current.InvokableRun(ctx, `{"dataSourceId":"`+uuid.NewString()+`","query":"SELECT 1"}`); err == nil {
		t.Fatal("query Tool accepted a dataSourceId outside the Grant")
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteReadonlyQueryNoRunAccessRejected(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	if _, err := current.InvokableRun(context.Background(), `{"query":"SELECT 1"}`); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("error = %v, want ErrRunAccessRequired", err)
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteReadonlyQueryNoSQLPermissionRejected(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	permissions, err := agentruntime.NewPermissionSet(agentruntime.PermissionCaseRead)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		DataSourceIDs: []uuid.UUID{uuid.New()},
	})
	if err != nil {
		t.Fatal(err)
	}
	access, err := agentruntime.NewConversationRunAccess(
		agentruntime.Actor{UserID: uuid.New(), Role: auth.RoleAnalyst}, permissions, grants,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.InvokableRun(
		agentruntime.WithRunAccess(context.Background(), access), `{"query":"SELECT 1"}`,
	); err == nil {
		t.Fatal("query Tool accepted a run without sql.read")
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteReadonlyQueryZeroGrantedSourcesRejected(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	// sql.read 存在但 Grant 为空：空 Grant 永远表示无权限，省略 ID 必须拒绝。
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationSQLAccess(t, nil))
	if _, err := current.InvokableRun(ctx, `{"query":"SELECT 1"}`); err == nil {
		t.Fatal("query Tool accepted an omitted dataSourceId with zero granted sources")
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteReadonlyQueryMultipleGrantedSourcesOmittedRejected(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{uuid.New(), uuid.New()}))
	if _, err := current.InvokableRun(ctx, `{"query":"SELECT 1"}`); err == nil {
		t.Fatal("query Tool accepted an omitted dataSourceId with multiple granted sources")
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteReadonlyQueryOmittedSingleGrantedSourceSucceeds(t *testing.T) {
	dataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{dataSourceID}))
	if _, err := current.InvokableRun(ctx, `{"query":"SELECT 1"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if calls, gotID, _, _, _ := executor.snapshot(); calls != 1 || gotID != dataSourceID {
		t.Fatalf("executor call = %d/%s", calls, gotID)
	}
}

func TestSearchSchemaCatalogRejectsBeforeSearcherCall(t *testing.T) {
	searcher := &countingSchemaCatalogSearcher{}
	current, err := NewSearchSchemaCatalogTool(searcher)
	if err != nil {
		t.Fatalf("NewSearchSchemaCatalogTool: %v", err)
	}
	// 无 RunAccess：拒绝且 searcher 零调用。
	if _, err := current.InvokableRun(context.Background(), `{"keyword":"ticket"}`); !errors.Is(err, ErrRunAccessRequired) {
		t.Fatalf("error = %v, want ErrRunAccessRequired", err)
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher calls = %d, want 0", searcher.calls)
	}
	// 显式越权：拒绝且 searcher 零调用。
	ctx := agentruntime.WithRunAccess(context.Background(),
		mustConversationSQLAccess(t, []uuid.UUID{uuid.New()}))
	if _, err := current.InvokableRun(ctx, `{"dataSourceId":"`+uuid.NewString()+`","keyword":"ticket"}`); err == nil {
		t.Fatal("catalog Tool accepted an unauthorized data source")
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher calls = %d, want 0 after over-privilege rejection", searcher.calls)
	}
	// 授权：searcher 恰好一次。
	if _, err := current.InvokableRun(ctx, `{"keyword":"ticket"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("searcher calls = %d, want 1", searcher.calls)
	}
}

func TestDiagnosisRunAccessKeepsReadOnlySQLPath(t *testing.T) {
	dataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	// Diagnosis RunAccess 直接由冻结 Policy ∩ ceiling 派生：read_only
	// 生产源进入 Grant，SQL 路径保持可用。
	access := mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{DataSourceIDs: []uuid.UUID{dataSourceID}},
	)
	if _, err := current.InvokableRun(
		withTestRunAccess(context.Background(), access), `{"query":"SELECT Status FROM dbo.v_Tickets"}`,
	); err != nil {
		t.Fatalf("diagnosis SQL path failed: %v", err)
	}
	if calls, gotID, _, _, _ := executor.snapshot(); calls != 1 || gotID != dataSourceID {
		t.Fatalf("executor call = %d/%s", calls, gotID)
	}
}

func TestDiagnosisRunAccessExcludesBoundedLabSource(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	current, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatalf("NewExecuteReadonlyQueryTool: %v", err)
	}
	// bounded_lab 数据源永不进入只读 SQL Grant：Policy 构造只允许
	// read_only 源，RunAccess 派生同样只保留只读源。
	access := mustDiagnosisTestRunAccess(t, uuid.New(),
		[]agentruntime.Permission{agentruntime.PermissionSQLRead},
		agentruntime.ResourceGrantsConfig{DataSourceIDs: []uuid.UUID{}},
	)
	if _, err := current.InvokableRun(
		withTestRunAccess(context.Background(), access), `{"query":"SELECT 1"}`,
	); err == nil {
		t.Fatal("query Tool accepted a run without any data source grant")
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

// scriptedSQLConversationModel 模拟自然语言 Conversation：先查 Schema Catalog，
// 再执行单条只读 T-SQL，最后基于结果作答——通过真实 Runner 装配验证生产可达性。
type scriptedSQLConversationModel struct {
	mu      sync.Mutex
	calls   int
	inputs  [][]string
	schemas [][]string
	tools   []*schema.ToolInfo
	state   *scriptedSQLState
}

type scriptedSQLState struct {
	catalogCalled bool
	queryCalled   bool
}

type duplicateReadonlyQueryModel struct {
	tools []*schema.ToolInfo
	state *duplicateReadonlyQueryState
}

type duplicateReadonlyQueryState struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

func (m *duplicateReadonlyQueryModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &duplicateReadonlyQueryModel{
		tools: append([]*schema.ToolInfo(nil), tools...),
		state: m.state,
	}, nil
}

func (m *duplicateReadonlyQueryModel) Generate(
	_ context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	toolInfos := common.Tools
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	m.state.mu.Lock()
	m.state.inputs = append(m.state.inputs, append([]*schema.Message(nil), input...))
	m.state.mu.Unlock()

	queryResults := 0
	for _, message := range input {
		if message.Role == schema.Tool && message.ToolName == ToolExecuteReadonlyQuery {
			queryResults++
		}
	}
	if queryResults == 0 && toolNameInList(toolInfoNames(toolInfos), ToolExecuteReadonlyQuery) {
		return withRunnerTestUsage(runnerTestToolCall(
			ToolExecuteReadonlyQuery,
			`{"query":"SELECT Status FROM dbo.v_Tickets WHERE TicketID='TKT-999'"}`,
		)), nil
	}
	if queryResults == 1 {
		return withRunnerTestUsage(runnerTestToolCall(
			ToolExecuteReadonlyQuery,
			`{"query":"SELECT COUNT(*) AS Total FROM dbo.v_Tickets"}`,
		)), nil
	}
	return withRunnerTestUsage(schema.AssistantMessage("已根据首次查询结果作答。", nil)), nil
}

func (m *duplicateReadonlyQueryModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func toolInfoNames(infos []*schema.ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func (m *scriptedSQLConversationModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &scriptedSQLConversationModel{
		state: m.state, tools: append([]*schema.ToolInfo(nil), tools...),
	}, nil
}

func (m *scriptedSQLConversationModel) Generate(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	toolInfos := common.Tools
	if len(toolInfos) == 0 {
		toolInfos = m.tools
	}
	names := toolInfoNames(toolInfos)
	inputSnapshot := make([]string, 0, len(input))
	catalogResult := false
	queryResult := false
	for _, message := range input {
		inputSnapshot = append(inputSnapshot, string(message.Role)+"\x00"+message.ToolName+"\x00"+message.Content)
		if message.Role == schema.Tool && message.ToolName == ToolSearchSchemaCatalog {
			catalogResult = true
		}
		if message.Role == schema.Tool && message.ToolName == ToolExecuteReadonlyQuery {
			queryResult = true
		}
	}
	m.mu.Lock()
	m.calls++
	m.schemas = append(m.schemas, names)
	m.inputs = append(m.inputs, inputSnapshot)
	m.mu.Unlock()
	if !catalogResult && toolNameInList(names, ToolSearchSchemaCatalog) {
		return withRunnerTestUsage(runnerTestToolCall(
			ToolSearchSchemaCatalog, `{"keyword":"TKT-999","limit":5}`,
		)), nil
	}
	if !queryResult && toolNameInList(names, ToolExecuteReadonlyQuery) {
		return withRunnerTestUsage(runnerTestToolCall(
			ToolExecuteReadonlyQuery, `{"query":"SELECT Status FROM dbo.v_Tickets WHERE TicketID='TKT-999'"}`,
		)), nil
	}
	return withRunnerTestUsage(schema.AssistantMessage("工单 TKT-999 当前状态为 处理中。", nil)), nil
}

func (m *scriptedSQLConversationModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func toolNameInList(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestConversationRunnerSelectsAndExecutesReadonlySQLTool(t *testing.T) {
	sqlDataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	schemaCatalog, err := NewSearchSchemaCatalogTool(&countingSchemaCatalogSearcher{})
	if err != nil {
		t.Fatal(err)
	}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, SchemaCatalog: schemaCatalog, ReadonlyQuery: readonlyQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &scriptedSQLState{}
	modelInstance := &scriptedSQLConversationModel{state: state}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: modelInstance, ToolCatalog: catalog,
		SystemInstruction: "conversation SQL fixture",
		ModelProvider:     "fixture",
		ModelID:           "fixture-v1",
		PromptVersion:     "conversation-test-v1",
		Logger:            zap.NewNop(),
		MaxContextRunes:   conversation.MaxContentRunes,
		SQLDataSourceID:   sqlDataSourceID,
	})
	if err != nil {
		t.Fatal(err)
	}

	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	current := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "查询工单 TKT-999 的实时状态",
	}
	request := conversation.AgentRequest{
		Conversation: conversation.Conversation{ID: conversationID, UserID: userID, Status: conversation.StatusActive},
		UserMessage:  current,
		History:      []conversation.Message{current},
	}
	ctx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if !strings.Contains(response.Content, "处理中") {
		t.Fatalf("answer must be based on the executed query result: %q", response.Content)
	}
	calls, gotID, query, seenAccess, seenAccessOK := executor.snapshot()
	if calls != 1 || gotID != sqlDataSourceID {
		t.Fatalf("executor call = %d/%s, want granted data source", calls, gotID)
	}
	if !strings.Contains(query, "SELECT") {
		t.Fatalf("executor received a non-SQL payload: %q", query)
	}
	if !seenAccessOK || !seenAccess.Allows(agentruntime.PermissionSQLRead) ||
		!slices.Contains(seenAccess.Grants().DataSourceIDs(), sqlDataSourceID) {
		t.Fatalf("tool did not observe the authoritative Conversation RunAccess: %+v ok=%v",
			seenAccess, seenAccessOK)
	}
	// 模型首轮可见 Schema 必须包含 SQL 两件套；turn_context 必须位于当前 user
	// 原文尾部且携带授权只读 dataSourceId。
	modelInstance.mu.Lock()
	defer modelInstance.mu.Unlock()
	if len(modelInstance.schemas) == 0 ||
		!toolNameInList(modelInstance.schemas[0], ToolSearchSchemaCatalog) ||
		!toolNameInList(modelInstance.schemas[0], ToolExecuteReadonlyQuery) {
		t.Fatalf("model-visible schemas = %v, want SQL tools", modelInstance.schemas)
	}
	firstInput := strings.Join(modelInstance.inputs[0], "\n")
	if !strings.Contains(firstInput, "</turn_context>") ||
		!strings.Contains(firstInput, `"dataSourceId":"`+sqlDataSourceID.String()+`"`) {
		t.Fatalf("turn_context missing from current user message: %q", firstInput)
	}
	userIndex := -1
	for index, message := range modelInstance.inputs[0] {
		if strings.HasPrefix(message, string(schema.User)+"\x00") {
			userIndex = index
		}
	}
	if userIndex < 0 {
		t.Fatal("no user message in model input")
	}
	userMessage := modelInstance.inputs[0][userIndex]
	if !strings.HasPrefix(userMessage, string(schema.User)+"\x00\x00查询工单 TKT-999 的实时状态") ||
		!strings.HasSuffix(userMessage, "</turn_context>") {
		t.Fatalf("turn_context must be appended after the original user text: %q", userMessage)
	}
}

func TestConversationRunnerBlocksSecondReadonlyQueryRecoverably(t *testing.T) {
	runner, executor, modelState := newDuplicateReadonlyQueryRunnerForTest(t, 8)
	request, ctx := conversationRunnerRequest(nil)
	response, err := runner.Respond(ctx, request)
	if err != nil {
		t.Fatalf("Respond() must recover from a duplicate readonly query request: %v", err)
	}
	if !strings.Contains(response.Content, "首次查询") {
		t.Fatalf("response = %q", response.Content)
	}
	if response.RunObservation == nil || response.RunObservation.ToolCalls != 2 {
		t.Fatalf("run observation = %+v, want two requested Tool calls", response.RunObservation)
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("readonly executor calls = %d, want exactly 1", calls)
	}

	modelState.mu.Lock()
	defer modelState.mu.Unlock()
	blockedResultSeen := false
	for _, input := range modelState.inputs {
		for _, message := range input {
			if message.Role == schema.Tool && message.ToolName == ToolExecuteReadonlyQuery &&
				strings.Contains(message.Content, `"status":"blocked"`) &&
				strings.Contains(message.Content, `"errorType":"tool_run_limit_exhausted"`) {
				blockedResultSeen = true
			}
		}
	}
	if !blockedResultSeen {
		t.Fatal("model did not receive a structured recoverable result for the second readonly query")
	}
}

func TestConversationRunnerDuplicateReadonlyQueryFailsClosedWhenToolBudgetIsExhausted(t *testing.T) {
	runner, executor, _ := newDuplicateReadonlyQueryRunnerForTest(t, 1)
	request, ctx := conversationRunnerRequest(nil)
	_, err := runner.Respond(ctx, request)
	if !errors.Is(err, ErrToolCallBudgetExhausted) {
		t.Fatalf("Respond() error = %v, want ErrToolCallBudgetExhausted", err)
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("readonly executor calls = %d, want exactly 1", calls)
	}
}

func newDuplicateReadonlyQueryRunnerForTest(
	t *testing.T,
	maxToolCalls int,
) (*ConversationRunner, *countingReadonlyQueryExecutor, *duplicateReadonlyQueryState) {
	t.Helper()
	sqlDataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, ReadonlyQuery: readonlyQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelState := &duplicateReadonlyQueryState{}
	modelInstance := &duplicateReadonlyQueryModel{state: modelState}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: modelInstance, ToolCatalog: catalog,
		SystemInstruction: "conversation SQL single-query policy fixture",
		ModelProvider:     "fixture",
		ModelID:           "fixture-v1",
		PromptVersion:     "conversation-test-v1",
		Logger:            zap.NewNop(),
		MaxToolCalls:      maxToolCalls,
		MaxContextRunes:   conversation.MaxContentRunes,
		SQLDataSourceID:   sqlDataSourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, executor, modelState
}

func TestConversationRunnerKeepsPriorSQLPromptContextByteStableAcrossTurns(t *testing.T) {
	sqlDataSourceID := uuid.New()
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{},
		SchemaCatalog: mustSchemaCatalogToolForTest(t),
		ReadonlyQuery: mustReadonlyQueryToolForTest(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &conversationRunnerModelState{finalContent: "已处理。"}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel:         &conversationRunnerTestModel{state: state},
		ToolCatalog:       catalog,
		SystemInstruction: "conversation SQL prompt stability fixture",
		ModelProvider:     "fixture",
		ModelID:           "fixture-v1",
		PromptVersion:     "conversation-test-v1",
		Logger:            zap.NewNop(),
		MaxContextRunes:   conversation.MaxContentRunes,
		SQLDataSourceID:   sqlDataSourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	userID, conversationID := uuid.New(), uuid.New()
	conversationItem := conversation.Conversation{
		ID: conversationID, UserID: userID, Status: conversation.StatusActive,
	}
	firstUser := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: "查询这张工单的实时状态",
	}
	firstCtx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: firstUser.ID,
		Actor: conversation.Actor{UserID: userID},
	})
	firstResponse, err := runner.Respond(firstCtx, conversation.AgentRequest{
		Conversation: conversationItem, UserMessage: firstUser,
		History: []conversation.Message{firstUser},
	})
	if err != nil {
		t.Fatalf("first Respond(): %v", err)
	}
	assistant := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 2,
		Role: conversation.MessageRoleAssistant, Content: firstResponse.Content,
	}
	secondUser := conversation.Message{
		ID: uuid.New(), ConversationID: conversationID, Seq: 3,
		Role: conversation.MessageRoleUser, Content: "继续",
	}
	secondCtx := conversation.WithCommandContext(context.Background(), conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: secondUser.ID,
		Actor: conversation.Actor{UserID: userID},
	})
	if _, err = runner.Respond(secondCtx, conversation.AgentRequest{
		Conversation: conversationItem, UserMessage: secondUser,
		History: []conversation.Message{firstUser, assistant, secondUser},
	}); err != nil {
		t.Fatalf("second Respond(): %v", err)
	}

	state.mu.Lock()
	inputs := append([][]string(nil), state.inputs...)
	state.mu.Unlock()
	if len(inputs) != 2 {
		t.Fatalf("captured model calls = %d, want 2", len(inputs))
	}
	findUser := func(messages []string, content string) string {
		prefix := string(schema.User) + "\x00\x00" + content
		for _, message := range messages {
			if strings.HasPrefix(message, prefix) {
				return message
			}
		}
		return ""
	}
	firstRendered := findUser(inputs[0], firstUser.Content)
	historicalRendered := findUser(inputs[1], firstUser.Content)
	if firstRendered == "" || historicalRendered == "" {
		t.Fatalf("first user prompt missing: current=%q historical=%q", firstRendered, historicalRendered)
	}
	if firstRendered != historicalRendered {
		t.Fatalf("prior SQL user prompt changed after the next turn:\ncurrent=%q\nhistorical=%q",
			firstRendered, historicalRendered)
	}
	if !strings.Contains(historicalRendered, "<turn_context>") ||
		!strings.Contains(historicalRendered, `"dataSourceId":"`+sqlDataSourceID.String()+`"`) {
		t.Fatalf("historical SQL prompt context = %q", historicalRendered)
	}
}

func TestConversationRunnerRunAccessDerivedFromResolvedProfileNames(t *testing.T) {
	// 只装配 ExternalCase + 只读查询（无 knowledge/web/memory/catalog）。
	// RunAccess 必须只从 ResolveProfile 的 ModelVisibleNames 推导：配置了
	// SQLDataSourceID -> sql.read；消息无 case/task/attachment 引用 -> 绝不
	// 授予 case.read/task.read/attachment.read/knowledge.read/web.read/memory.read。
	sqlDataSourceID := uuid.New()
	executor := &countingReadonlyQueryExecutor{}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, ReadonlyQuery: readonlyQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &scriptedSQLState{}
	modelInstance := &scriptedSQLConversationModel{state: state}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: modelInstance, ToolCatalog: catalog,
		SystemInstruction: "conversation SQL derivation fixture",
		ModelProvider:     "fixture",
		ModelID:           "fixture-v1",
		PromptVersion:     "conversation-test-v1",
		Logger:            zap.NewNop(),
		MaxContextRunes:   conversation.MaxContentRunes,
		SQLDataSourceID:   sqlDataSourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ctx := conversationRunnerRequest(nil)
	if _, err := runner.Respond(ctx, request); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	calls, gotID, _, seenAccess, seenAccessOK := executor.snapshot()
	if calls != 1 || gotID != sqlDataSourceID {
		t.Fatalf("executor call = %d/%s, want granted data source", calls, gotID)
	}
	if !seenAccessOK || !seenAccess.Allows(agentruntime.PermissionSQLRead) {
		t.Fatalf("executor did not observe sql.read: %+v ok=%v", seenAccess, seenAccessOK)
	}
	for _, permission := range []agentruntime.Permission{
		agentruntime.PermissionCaseRead, agentruntime.PermissionTaskRead,
		agentruntime.PermissionAttachmentRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionWebRead, agentruntime.PermissionMemoryRead,
		agentruntime.PermissionDiagnosisCreate,
	} {
		if seenAccess.Allows(permission) {
			t.Fatalf("RunAccess derived from resolved Profile must not grant %s: %v",
				permission, seenAccess.Permissions().Values())
		}
	}
}

func TestConversationRunnerWithoutSQLGrantRejectsQueryWithZeroExecutorCalls(t *testing.T) {
	executor := &countingReadonlyQueryExecutor{}
	readonlyQuery, err := NewExecuteReadonlyQueryTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversationDefaultToolCatalog(context.Background(), DefaultToolCatalogDependencies{
		ExternalCases: runnerTestCaseGetter{}, ReadonlyQuery: readonlyQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &scriptedSQLState{}
	modelInstance := &scriptedSQLConversationModel{state: state}
	runner, err := NewConversationRunner(ConversationRunnerConfig{
		ChatModel: modelInstance, ToolCatalog: catalog,
		SystemInstruction: "conversation SQL rejection fixture",
		ModelProvider:     "fixture",
		ModelID:           "fixture-v1",
		PromptVersion:     "conversation-test-v1",
		Logger:            zap.NewNop(),
		MaxContextRunes:   conversation.MaxContentRunes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 未配置 SQLDataSourceID：SQL Tool 仍在固定 Schema 中，但执行必须被
	// RunAccess（缺 sql.read）拒绝，executor 零调用。
	request, ctx := conversationRunnerRequest(nil)
	_, err = runner.Respond(ctx, request)
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("Respond() error = %v, want ErrToolNotAllowed", err)
	}
	if calls, _, _, _, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
	modelInstance.mu.Lock()
	defer modelInstance.mu.Unlock()
	if len(modelInstance.schemas) == 0 || !slices.Contains(modelInstance.schemas[0], ToolExecuteReadonlyQuery) {
		t.Fatalf("SQL Tool missing from stable schema: %v", modelInstance.schemas)
	}
}
