package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type pairedEvaluationModelStub struct{}

func pairedComparabilityForTest() mesagent.ToolSelectionComparability {
	return mesagent.ToolSelectionComparability{
		ComparisonFingerprint: "sha256:" + strings.Repeat("c", 64),
		SharedToolNames:       []string{mesagent.ToolReadExternalCase, mesagent.ToolSkill},
		BaselineOnlyToolNames: []string{"search_code"},
	}
}

func (pairedEvaluationModelStub) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (pairedEvaluationModelStub) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := (pairedEvaluationModelStub{}).Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (pairedEvaluationModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return pairedEvaluationModelStub{}, nil
}

func TestBuildPairedEvaluationRunUsesStrictEvidenceReportPolicy(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{
		SkillsDirectory: filepath.Join("..", "..", "..", "config", "skills"),
		MaxAgentRuns:    2, MaxToolCalls: 8, MaxEvidenceItems: 16,
		MaxTotalTokens: 16000, TimeoutMillis: 60000,
	}}
	prompts := config.AgentPrompts{
		SystemInstruction:         "system",
		BaselineInstruction:       "baseline",
		ReportContractInstruction: "report contract",
	}
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}

	orchestrator, fingerprint, err := buildPairedEvaluationRun(
		context.Background(), cfg, prompts, pairedEvaluationModelStub{},
		zap.NewNop(), mesagent.EvaluationExperiment, "tool-selection", assembly,
	)
	if err != nil {
		t.Fatalf("buildPairedEvaluationRun: %v", err)
	}
	if orchestrator == nil || len(fingerprint) != 64 {
		t.Fatalf("orchestrator=%v fingerprint=%q", orchestrator, fingerprint)
	}
}

func TestNewEvaluationObservationUsesConfiguredPromptVersion(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{PromptVersion: "diagnosis-v7"}}
	profile := config.ChatModelProfileConfig{Provider: "stepfun", Model: "step-3.7-flash", ReasoningEffort: "medium"}
	observation := observationFromResult(
		mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"},
		mesagent.EvaluationExperiment,
		cfg,
		"stepfun-main",
		profile,
		mesagent.OrchestrationResult{},
		time.Second,
		strings.Repeat("2", 64),
		pairedComparabilityForTest(),
		evaluationidentity.Identity{Revision: "git:test-revision", Dirty: false},
		strings.Repeat("a", 64),
	)
	if observation.PromptVersion != "diagnosis-v7" {
		t.Fatalf("PromptVersion = %q, want diagnosis-v7", observation.PromptVersion)
	}
}

func TestNewEvaluationObservationRecordsV3ArmSpecificIdentity(t *testing.T) {
	base := mesagent.EvaluationCase{DatasetVersion: "test-v1", CaseID: "case-1"}
	cfg := pairedEvalTestConfig()
	profile, profileErr := cfg.Models.Chat.ActiveProfile()
	if profileErr != nil {
		t.Fatalf("ActiveProfile(): %v", profileErr)
	}
	identity := evaluationidentity.Identity{Revision: "git:test-revision", Dirty: false}
	experimentResult := mesagent.OrchestrationResult{
		SelectedSkill: mesagent.SkillTicketDiagnosis,
		AllowedTools:  []string{mesagent.ToolReadExternalCase, mesagent.ToolSkill},
		Report: mesagent.StructuredReport{
			ConclusionStatus: mesagent.ConclusionProbable,
		},
	}
	baselineResult := mesagent.OrchestrationResult{
		SelectedSkill: mesagent.SkillTicketDiagnosis,
		AllowedTools:  []string{mesagent.ToolReadExternalCase, mesagent.ToolSkill, "search_code"},
		Report: mesagent.StructuredReport{
			ConclusionStatus: mesagent.ConclusionProbable,
		},
	}
	experiment := observationFromResult(
		base, mesagent.EvaluationExperiment, cfg, cfg.Models.Chat.ActiveProfileName, profile, experimentResult,
		time.Second, strings.Repeat("2", 64), pairedComparabilityForTest(), identity, strings.Repeat("a", 64),
	)
	baseline := observationFromResult(
		base, mesagent.EvaluationBaseline, cfg, cfg.Models.Chat.ActiveProfileName, profile, baselineResult,
		time.Second, strings.Repeat("1", 64), pairedComparabilityForTest(), identity, strings.Repeat("a", 64),
	)
	if experiment.ObservationSchemaVersion != mesagent.EvaluationObservationV3 ||
		baseline.ObservationSchemaVersion != mesagent.EvaluationObservationV3 {
		t.Fatal("observations must carry the v3 observation schema version")
	}
	if experiment.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
		t.Fatalf("experiment toolProfileId = %q, want diagnosis-default", experiment.ToolProfileID)
	}
	if baseline.ToolProfileID != string(agentruntime.ToolProfileEvaluationWide) {
		t.Fatalf("baseline toolProfileId = %q, want evaluation-wide-v2", baseline.ToolProfileID)
	}
	if experiment.ToolSchemaFingerprint != strings.Repeat("2", 64) ||
		baseline.ToolSchemaFingerprint != strings.Repeat("1", 64) {
		t.Fatal("toolSchemaFingerprint must be arm-specific")
	}
	if experiment.ModelProfileFingerprint != strings.Repeat("a", 64) ||
		experiment.ImplementationRevision != "git:test-revision" || experiment.ImplementationDirty {
		t.Fatalf("experiment identity fields = %+v", experiment)
	}
	if experiment.ComparisonFingerprint != pairedComparabilityForTest().ComparisonFingerprint ||
		!slices.Equal(experiment.SharedToolNames, pairedComparabilityForTest().SharedToolNames) ||
		!slices.Equal(experiment.BaselineOnlyToolNames, pairedComparabilityForTest().BaselineOnlyToolNames) {
		t.Fatalf("experiment comparison identity = %+v", experiment)
	}
	if err := experiment.Validate(); err != nil {
		t.Fatalf("experiment Validate: %v", err)
	}
	if err := baseline.Validate(); err != nil {
		t.Fatalf("baseline Validate: %v", err)
	}
}

func TestEvidenceGateObservationRecordsInvocationFailureWithoutQualityLabels(t *testing.T) {
	cfg := config.Config{
		Agent: config.AgentConfig{PromptVersion: "diagnosis-v1"},
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			ActiveProfileName: "fixture",
			Profiles: map[string]config.ChatModelProfileConfig{
				"fixture": {Provider: "stepfun", Model: "step-3.7-flash", ReasoningEffort: "medium"},
			},
		}},
	}
	observation := evidenceGateObservationFromResult(
		mesagent.EvaluationCase{DatasetVersion: "gate-v1", CaseID: "case-1"},
		mesagent.EvaluationExperiment,
		cfg,
		"fixture",
		cfg.Models.Chat.Profiles["fixture"],
		"sha256:pair",
		mesagent.OrchestrationResult{},
		time.Second,
		errors.New("provider unavailable"),
	)
	if err := observation.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if observation.Completed || observation.QualityReviewed ||
		observation.ErrorType != "provider_or_orchestration_error" ||
		len(observation.DegradationReasons) != 1 {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestValidatePairedProviderBudgetRequiresExplicitBoundedAuthorization(t *testing.T) {
	if _, err := validatePairedProviderBudget("tool-selection", 3, 2, 8, 16000, false, 3, 60, 96000); err == nil {
		t.Fatal("Provider run was accepted without explicit authorization")
	}
	if _, err := validatePairedProviderBudget("tool-selection", 3, 2, 8, 16000, true, 3, 59, 96000); err == nil {
		t.Fatal("Provider call upper bound exceeded the authorization")
	}
	budget, err := validatePairedProviderBudget("tool-selection", 3, 2, 8, 16000, true, 3, 60, 96000)
	if err != nil {
		t.Fatalf("validatePairedProviderBudget: %v", err)
	}
	if budget.Cases != 3 || budget.ProviderCalls != 60 || budget.TotalTokens != 96000 {
		t.Fatalf("budget = %+v", budget)
	}
}

// TestPairedCaseCapSeparatesEvidenceGateFromToolSelection 证明固定 reviewed
// Case 上限只约束 evidence-gate：tool-selection 的 Case 数只受数据集大小、
// 显式 max-cases、max-provider-calls 与 max-provider-tokens 限制；evidence-gate
// 超 30 个 reviewed Case（无论来自数据集的 case 数还是显式 max-cases）都拒绝。
func TestPairedCaseCapSeparatesEvidenceGateFromToolSelection(t *testing.T) {
	const cases = 31 // > evidenceGateReviewedCaseTargetForProviderRun
	budget, err := validatePairedProviderBudget("tool-selection", cases, 2, 8, 16000, true, cases, cases*40, cases*64000)
	if err != nil {
		t.Fatalf("tool-selection with the same Case count and explicit budget must be allowed, got %v", err)
	}
	if budget.Cases != cases {
		t.Fatalf("tool-selection budget cases = %d, want %d", budget.Cases, cases)
	}
	if _, err := validatePairedProviderBudget("evidence-gate", cases, 2, 8, 16000, true, cases, cases*40, cases*64000); err == nil ||
		!strings.Contains(err.Error(), "reviewed") {
		t.Fatalf("evidence-gate dataset above the fixed reviewed target must be refused, got %v", err)
	}
	if _, err := validatePairedProviderBudget("evidence-gate", 30, 2, 8, 16000, true, cases, cases*40, cases*64000); err == nil ||
		!strings.Contains(err.Error(), "reviewed") {
		t.Fatalf("evidence-gate explicit max-cases above the fixed reviewed target must be refused, got %v", err)
	}
	budget30, err := validatePairedProviderBudget("evidence-gate", 30, 2, 8, 16000, true, 30, 30*40, 30*64000)
	if err != nil {
		t.Fatalf("evidence-gate exactly at the fixed reviewed target must be allowed, got %v", err)
	}
	if budget30.Cases != 30 {
		t.Fatalf("evidence-gate budget cases = %d, want 30", budget30.Cases)
	}
}

func pairedEvalTestConfig() config.Config {
	return config.Config{
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			Enabled:           true,
			ActiveProfileName: "stepfun-main",
			Profiles: map[string]config.ChatModelProfileConfig{
				"stepfun-main": {
					Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
					APIKeyEnv: "MESGUARD_STEPFUN_API_KEY",
					Model:     "step-3.7-flash", ReasoningEffort: "medium",
					TimeoutMillis:       120_000,
					ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
					PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
					TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
				},
				"opencode-deepseek-main": {
					Provider: "opencode-go", BaseURL: "https://opencode.deepseek.com/api/v1",
					APIKeyEnv: "MESGUARD_OPENCODE_DEEPSEEK_API_KEY",
					Model:     "deepseek-v4-flash", ReasoningEffort: "low",
					TimeoutMillis:       120_000,
					ContextWindowTokens: 262_144, MaxOutputTokens: 8192,
					PromptSafetyMarginTokens: 4096, PromptSafetyMarginRatio: 0.05,
					TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
				},
			},
		}},
		GitHubMCP: config.GitHubMCPConfig{Enabled: true},
		Agent: config.AgentConfig{
			SkillsDirectory:        filepath.Join("..", "..", "..", "config", "skills"),
			PromptVersion:          "diagnosis-v7",
			SystemPromptFile:       filepath.Join("..", "..", "..", "config", "prompts", "diagnosis-system.md"),
			BaselinePromptFile:     filepath.Join("..", "..", "..", "config", "prompts", "evaluation-baseline.md"),
			ReportContractFile:     filepath.Join("..", "..", "..", "config", "prompts", "report-contract.md"),
			ConversationPromptFile: filepath.Join("..", "..", "..", "config", "prompts", "conversation-system.md"),
			MaxAgentRuns:           2, MaxToolCalls: 8, MaxEvidenceItems: 16,
			MaxTotalTokens: 16000, TimeoutMillis: 60000,
		},
	}
}

func writePairedDatasetForTest(t *testing.T) string {
	t.Helper()
	dataset := filepath.Join(t.TempDir(), "dataset.jsonl")
	line := `{"datasetVersion":"dev-v1","caseId":"ticket","taskType":"diagnosis","userQuery":"读取并诊断工单","expectedSkill":"ticket-diagnosis","expectedFirstTool":"read_external_case","expectedTools":["read_external_case"],"requiredEvidence":["ticket"],"expectedRootCause":"status-sync","acceptableConclusionStatuses":["probable"]}` + "\n"
	if err := os.WriteFile(dataset, []byte(line), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	return dataset
}

// writePairedTaggedDatasetForTest 写带 tags 的评测 Case 数据集（SQL/GitHub 授权
// 规则由 Case tags 派生）。
func writePairedTaggedDatasetForTest(t *testing.T, tags ...string) string {
	t.Helper()
	tagsJSON := "[]"
	if len(tags) > 0 {
		encoded, err := json.Marshal(tags)
		if err != nil {
			t.Fatalf("marshal tags: %v", err)
		}
		tagsJSON = string(encoded)
	}
	dataset := filepath.Join(t.TempDir(), "dataset-tagged.jsonl")
	line := `{"datasetVersion":"dev-v1","caseId":"ticket","taskType":"diagnosis","userQuery":"读取并诊断工单","expectedSkill":"ticket-diagnosis","expectedFirstTool":"read_external_case","expectedTools":["read_external_case"],"requiredEvidence":["ticket"],"expectedRootCause":"status-sync","acceptableConclusionStatuses":["probable"],"tags":` + tagsJSON + `}` + "\n"
	if err := os.WriteFile(dataset, []byte(line), 0o600); err != nil {
		t.Fatalf("write tagged dataset: %v", err)
	}
	return dataset
}

// pairedPromptCapturingModel 捕获每次模型请求的 system 消息，用于证明 paired
// CLI 的真实 Invoke Context 同时携带 RunAccess 与由
// BuildDiagnosisRunContext 生成的 task_context。
type pairedPromptCapturingModel struct {
	mu      sync.Mutex
	systems [][]string
}

func (m *pairedPromptCapturingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *pairedPromptCapturingModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	var systems []string
	for _, message := range input {
		if message != nil && message.Role == schema.System {
			systems = append(systems, message.Content)
		}
	}
	m.mu.Lock()
	m.systems = append(m.systems, systems)
	m.mu.Unlock()
	return schema.AssistantMessage("ok", nil), nil
}

func (m *pairedPromptCapturingModel) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *pairedPromptCapturingModel) snapshot() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([][]string, len(m.systems))
	for index, systems := range m.systems {
		result[index] = append([]string(nil), systems...)
	}
	return result
}

// TestPairedCeilingProfileIsDiagnosisNotWide 证明 ceiling 的 ProfileToolNames
// 来自真实生产 diagnosis-default Profile（经 Catalog 解析），而不是
// evaluation-wide-v2：production 名单必须与 wide 名单不同，且差异正好是
// baseline-only Tool。
func TestPairedCeilingProfileIsDiagnosisNotWide(t *testing.T) {
	cfg := pairedEvalTestConfig()
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	production, err := pairedProductionProfileToolNames(context.Background(), assembly.productionCatalog)
	if err != nil {
		t.Fatalf("pairedProductionProfileToolNames: %v", err)
	}
	wide, err := assembly.wideCatalog.ResolveProfile(context.Background(), agentruntime.ToolProfileEvaluationWide)
	if err != nil {
		t.Fatalf("wide ResolveProfile: %v", err)
	}
	sortedProduction := append([]string(nil), production...)
	sortedWide := append([]string(nil), wide.ModelVisibleNames...)
	slices.Sort(sortedProduction)
	slices.Sort(sortedWide)
	if slices.Equal(sortedProduction, sortedWide) {
		t.Fatal("ceiling ProfileToolNames must be the production diagnosis-default Profile, not the wide contract")
	}
	for _, name := range sortedWide {
		if !slices.Contains(sortedProduction, name) {
			if !slices.Contains(assembly.comparability.BaselineOnlyToolNames, name) {
				t.Fatalf("wide-only Tool %q is not declared baseline-only by the comparison contract", name)
			}
		}
	}
	for _, name := range sortedProduction {
		if slices.Contains(assembly.comparability.BaselineOnlyToolNames, name) {
			t.Fatalf("production Profile must not contain baseline-only Tool %q", name)
		}
	}
}

// TestBuildPairedCaseRunContextPerCaseProjection 证明每 Case 只构造一次
// BuildDiagnosisRunContext 输出：SQL Case 的 task_context 携带被授权数据源
// （合法 id/role/read_only），非 SQL Case 不泄漏任何 SQL 数据源；两类的
// externalCaseId 与 effectivePermissions（生产 diagnosis-default ceiling 交集）
// 均正确且非空。
func TestBuildPairedCaseRunContextPerCaseProjection(t *testing.T) {
	cfg := pairedEvalTestConfig()
	cfg.SQLServer = config.SQLServerConfig{
		Enabled: true, ID: "44444444-4444-4444-4444-444444444444",
	}
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	profileToolNames, err := pairedProductionProfileToolNames(context.Background(), assembly.productionCatalog)
	if err != nil {
		t.Fatalf("pairedProductionProfileToolNames: %v", err)
	}

	sqlCase := mesagent.EvaluationCase{CaseID: "ticket-sql", Tags: []string{"sql-enabled"}}
	// 真实部署中 production diagnosis-default Profile 含 SQL Tool；本测试装配的
	// catalog 未注入 SQL Tool（nil 依赖），因此显式补齐真实生产名单里的 SQL
	// Tool 名称，使 ceiling 等价于真实部署。
	sqlProfileToolNames := append(append([]string(nil), profileToolNames...),
		mesagent.ToolSearchSchemaCatalog, mesagent.ToolExecuteReadonlyQuery, mesagent.ToolDatabaseObjectDefinition)
	sqlRunContext, err := buildPairedCaseRunContext(sqlCase, cfg, sqlProfileToolNames)
	if err != nil {
		t.Fatalf("buildPairedCaseRunContext(sql): %v", err)
	}
	sqlTaskContext := sqlRunContext.TaskContext()
	if strings.TrimSpace(sqlTaskContext) == "" {
		t.Fatal("SQL case task_context must be non-empty")
	}
	if !strings.Contains(sqlTaskContext, `"externalCaseId":"11111111-1111-1111-1111-111111111111"`) {
		t.Fatalf("SQL task_context must carry the current external case id:\n%s", sqlTaskContext)
	}
	if !strings.Contains(sqlTaskContext, `"sql.read"`) || !strings.Contains(sqlTaskContext, `"case.read"`) {
		t.Fatalf("SQL task_context effectivePermissions must include sql.read and case.read:\n%s", sqlTaskContext)
	}
	if !strings.Contains(sqlTaskContext, `"id":"44444444-4444-4444-4444-444444444444"`) {
		t.Fatalf("SQL task_context must project the authorized data source:\n%s", sqlTaskContext)
	}
	if !strings.Contains(sqlTaskContext, `"role":"case_source"`) || !strings.Contains(sqlTaskContext, `"safetyMode":"read_only"`) {
		t.Fatalf("SQL task_context data source must carry a legal role and read_only safety mode:\n%s", sqlTaskContext)
	}
	if !sqlRunContext.Access().Allows(agentruntime.PermissionSQLRead) {
		t.Fatal("SQL case RunAccess must include sql.read")
	}

	plainCase := mesagent.EvaluationCase{CaseID: "ticket-plain"}
	plainRunContext, err := buildPairedCaseRunContext(plainCase, cfg, profileToolNames)
	if err != nil {
		t.Fatalf("buildPairedCaseRunContext(plain): %v", err)
	}
	plainTaskContext := plainRunContext.TaskContext()
	if strings.TrimSpace(plainTaskContext) == "" {
		t.Fatal("non-SQL case task_context must be non-empty")
	}
	if strings.Contains(plainTaskContext, "dataSources") {
		t.Fatalf("non-SQL case must not leak SQL data sources:\n%s", plainTaskContext)
	}
	if strings.Contains(plainTaskContext, "sql.read") {
		t.Fatalf("non-SQL case must not carry sql.read permission:\n%s", plainTaskContext)
	}
	if !strings.Contains(plainTaskContext, `"case.read"`) {
		t.Fatalf("non-SQL task_context must include case.read:\n%s", plainTaskContext)
	}
	if plainRunContext.Access().Allows(agentruntime.PermissionSQLRead) {
		t.Fatal("non-SQL case RunAccess must not include sql.read")
	}
}

// TestPairedInvokeContextCarriesRunAccessAndTaskContext 是任务一的回归红测试：
// paired CLI 的真实 Invoke Context 必须同时携带 RunAccess 与由
// BuildDiagnosisRunContext 生成的 task_context，且两臂最终 system message 字节
// 级一致。旧实现只注入 RunAccess，system 中没有 task_context，本测试失败。
func TestPairedInvokeContextCarriesRunAccessAndTaskContext(t *testing.T) {
	dataset := writePairedDatasetForTest(t)
	output := filepath.Join(t.TempDir(), "obs.jsonl")
	capturing := &pairedPromptCapturingModel{}
	deps := defaultPairedEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		return capturing, nil
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-allow-dirty",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "100", "-max-provider-tokens", "3000000",
	}, zap.NewNop(), deps)
	if err != nil {
		t.Fatalf("runWithDependencies: %v", err)
	}
	turns := capturing.snapshot()
	if len(turns) < 2 {
		t.Fatalf("captured %d model turns, want at least 2 (one per arm)", len(turns))
	}
	firstSystems := turns[0]
	if len(firstSystems) != 1 {
		t.Fatalf("first arm captured %d system messages, want 1", len(firstSystems))
	}
	experimentSystem := firstSystems[0]
	if !strings.Contains(experimentSystem, "<task_context>") {
		t.Fatalf("Invoke Context must carry the diagnosis task_context; missing from system:\n%s", experimentSystem)
	}
	if !strings.Contains(experimentSystem, `"externalCaseId":"11111111-1111-1111-1111-111111111111"`) {
		t.Fatalf("task_context must carry the current external case id:\n%s", experimentSystem)
	}
	if strings.Count(experimentSystem, "<task_context>") != 1 {
		t.Fatalf("task_context must appear exactly once, got %d:\n%s", strings.Count(experimentSystem, "<task_context>"), experimentSystem)
	}
	// task_context 位于应用侧 system 指令尾部（块之后只允许框架级静态后缀）。
	blockIndex := strings.Index(experimentSystem, "<task_context>")
	remainder := experimentSystem[blockIndex:]
	if !strings.HasPrefix(remainder, "<task_context>\n{") {
		t.Fatalf("task_context is not at the system tail:\n%s", experimentSystem)
	}
	for index := 1; index < len(turns); index++ {
		if len(turns[index]) != 1 {
			t.Fatalf("turn %d captured %d system messages, want 1", index, len(turns[index]))
		}
		if turns[index][0] != experimentSystem {
			t.Fatalf("turn %d system message differs between arms:\n--- first ---\n%s\n--- turn %d ---\n%s",
				index, experimentSystem, index, turns[index][0])
		}
	}
}

// TestPairedPreflightFailClosedBeforeProvider 证明两臂可比性校验在创建任何
// Provider 之前 fail-closed：verify 返回漂移错误时，runWithDependencies 立即
// 失败且 newChatModel factory 从未被调用（factory.calls == 0）。
func TestPairedPreflightFailClosedBeforeProvider(t *testing.T) {
	dataset := writePairedDatasetForTest(t)
	output := filepath.Join(t.TempDir(), "obs.jsonl")

	factoryCalls := atomic.Int32{}
	deps := defaultPairedEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		return nil, errors.New("factory must not be reached before comparability preflight")
	}
	deps.verifyPairedComparability = func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error) {
		return mesagent.ToolSelectionComparability{}, errors.New("drifted shared schema")
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-allow-dirty",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "100", "-max-provider-tokens", "1000000",
	}, zap.NewNop(), deps)
	if err == nil || !strings.Contains(err.Error(), "comparability preflight") {
		t.Fatalf("runWithDependencies error = %v, want comparability preflight rejection", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("newChatModel factory called %d times before comparability preflight, want 0", factoryCalls.Load())
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("observations output must not be created on preflight failure: %v", err)
	}
}

// TestPairedProviderBudgetGateCoversBothComparisons 证明统一成本闸门对
// tool-selection 与 evidence-gate 两种 comparison 同样生效：缺授权或带预算
// 旗标缺失时，newChatModel 与 connectGitHub 调用数均为 0（fail-closed 先于
// 任何 Provider/远端连接创建）。
func TestPairedProviderBudgetGateCoversBothComparisons(t *testing.T) {
	for _, comparison := range []string{"tool-selection", "evidence-gate"} {
		t.Run(comparison, func(t *testing.T) {
			dataset := writePairedDatasetForTest(t)
			output := filepath.Join(t.TempDir(), "obs.jsonl")
			var modelCalls atomic.Int32
			var githubCalls atomic.Int32
			deps := defaultPairedEvalDependencies()
			deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
			deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
				modelCalls.Add(1)
				return nil, errors.New("factory must not be reached before budget gate")
			}
			deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
				githubCalls.Add(1)
				return nil, errors.New("GitHub must not be reached before budget gate")
			}
			err := runWithDependencies(context.Background(), []string{
				"-dataset", dataset, "-output", output, "-allow-dirty",
				"-comparison", comparison,
			}, zap.NewNop(), deps)
			if err == nil || !strings.Contains(err.Error(), "allow-provider-calls") {
				t.Fatalf("missing authorization must be refused for %s, got %v", comparison, err)
			}
			if modelCalls.Load() != 0 {
				t.Fatalf("newChatModel called %d times, want 0", modelCalls.Load())
			}
			if githubCalls.Load() != 0 {
				t.Fatalf("connectGitHub called %d times, want 0", githubCalls.Load())
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("observations output must not be created on budget rejection: %v", err)
			}
		})
	}
}

// TestPairedProviderBudgetRejectsCallAndTokenOverrun 证明两种 comparison 下
// 调用/Token 上界超限同样 fail-closed 且 factory 调用数为 0。
func TestPairedProviderBudgetRejectsCallAndTokenOverrun(t *testing.T) {
	dataset := writePairedDatasetForTest(t)
	scenarios := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "call overrun", args: []string{
			"-allow-provider-calls", "-max-cases", "1",
			"-max-provider-calls", "19", "-max-provider-tokens", "1000000",
		}, wantErr: "max-provider-calls"},
		{name: "token overrun", args: []string{
			"-allow-provider-calls", "-max-cases", "1",
			"-max-provider-calls", "100", "-max-provider-tokens", "31999",
		}, wantErr: "max-provider-tokens"},
	}
	for _, comparison := range []string{"tool-selection", "evidence-gate"} {
		for _, scenario := range scenarios {
			t.Run(comparison+"/"+scenario.name, func(t *testing.T) {
				output := filepath.Join(t.TempDir(), "obs.jsonl")
				var modelCalls atomic.Int32
				deps := defaultPairedEvalDependencies()
				deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
				deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
					modelCalls.Add(1)
					return nil, errors.New("factory must not be reached before budget gate")
				}
				deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
					return &githubmcp.Connection{}, nil
				}
				args := append([]string{"-dataset", dataset, "-output", output, "-allow-dirty",
					"-comparison", comparison}, scenario.args...)
				err := runWithDependencies(context.Background(), args, zap.NewNop(), deps)
				if err == nil || !strings.Contains(err.Error(), scenario.wantErr) {
					t.Fatalf("runWithDependencies error = %v, want %q", err, scenario.wantErr)
				}
				if modelCalls.Load() != 0 {
					t.Fatalf("newChatModel called %d times, want 0", modelCalls.Load())
				}
			})
		}
	}
}

// TestPairedPreflightAcceptsRealArms 证明真实两臂装配与真实
// VerifyToolSelectionComparability 通过 preflight：wide 臂（evaluation-wide-
// v2 并集，经 Skill Middleware）是 production 的严格 Schema 超集，共享
// Tool 完全一致。
func TestPairedPreflightAcceptsRealArms(t *testing.T) {
	cfg := pairedEvalTestConfig()
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	if !strings.HasPrefix(assembly.comparability.ComparisonFingerprint, "sha256:") {
		t.Fatalf("comparisonFingerprint = %q", assembly.comparability.ComparisonFingerprint)
	}
	if len(assembly.comparability.SharedToolNames) == 0 || len(assembly.comparability.BaselineOnlyToolNames) == 0 {
		t.Fatalf("comparison Tool lists are empty: shared=%v baselineOnly=%v",
			assembly.comparability.SharedToolNames, assembly.comparability.BaselineOnlyToolNames)
	}
	if !slices.Contains(assembly.comparability.SharedToolNames, mesagent.ToolSkill) {
		t.Fatalf("sharedToolNames must include the Middleware-owned skill: %v", assembly.comparability.SharedToolNames)
	}
	if assembly.productionCatalog == nil || assembly.wideCatalog == nil || assembly.skillRuntime == nil ||
		len(assembly.productionSchemaFingerprint) != 64 || len(assembly.wideSchemaFingerprint) != 64 {
		t.Fatalf("preflight assembly is incomplete: %+v", assembly)
	}
}

// TestPreparePairedProfileSelectsNamedProfile 证明 -profile 非空时精确选择
// 命名 Profile，activeProfile 与配置 Map 完全不动；指纹基于最终副本与实际
// Profile 名；-reasoning-effort 只作用于局部副本。
func TestPreparePairedProfileSelectsNamedProfile(t *testing.T) {
	cfg := pairedEvalTestConfig()
	profileName, profile, fingerprint, err := preparePairedProfile(cfg.Models.Chat, "opencode-deepseek-main", "high")
	if err != nil {
		t.Fatalf("preparePairedProfile(named): %v", err)
	}
	if profileName != "opencode-deepseek-main" {
		t.Fatalf("profileName = %q, want opencode-deepseek-main", profileName)
	}
	if profile.Provider != "opencode-go" || profile.ReasoningEffort != "high" {
		t.Fatalf("named profile = %+v", profile)
	}
	active, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	if active.Provider != "stepfun" || active.ReasoningEffort != "medium" {
		t.Fatalf("activeProfile must stay untouched: %+v", active)
	}
	finalFingerprint, err := profile.PromptProfileFingerprint(profileName)
	if err != nil {
		t.Fatalf("final PromptProfileFingerprint(): %v", err)
	}
	if fingerprint != finalFingerprint {
		t.Fatalf("recorded fingerprint %q != final named profile fingerprint %q", fingerprint, finalFingerprint)
	}
	if _, _, _, err := preparePairedProfile(cfg.Models.Chat, "missing-profile", ""); err == nil {
		t.Fatal("unknown named profile must be rejected")
	}
}

// TestPairedProfileReachesFactoryWithoutTouchingActiveProfile 证明端到端
// -profile 直接到达 Provider Factory（收到最终 profileName + 副本），且
// activeProfile 未被修改。
func TestPairedProfileReachesFactoryWithoutTouchingActiveProfile(t *testing.T) {
	dataset := writePairedDatasetForTest(t)
	output := filepath.Join(t.TempDir(), "obs.jsonl")
	var factoryCalls atomic.Int32
	deps := defaultPairedEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return pairedEvalTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(_ context.Context, profileName string, profile config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		if profileName != "opencode-deepseek-main" {
			return nil, fmt.Errorf("factory received profile %q, want the explicitly named profile", profileName)
		}
		if profile.Provider != "opencode-go" || profile.Model != "deepseek-v4-flash" {
			return nil, fmt.Errorf("factory received wrong named profile: %+v", profile)
		}
		return pairedEvaluationModelStub{}, nil
	}
	// 真实 VerifyToolSelectionComparability（默认依赖）保持两臂观测
	// AllowedTools 与 comparison 合同一致。
	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-allow-dirty",
		"-profile", "opencode-deepseek-main",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "100", "-max-provider-tokens", "1000000",
	}, zap.NewNop(), deps)
	if err != nil {
		t.Fatalf("runWithDependencies: %v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("newChatModel called %d times, want 1", factoryCalls.Load())
	}
	cfg := pairedEvalTestConfig()
	active, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	if active.Provider != "stepfun" || active.ReasoningEffort != "medium" {
		t.Fatalf("activeProfile must stay untouched: %+v", active)
	}
}

// TestBuildPairedEvaluationRunRejectsSchemaDriftFromPreflight proves the
// runtime Runner is bound to the exact Tool Schema accepted by the preflight,
// rather than silently rebuilding and running a different contract.
func TestBuildPairedEvaluationRunRejectsSchemaDriftFromPreflight(t *testing.T) {
	cfg := pairedEvalTestConfig()
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	assembly, err := verifyPairedArmsComparability(
		context.Background(), cfg, nil, nil, nil, nil,
		mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("verifyPairedArmsComparability: %v", err)
	}
	assembly.productionSchemaFingerprint = strings.Repeat("f", 64)

	_, _, err = buildPairedEvaluationRun(
		context.Background(), cfg, prompts, pairedEvaluationModelStub{}, zap.NewNop(),
		mesagent.EvaluationExperiment, "tool-selection", assembly,
	)
	if err == nil || !strings.Contains(err.Error(), "preflight Tool Schema") {
		t.Fatalf("buildPairedEvaluationRun error = %v, want preflight Schema drift rejection", err)
	}
}
