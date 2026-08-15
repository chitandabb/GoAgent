package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/diagnosisworker"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const diagnosisWorkerTestSQLSourceID = "8d5c67dc-4c09-4ee5-9e80-4d822303dc35"

func TestNewDiagnosisInvestigationPolicyBuilderFollowsDeploymentConfig(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.ID = diagnosisWorkerTestSQLSourceID
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	cfg.GitHubMCP.Enabled = true
	cfg.WebSearch.Enabled = true
	builder, err := newDiagnosisInvestigationPolicyBuilder(cfg)
	if err != nil {
		t.Fatalf("newDiagnosisInvestigationPolicyBuilder: %v", err)
	}
	caseID, attachmentID, unrelatedSourceID := uuid.New(), uuid.New(), uuid.New()
	sqlSourceID := uuid.MustParse(diagnosisWorkerTestSQLSourceID)
	policy, err := builder.Build(diagnosis.InvestigationPolicyInput{
		ExternalCaseID: caseID,
		DataSourceIDs:  []uuid.UUID{sqlSourceID, unrelatedSourceID},
		AttachmentIDs:  []uuid.UUID{attachmentID},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []agentruntime.Permission{
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionWebRead, agentruntime.PermissionCodeRead,
		agentruntime.PermissionSQLRead, agentruntime.PermissionAttachmentRead,
	} {
		if !policy.Permissions().Has(want) {
			t.Fatalf("policy missing %q: %v", want, policy.Permissions().Values())
		}
	}
	if !policy.Grants().AllowsExternalCase(caseID) || !policy.Grants().AllowsAttachment(attachmentID) ||
		!policy.Grants().AllowsDataSource(sqlSourceID) || policy.Grants().AllowsDataSource(unrelatedSourceID) {
		t.Fatalf("policy grants = %v", policy.Grants())
	}

	// 部署关闭 SQL/Code/Web：Policy 不授予对应上限，数据源 Grant 为空。
	disabledCfg := testAgentConfig()
	disabledBuilder, err := newDiagnosisInvestigationPolicyBuilder(disabledCfg)
	if err != nil {
		t.Fatalf("disabled builder: %v", err)
	}
	disabledPolicy, err := disabledBuilder.Build(diagnosis.InvestigationPolicyInput{
		ExternalCaseID: caseID,
		DataSourceIDs:  []uuid.UUID{sqlSourceID},
	})
	if err != nil {
		t.Fatalf("disabled Build: %v", err)
	}
	for _, forbidden := range []agentruntime.Permission{
		agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead, agentruntime.PermissionWebRead,
	} {
		if disabledPolicy.Permissions().Has(forbidden) {
			t.Fatalf("disabled deployment must not grant %q", forbidden)
		}
	}
	if len(disabledPolicy.Grants().DataSourceIDs()) != 0 {
		t.Fatalf("disabled deployment granted data sources: %v", disabledPolicy.Grants().DataSourceIDs())
	}

	// SQL 启用但 allowedSchemas 为空：不授予 sql.read。
	noSchemaCfg := testAgentConfig()
	noSchemaCfg.SQLServer.Enabled = true
	noSchemaCfg.SQLServer.ID = diagnosisWorkerTestSQLSourceID
	noSchemaBuilder, err := newDiagnosisInvestigationPolicyBuilder(noSchemaCfg)
	if err != nil {
		t.Fatalf("no-schema builder: %v", err)
	}
	noSchemaPolicy, err := noSchemaBuilder.Build(diagnosis.InvestigationPolicyInput{
		ExternalCaseID: caseID, DataSourceIDs: []uuid.UUID{sqlSourceID},
	})
	if err != nil {
		t.Fatalf("no-schema Build: %v", err)
	}
	if noSchemaPolicy.Permissions().Has(agentruntime.PermissionSQLRead) ||
		len(noSchemaPolicy.Grants().DataSourceIDs()) != 0 {
		t.Fatalf("empty allowedSchemas must not grant SQL: %v", noSchemaPolicy.Permissions().Values())
	}
}

func TestNewDiagnosisInvestigationPolicyBuilderFailsClosedOnInvalidSQLID(t *testing.T) {
	cfg := testAgentConfig()
	cfg.SQLServer.Enabled = true
	cfg.SQLServer.ID = "not-a-uuid"
	cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
	if _, err := newDiagnosisInvestigationPolicyBuilder(cfg); err == nil {
		t.Fatal("newDiagnosisInvestigationPolicyBuilder accepted an invalid SQL Server data source id")
	}
}

// diagnosisExecutorCaseGetter 记录执行期 Context 的权威 RunAccess。
type diagnosisExecutorCaseGetter struct {
	mu     sync.Mutex
	item   *externalcase.ExternalCase
	access agentruntime.RunAccess
	ok     bool
}

func (s *diagnosisExecutorCaseGetter) Get(ctx context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	access, ok := agentruntime.RunAccessFromContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access, s.ok = access, ok
	if s.item == nil || s.item.ID != id {
		return nil, errors.New("case not found")
	}
	return s.item, nil
}

func (s *diagnosisExecutorCaseGetter) snapshot() (agentruntime.RunAccess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access, s.ok
}

// diagnosisExecutorChatModel 先调用 read_external_case，再返回最终答案。
type diagnosisExecutorChatModel struct {
	mu       sync.Mutex
	system   string
	caseDone bool
	caseID   string
}

func (m *diagnosisExecutorChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *diagnosisExecutorChatModel) Generate(
	_ context.Context, input []*schema.Message, _ ...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range input {
		if message != nil && message.Role == schema.System {
			m.system = message.Content
		}
	}
	if !m.caseDone {
		m.caseDone = true
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-case", Function: schema.FunctionCall{
				Name: agent.ToolReadExternalCase, Arguments: `{"externalCaseId":"` + m.caseID + `"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("已根据工单证据形成初步诊断。", nil), nil
}

func (m *diagnosisExecutorChatModel) Stream(
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

func (m *diagnosisExecutorChatModel) snapshot() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.system, m.caseDone
}

func buildDiagnosisWorkerTestRuntime(
	t *testing.T,
	cfg config.Config,
	chatModel model.ToolCallingChatModel,
	externalCases agent.ExternalCaseGetter,
	sqlTool bool,
) *agentRuntime {
	t.Helper()
	builders := agentRuntimeBuilders{
		chatModel: func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
			return chatModel, nil
		},
	}
	var sqlServer *sql.DB
	var postgresDB *gorm.DB
	if sqlTool {
		cfg.SQLServer.Enabled = true
		cfg.SQLServer.ID = diagnosisWorkerTestSQLSourceID
		cfg.SQLServer.Investigation.AllowedSchemas = []string{"dbo"}
		sqlServer = new(sql.DB)
		// schemaCatalog builder 需要非空 PostgreSQL 句柄才参与装配。
		postgresDB = &gorm.DB{}
		builders.schemaCatalog = func(*gorm.DB, uuid.UUID, *zap.Logger) (tool.BaseTool, error) {
			return agent.NewSearchSchemaCatalogTool(conversationSQLCatalogSearcherStub{})
		}
	}
	runtime, err := buildAgentRuntimeForRole(
		context.Background(), agentRuntimeRoleDiagnosis, cfg, externalCases, sqlServer, postgresDB, zap.NewNop(), builders,
	)
	if err != nil {
		t.Fatalf("buildAgentRuntimeForRole(diagnosis): %v", err)
	}
	t.Cleanup(func() { _ = runtime.close() })
	if runtime.orchestrator == nil {
		t.Fatal("diagnosis orchestrator is nil")
	}
	return runtime
}

func diagnosisWorkerTestTask(caseID uuid.UUID) diagnosisworker.Task {
	sourceID := uuid.MustParse(diagnosisWorkerTestSQLSourceID)
	return diagnosisworker.Task{
		ID: uuid.New(), CreatedBy: uuid.New(), Role: auth.RoleAnalyst,
		RequestText: "检查工单状态",
		RequestScope: map[string]any{
			diagnosis.RequestScopeKeyAllowedCapabilities: []any{"case", "knowledge", "web_search"},
		},
		CaseSnapshot: externalcase.ExternalCase{
			ID: caseID, DataSourceID: sourceID, ExternalCaseKey: "TKT-1", Title: "报工状态未更新",
			SourceFingerprint: "sha256:source",
		},
		DataSources: []diagnosisworker.DataSource{{
			ID: sourceID, Role: agent.DataSourceRoleCaseSource, SafetyMode: agent.DataSourceSafetyReadOnly,
		}},
	}
}

func TestDiagnosisWorkerExecutorBindsPolicyRunAccessAndTaskContext(t *testing.T) {
	cfg := testAgentConfig()
	caseID := uuid.New()
	getter := &diagnosisExecutorCaseGetter{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: uuid.MustParse(diagnosisWorkerTestSQLSourceID),
		ExternalCaseKey: "TKT-1", Title: "报工状态未更新", SourceFingerprint: "sha256:source",
	}}
	chatModel := &diagnosisExecutorChatModel{caseID: caseID.String()}
	runtime := buildDiagnosisWorkerTestRuntime(t, cfg, chatModel, getter, false)
	executor := diagnosisAgentExecutor{runtime: runtime}
	task := diagnosisWorkerTestTask(caseID)
	policy := mustDiagnosisPolicyForBootstrapTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID}},
	)
	task.Policy = &policy

	result, err := executor.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Orchestration.AgentRuns < 1 || len(result.Orchestration.ToolExecutions) == 0 {
		t.Fatalf("execution result = %+v", result.Orchestration)
	}
	access, ok := getter.snapshot()
	if !ok || !access.Allows(agentruntime.PermissionCaseRead) || !access.Grants().AllowsExternalCase(caseID) {
		t.Fatalf("case getter observed access = %+v ok=%t", access, ok)
	}
	if access.RuntimeKind() != agentruntime.RuntimeKindDiagnosis {
		t.Fatalf("runtime kind = %q", access.RuntimeKind())
	}
	system, _ := chatModel.snapshot()
	if !strings.Contains(system, "<task_context>") || !strings.Contains(system, caseID.String()) {
		t.Fatalf("system instruction lost task_context: %q", system)
	}
	if !strings.Contains(system, `"policySchemaVersion":1`) {
		t.Fatalf("task_context missing policySchemaVersion: %q", system)
	}
	if result.PromptVersion != cfg.Agent.PromptVersion {
		t.Fatalf("prompt version = %q, want %q", result.PromptVersion, cfg.Agent.PromptVersion)
	}
}

func TestDiagnosisWorkerExecutorFrozenPolicyDoesNotDeriveFromLegacyScope(t *testing.T) {
	cfg := testAgentConfig()
	caseID := uuid.New()
	getter := &diagnosisExecutorCaseGetter{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: uuid.MustParse(diagnosisWorkerTestSQLSourceID),
		ExternalCaseKey: "TKT-1", Title: "报工状态未更新", SourceFingerprint: "sha256:source",
	}}
	chatModel := &diagnosisExecutorChatModel{caseID: caseID.String()}
	runtime := buildDiagnosisWorkerTestRuntime(t, cfg, chatModel, getter, true)
	if !slices.Contains(runtime.diagnosisToolNames, agent.ToolSearchKnowledge) {
		t.Fatalf("fixture profile lacks the knowledge Tool: %v", runtime.diagnosisToolNames)
	}
	executor := diagnosisAgentExecutor{runtime: runtime}
	task := diagnosisWorkerTestTask(caseID)
	// 冻结 request_scope 只有 case 能力；但 frozen Policy 授予 case+knowledge。
	// 新任务必须只走 frozen Policy，绝不能退回 legacy 派生（否则会丢掉
	// knowledge.read）。
	task.RequestScope = map[string]any{
		diagnosis.RequestScopeKeyAllowedCapabilities: []any{"case"},
	}
	policy := mustDiagnosisPolicyForBootstrapTest(t,
		[]agentruntime.Permission{agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID}},
	)
	task.Policy = &policy

	result, err := executor.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Orchestration.AgentRuns < 1 {
		t.Fatalf("execution result = %+v", result.Orchestration)
	}
	access, ok := getter.snapshot()
	if !ok || !access.Allows(agentruntime.PermissionCaseRead) {
		t.Fatalf("frozen access = %+v ok=%t", access, ok)
	}
	if !access.Allows(agentruntime.PermissionKnowledgeRead) {
		t.Fatalf("frozen policy lost knowledge.read that legacy scope lacks: %v", access.Permissions().Values())
	}
}

func TestDiagnosisWorkerExecutorLegacyTaskCannotGainSQLFromCeiling(t *testing.T) {
	cfg := testAgentConfig()
	caseID := uuid.New()
	getter := &diagnosisExecutorCaseGetter{item: &externalcase.ExternalCase{
		ID: caseID, DataSourceID: uuid.MustParse(diagnosisWorkerTestSQLSourceID),
		ExternalCaseKey: "TKT-1", Title: "报工状态未更新", SourceFingerprint: "sha256:source",
	}}
	chatModel := &diagnosisExecutorChatModel{caseID: caseID.String()}
	// ceiling Profile 包含 SQL Tool（schema catalog），但旧任务 Policy=NULL。
	runtime := buildDiagnosisWorkerTestRuntime(t, cfg, chatModel, getter, true)
	if !slices.Contains(runtime.diagnosisToolNames, agent.ToolSearchSchemaCatalog) {
		t.Fatalf("fixture profile lacks the SQL Tool: %v", runtime.diagnosisToolNames)
	}
	executor := diagnosisAgentExecutor{runtime: runtime}
	task := diagnosisWorkerTestTask(caseID)
	task.Policy = nil
	// 冻结 scope 只有 case/knowledge/web_search：legacy 派生绝不能从新
	// 部署 ceiling 拿到 sql.read。
	result, err := executor.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Orchestration.AgentRuns < 1 {
		t.Fatalf("execution result = %+v", result.Orchestration)
	}
	access, ok := getter.snapshot()
	if !ok || !access.Allows(agentruntime.PermissionCaseRead) {
		t.Fatalf("legacy access = %+v ok=%t", access, ok)
	}
	// legacy 派生不能从新部署 ceiling 拿到 sql.read 权限；数据源 Grant 只对
	// SQL Tool 有意义，权限缺失时 Tool 边界仍 fail-closed（零底层调用）。
	if access.Allows(agentruntime.PermissionSQLRead) {
		t.Fatalf("legacy task gained sql.read from the ceiling: %v", access.Permissions().Values())
	}
	if access.Allows(agentruntime.PermissionCodeRead) {
		t.Fatalf("legacy task gained code.read from the ceiling: %v", access.Permissions().Values())
	}
}

func TestDiagnosisWorkerExecutorFailsClosedOnInvalidPersistedScope(t *testing.T) {
	cfg := testAgentConfig()
	runtime := buildDiagnosisWorkerTestRuntime(t, cfg, &diagnosisExecutorChatModel{}, &diagnosisExecutorCaseGetter{}, false)
	executor := diagnosisAgentExecutor{runtime: runtime}
	task := diagnosisWorkerTestTask(uuid.New())
	task.RequestScope = map[string]any{
		diagnosis.RequestScopeKeyAllowedCapabilities: []any{"case", "shell"},
	}
	if _, err := executor.Execute(context.Background(), task); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("Execute error = %v, want ErrInvalidTask", err)
	}
}

func mustDiagnosisPolicyForBootstrapTest(
	t *testing.T,
	permissions []agentruntime.Permission,
	grantsConfig agentruntime.ResourceGrantsConfig,
) agentruntime.InvestigationPolicy {
	t.Helper()
	permissionSet, err := agentruntime.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := agentruntime.NewResourceGrants(grantsConfig)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.NewInvestigationPolicy(1, permissionSet, grants)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
