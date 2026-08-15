package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	"github.com/chitandabb/GoAgent/internal/resilience"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

func skillRuntimeForSelectionTest(t *testing.T) *mesagent.NativeSkillRuntime {
	t.Helper()
	root := filepath.Join("..", "..", "..", "config", "skills")
	runtime, err := mesagent.NewNativeSkillRuntime(context.Background(), root)
	if err != nil {
		t.Fatalf("NewNativeSkillRuntime(%s): %v", root, err)
	}
	return runtime
}

// normalizeNames 复制并排序名称列表，用于跨源比较。ToolProfile 内部名称是
// 排序的，而 Skill Middleware 可能把 skill 追加到尾部；不修改生产 Tool
// 顺序，只规范化测试中的比较视图。
func normalizeNames(names []string) []string {
	normalized := append([]string(nil), names...)
	slices.Sort(normalized)
	return normalized
}

// declaredProfileNames 读取 Profile 声明名单（A 源）：catalog.ResolveProfile
// 返回的 ModelVisibleNames。它来自 Catalog 绑定的不可变 ToolProfile。
func declaredProfileNames(t *testing.T, catalog *mesagent.ToolCatalog) []string {
	t.Helper()
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile(diagnosis-default): %v", err)
	}
	return normalizeNames(resolved.ModelVisibleNames)
}

// assembleSelectionSchema 复现 observeToolSelection 的 experiment 装配
// （B 源）：ToolAuthorizationMiddleware 注入 Catalog-owned Tool，真实 Eino
// Skill Middleware 追加真实 skill，返回最终真正传给模型的 Schema 名单。
// 与 declaredProfileNames 不同源：前者来自 Profile 声明，后者来自
// Middleware 链的实际输出。调用方必须传入已绑定合法 RunAccess 的 Context
// （Middleware 校验 RunAccess 存在，装配不执行任何 Tool）。
func assembleSelectionSchema(
	t *testing.T,
	catalog *mesagent.ToolCatalog,
	authorization *mesagent.ToolAuthorizationMiddleware,
	skillMiddleware adk.ChatModelAgentMiddleware,
	ctx context.Context,
) []string {
	t.Helper()
	_, authorizedCtx, err := authorization.BeforeAgent(ctx, &adk.ChatModelAgentContext{Tools: nil})
	if err != nil {
		t.Fatalf("BeforeAgent(authorization): %v", err)
	}
	_, finalCtx, err := skillMiddleware.BeforeAgent(ctx, authorizedCtx)
	if err != nil {
		t.Fatalf("BeforeAgent(skill): %v", err)
	}
	names, err := mesagent.ToolNamesFromTools(context.Background(), finalCtx.Tools)
	if err != nil {
		t.Fatalf("ToolNamesFromTools: %v", err)
	}
	return normalizeNames(names)
}

// TestSelectionCatalogBindsDiagnosisProfile 证明 experiment 侧 Catalog 绑定
// 固定 diagnosis-default Profile：Profile 声明名单（A 源）与 Middleware 链
// 实际输出（B 源）完全一致，且最终模型 Schema 不随 RunAccess 变化。该测试
// 不调用任何模型或 Provider。
func TestSelectionCatalogBindsDiagnosisProfile(t *testing.T) {
	skillRuntime := skillRuntimeForSelectionTest(t)
	catalog, wideCatalog, err := buildSelectionCatalogs(context.Background(), nil, skillRuntime)
	if err != nil {
		t.Fatalf("buildSelectionCatalogs: %v", err)
	}
	_ = wideCatalog
	if got := catalog.BoundProfileID(); got != agentruntime.ToolProfileDiagnosis {
		t.Fatalf("selection catalog bound profile = %q, want diagnosis-default", got)
	}
	authorization, err := mesagent.NewToolAuthorizationMiddleware(catalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	declared := declaredProfileNames(t, catalog)
	if len(declared) == 0 {
		t.Fatal("declared profile names are empty")
	}
	baseActual := assembleSelectionSchema(t, catalog, authorization, skillRuntime.Middleware,
		selectionRunAccessForTest(t, mesagent.ToolSelectionTicket))
	if len(baseActual) == 0 {
		t.Fatal("assembled model schema is empty")
	}
	// 跨源断言：Profile 声明名单必须等于 Middleware 链实际装配的名单。
	if !slices.Equal(declared, baseActual) {
		t.Fatalf("declared profile names %v != actual assembled schema %v", declared, baseActual)
	}

	for _, kind := range []mesagent.ToolSelectionScope{
		mesagent.ToolSelectionTicket, mesagent.ToolSelectionGitHub, mesagent.ToolSelectionSQL,
	} {
		actual := assembleSelectionSchema(t, catalog, authorization, skillRuntime.Middleware,
			selectionRunAccessForTest(t, kind))
		if !slices.Equal(actual, baseActual) {
			t.Fatalf("scope %s changed the final model schema: %v vs %v", kind, actual, baseActual)
		}
	}
}

// TestSelectionFinalSchemaSkillOwnership 验证：最终模型 Schema 中 skill 恰好
// 一次且由真实 Middleware 追加（Catalog 不伪造）；read_skill_reference 存在；
// Profile 声明名单与最终 Schema 完全一致（跨源比较）。
func TestSelectionFinalSchemaSkillOwnership(t *testing.T) {
	skillRuntime := skillRuntimeForSelectionTest(t)
	catalog, wideCatalog, err := buildSelectionCatalogs(context.Background(), nil, skillRuntime)
	if err != nil {
		t.Fatalf("buildSelectionCatalogs: %v", err)
	}
	_ = wideCatalog
	authorization, err := mesagent.NewToolAuthorizationMiddleware(catalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("NewToolAuthorizationMiddleware: %v", err)
	}
	declared := declaredProfileNames(t, catalog)
	actual := assembleSelectionSchema(t, catalog, authorization, skillRuntime.Middleware,
		selectionRunAccessForTest(t, mesagent.ToolSelectionTicket))

	if !slices.Equal(declared, actual) {
		t.Fatalf("declared profile names %v != actual assembled schema %v", declared, actual)
	}
	skillCount := 0
	for _, name := range actual {
		if name == mesagent.ToolSkill {
			skillCount++
		}
	}
	if skillCount != 1 {
		t.Fatalf("final model schema contains skill %d times, want exactly 1: %v", skillCount, actual)
	}
	if !slices.Contains(actual, mesagent.ToolReadSkillReference) {
		t.Fatalf("read_skill_reference missing from final schema: %v", actual)
	}
	// Catalog 不伪造 skill：直接解析 Profile 的 Tools 不含它。
	resolved, err := catalog.ResolveProfile(context.Background(), agentruntime.ToolProfileDiagnosis)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	for _, current := range resolved.Tools {
		info, infoErr := current.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("Tool.Info: %v", infoErr)
		}
		if info.Name == mesagent.ToolSkill {
			t.Fatalf("catalog resolved a fake skill Tool: %v", resolved.ModelVisibleNames)
		}
	}
}

// TestSelectionSchemaComparisonDetectsProfileDeclarationDrift 证明跨源比较
// 不是恒等比较：删除 Profile 声明（skill）后，声明名单与 Middleware 实际
// 输出必然不一致，测试逻辑能够真实失败。middleware-owned 的 skill 由 Eino
// Skill Middleware 无条件追加，只有 Profile 声明包含它时两源才一致。
func TestSelectionSchemaComparisonDetectsProfileDeclarationDrift(t *testing.T) {
	skillRuntime := skillRuntimeForSelectionTest(t)
	ctx := context.Background()
	registration := mesagent.ToolRegistration{
		Tool: skillRuntime.ReferenceTool, FailurePolicy: resilience.PolicyBestEffort,
	}
	withSkill, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis,
		[]string{mesagent.ToolReadSkillReference, mesagent.ToolSkill})
	if err != nil {
		t.Fatalf("NewToolProfile(with skill): %v", err)
	}
	withoutSkill, err := agentruntime.NewToolProfile(agentruntime.ToolProfileDiagnosis,
		[]string{mesagent.ToolReadSkillReference})
	if err != nil {
		t.Fatalf("NewToolProfile(without skill): %v", err)
	}
	withCatalog, err := mesagent.NewToolCatalog(ctx, registration)
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	if err := withCatalog.BindProfile(withSkill, []string{mesagent.ToolSkill}); err != nil {
		t.Fatalf("BindProfile(with skill): %v", err)
	}
	withoutCatalog, err := mesagent.NewToolCatalog(ctx, registration)
	if err != nil {
		t.Fatalf("NewToolCatalog: %v", err)
	}
	if err := withoutCatalog.BindProfile(withoutSkill, []string{mesagent.ToolSkill}); err != nil {
		t.Fatalf("BindProfile(without skill): %v", err)
	}
	scope := selectionRunAccessForTest(t, mesagent.ToolSelectionTicket)
	for _, current := range []struct {
		name    string
		catalog *mesagent.ToolCatalog
	}{
		{name: "with skill declared", catalog: withCatalog},
		{name: "without skill declared", catalog: withoutCatalog},
	} {
		authorization, authErr := mesagent.NewToolAuthorizationMiddleware(current.catalog, agentruntime.ToolProfileDiagnosis)
		if authErr != nil {
			t.Fatalf("%s: NewToolAuthorizationMiddleware: %v", current.name, authErr)
		}
		declared := declaredProfileNames(t, current.catalog)
		actual := assembleSelectionSchema(t, current.catalog, authorization, skillRuntime.Middleware, scope)
		matches := slices.Equal(declared, actual)
		if current.name == "with skill declared" && !matches {
			t.Fatalf("with skill declared, declared %v must equal actual %v", declared, actual)
		}
		if current.name == "without skill declared" && matches {
			t.Fatalf("without skill declared, declared %v must NOT equal actual %v (drift must be detected)", declared, actual)
		}
	}
}

func selectionRunAccessForTest(t *testing.T, _ mesagent.ToolSelectionScope) context.Context {
	t.Helper()
	ctx, err := withSelectionRunAccess(context.Background())
	if err != nil {
		t.Fatalf("withSelectionRunAccess: %v", err)
	}
	return ctx
}

func selectionChatModelsForTest() config.ChatModelConfig {
	active := config.ChatModelProfileConfig{
		Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
		APIKeyEnv: "MESGUARD_STEPFUN_API_KEY", Model: "step-3.7-flash",
		ReasoningEffort: "high", TimeoutMillis: 120_000,
		ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
		PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
	return config.ChatModelConfig{
		ActiveProfileName: "stepfun-main",
		Profiles:          map[string]config.ChatModelProfileConfig{"stepfun-main": active},
	}
}

// TestPrepareToolSelectionModelProfileOverridesEffort 验证变换顺序：high ->
// low，且最终 Profile 写回配置。
func TestPrepareToolSelectionModelProfileOverridesEffort(t *testing.T) {
	models := selectionChatModelsForTest()
	profile, fingerprint, err := prepareToolSelectionModelProfile(models)
	if err != nil {
		t.Fatalf("prepareToolSelectionModelProfile(): %v", err)
	}
	if profile.ReasoningEffort != "low" {
		t.Fatalf("final reasoningEffort = %q, want low", profile.ReasoningEffort)
	}
	if written := models.Profiles[models.ActiveProfileName].ReasoningEffort; written != "low" {
		t.Fatalf("profile written back to config has reasoningEffort = %q, want low", written)
	}
	if fingerprint == "" {
		t.Fatal("fingerprint must be non-empty")
	}
}

// TestPrepareToolSelectionModelProfileFingerprintMatchesFinalProfile 验证指纹
// 等于最终 Profile 的指纹、与修改前 Profile 的指纹不同，且最终 Profile 携带
// 评测实际使用的全部参数（ReasoningEffort、Temperature=0、
// MaxOutputTokens=toolSelectionMaxTokens）。
func TestPrepareToolSelectionModelProfileFingerprintMatchesFinalProfile(t *testing.T) {
	models := selectionChatModelsForTest()
	original, err := models.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	originalFingerprint, err := original.PromptProfileFingerprint(models.ActiveProfileName)
	if err != nil {
		t.Fatalf("original PromptProfileFingerprint(): %v", err)
	}
	profile, fingerprint, err := prepareToolSelectionModelProfile(models)
	if err != nil {
		t.Fatalf("prepareToolSelectionModelProfile(): %v", err)
	}
	finalFingerprint, err := profile.PromptProfileFingerprint(models.ActiveProfileName)
	if err != nil {
		t.Fatalf("final PromptProfileFingerprint(): %v", err)
	}
	if fingerprint != finalFingerprint {
		t.Fatalf("recorded fingerprint %q != final profile fingerprint %q", fingerprint, finalFingerprint)
	}
	if fingerprint == originalFingerprint {
		t.Fatalf("recorded fingerprint must differ from the pre-transformation profile fingerprint %q", originalFingerprint)
	}
	if profile.ReasoningEffort != "low" {
		t.Fatalf("final reasoningEffort = %q, want low", profile.ReasoningEffort)
	}
	if profile.Temperature == nil || *profile.Temperature != 0 {
		t.Fatalf("final profile temperature = %v, want 0", profile.Temperature)
	}
	if profile.MaxOutputTokens != toolSelectionMaxTokens {
		t.Fatalf("final profile maxOutputTokens = %d, want %d", profile.MaxOutputTokens, toolSelectionMaxTokens)
	}
}

// TestPrepareToolSelectionModelProfileKeepsEmptyEffort 验证未声明
// ReasoningEffort 时保持为空，不无条件改写为 low；Temperature 与
// MaxOutputTokens 仍按评测参数设置。
func TestPrepareToolSelectionModelProfileKeepsEmptyEffort(t *testing.T) {
	models := selectionChatModelsForTest()
	profile := models.Profiles[models.ActiveProfileName]
	profile.ReasoningEffort = ""
	models.Profiles[models.ActiveProfileName] = profile
	finalProfile, _, err := prepareToolSelectionModelProfile(models)
	if err != nil {
		t.Fatalf("prepareToolSelectionModelProfile(): %v", err)
	}
	if finalProfile.ReasoningEffort != "" {
		t.Fatalf("empty reasoningEffort must stay empty, got %q", finalProfile.ReasoningEffort)
	}
	if finalProfile.Temperature == nil || *finalProfile.Temperature != 0 {
		t.Fatalf("final profile temperature = %v, want 0", finalProfile.Temperature)
	}
	if finalProfile.MaxOutputTokens != toolSelectionMaxTokens {
		t.Fatalf("final profile maxOutputTokens = %d, want %d", finalProfile.MaxOutputTokens, toolSelectionMaxTokens)
	}
}

// 实现身份解析已提取到共享 internal/evaluationidentity 包（fail-closed 语义
// 由该包自身的 7 个测试覆盖：dirty 检测、status 失败非 clean、status 失败 +
// allow-dirty 保留已知 revision、clean 树、rev-parse 失败 unknown、formal
// 拒绝 unknown/dirty、allow-dirty 接受 unknown）。本命令不再保留本地副本。

// selectionEvalModelStub 是单 case 评测的脚本化模型：第一次调用（base
// prompt 测量）返回 800 tokens，后续调用返回 1000 tokens，使
// ToolSchemaPromptTokens = 200 满足 v3 校准校验。每次调用都选择
// read_external_case 且带完整 Provider usage。
type selectionEvalModelStub struct {
	calls atomic.Int32
}

func (m *selectionEvalModelStub) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	promptTokens := 1000
	if m.calls.Add(1) == 1 {
		promptTokens = 800
	}
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "ok",
		ToolCalls: []schema.ToolCall{{
			Index: intPtr(0),
			Function: schema.FunctionCall{
				Name: mesagent.ToolReadExternalCase, Arguments: "{}",
			},
		}},
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens: promptTokens, CompletionTokens: 50, TotalTokens: promptTokens + 50,
			},
		},
	}, nil
}

func (m *selectionEvalModelStub) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *selectionEvalModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func intPtr(value int) *int {
	return &value
}

func selectionTestConfig() config.Config {
	return config.Config{
		Models: config.ModelsConfig{Chat: config.ChatModelConfig{
			Enabled:           true,
			ActiveProfileName: "stepfun-main",
			Profiles: map[string]config.ChatModelProfileConfig{
				"stepfun-main": {
					Provider: "stepfun", BaseURL: "https://api.stepfun.com/step_plan/v1",
					APIKeyEnv: "MESGUARD_STEPFUN_API_KEY",
					Model:     "step-3.7-flash", ReasoningEffort: "low",
					TimeoutMillis:       120_000,
					ContextWindowTokens: 131_072, MaxOutputTokens: 4096,
					PromptSafetyMarginTokens: 2048, PromptSafetyMarginRatio: 0.05,
					TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
				},
			},
		}},
		GitHubMCP: config.GitHubMCPConfig{Enabled: true},
		Agent: config.AgentConfig{
			SkillsDirectory: filepath.Join("..", "..", "..", "config", "skills"),
		},
	}
}

func writeSelectionDatasetForTest(t *testing.T) string {
	t.Helper()
	dataset := filepath.Join(t.TempDir(), "dataset.jsonl")
	line := `{"datasetVersion":"tools-v1","caseId":"case-1","scope":"github","userQuery":"查找代码","expectedTool":"read_external_case"}` + "\n"
	if err := os.WriteFile(dataset, []byte(line), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	return dataset
}

// TestSelectionPreflightFailClosedBeforeProvider 证明可比性校验在创建任何
// Provider 之前 fail-closed：verify 返回漂移错误时，runWithDependencies 立即
// 失败且 newChatModel factory 从未被调用（factory.calls == 0）。
func TestSelectionPreflightFailClosedBeforeProvider(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "obs.jsonl")
	summary := filepath.Join(outputDir, "summary.json")

	factoryCalls := atomic.Int32{}
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		return nil, errors.New("factory must not be reached before comparability preflight")
	}
	deps.verifySelectionComparability = func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error) {
		return mesagent.ToolSelectionComparability{}, errors.New("drifted shared schema")
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
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

// TestSelectionPreflightAcceptsRealArms 端到端证明：真实两臂装配与真实
// VerifyToolSelectionComparability 通过 preflight，随后只创建一次 Provider
// 并产出 v3 观测：两臂共享 comparison 身份、v3 合同、正确的 Profile ID。
func TestSelectionPreflightAcceptsRealArms(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "obs.jsonl")
	summary := filepath.Join(outputDir, "summary.json")

	factoryCalls := atomic.Int32{}
	modelStub := &selectionEvalModelStub{}
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		return modelStub, nil
	}
	// 真实 VerifyToolSelectionComparability（默认依赖）。

	if err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
	}, zap.NewNop(), deps); err != nil {
		t.Fatalf("runWithDependencies: %v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("newChatModel factory called %d times, want exactly 1", factoryCalls.Load())
	}

	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 2 {
		t.Fatalf("observations = %d lines, want 2 (wide + production)", len(lines))
	}
	var wide, production mesagent.ToolSelectionObservation
	for _, line := range lines {
		var observation mesagent.ToolSelectionObservation
		if err := json.Unmarshal([]byte(line), &observation); err != nil {
			t.Fatalf("unmarshal observation: %v", err)
		}
		switch observation.Variant {
		case mesagent.ToolSelectionWide:
			wide = observation
		case mesagent.ToolSelectionProduction:
			production = observation
		default:
			t.Fatalf("unexpected variant %q", observation.Variant)
		}
	}
	if wide.Variant == "" || production.Variant == "" {
		t.Fatal("both arms must be recorded")
	}
	for name, observation := range map[string]mesagent.ToolSelectionObservation{
		"wide": wide, "production": production,
	} {
		if observation.ObservationSchemaVersion != mesagent.ToolSelectionObservationV3 {
			t.Fatalf("%s observationSchemaVersion = %q, want %q", name,
				observation.ObservationSchemaVersion, mesagent.ToolSelectionObservationV3)
		}
		if observation.ComparisonFingerprint == "" ||
			!strings.HasPrefix(observation.ComparisonFingerprint, "sha256:") {
			t.Fatalf("%s comparisonFingerprint = %q", name, observation.ComparisonFingerprint)
		}
		if len(observation.SharedToolNames) == 0 || len(observation.BaselineOnlyToolNames) == 0 {
			t.Fatalf("%s comparison Tool lists are empty: shared=%v baselineOnly=%v", name,
				observation.SharedToolNames, observation.BaselineOnlyToolNames)
		}
		if observation.ToolCallCount != 1 || observation.SelectedTool != mesagent.ToolReadExternalCase {
			t.Fatalf("%s selection = %d/%q", name, observation.ToolCallCount, observation.SelectedTool)
		}
		if err := observation.Validate(); err != nil {
			t.Fatalf("%s Validate: %v", name, err)
		}
	}
	if wide.ComparisonFingerprint != production.ComparisonFingerprint {
		t.Fatal("both arms must record the same comparisonFingerprint")
	}
	if !slices.Equal(wide.SharedToolNames, production.SharedToolNames) ||
		!slices.Equal(wide.BaselineOnlyToolNames, production.BaselineOnlyToolNames) {
		t.Fatal("both arms must record the same comparison Tool lists")
	}
	if wide.ToolProfileID != string(agentruntime.ToolProfileEvaluationWide) {
		t.Fatalf("wide toolProfileId = %q, want evaluation-wide-v2", wide.ToolProfileID)
	}
	if production.ToolProfileID != string(agentruntime.ToolProfileDiagnosis) {
		t.Fatalf("production toolProfileId = %q, want diagnosis-default", production.ToolProfileID)
	}
	// wide 必须包含 production 的全部 Tool 且严格更多（并集合同）。
	for _, name := range production.ModelVisibleNames {
		if !slices.Contains(wide.ModelVisibleNames, name) {
			t.Fatalf("wide arm is missing production Tool %q: %v", name, wide.ModelVisibleNames)
		}
	}
	if len(wide.ModelVisibleNames) <= len(production.ModelVisibleNames) {
		t.Fatalf("wide arm must be a strict superset: wide=%d production=%d",
			len(wide.ModelVisibleNames), len(production.ModelVisibleNames))
	}
	if _, err := os.Stat(summary); err != nil {
		t.Fatalf("summary output missing: %v", err)
	}
}

func TestObserveToolSelectionRejectsSchemaDriftFromPreflight(t *testing.T) {
	cfg := selectionTestConfig()
	skillRuntime, err := mesagent.NewNativeSkillRuntime(context.Background(), cfg.Agent.SkillsDirectory)
	if err != nil {
		t.Fatalf("NewNativeSkillRuntime: %v", err)
	}
	assembly, err := assembleSelectionEval(
		context.Background(), nil, skillRuntime, mesagent.VerifyToolSelectionComparability,
	)
	if err != nil {
		t.Fatalf("assembleSelectionEval: %v", err)
	}
	assembly.productionSchemaHash = "sha256:" + strings.Repeat("f", 64)
	profile, fingerprint, err := prepareToolSelectionModelProfile(cfg.Models.Chat)
	if err != nil {
		t.Fatalf("prepareToolSelectionModelProfile: %v", err)
	}

	_, err = observeToolSelection(
		context.Background(), &selectionEvalModelStub{}, assembly,
		mesagent.ToolSelectionCase{
			DatasetVersion: "tool-selection-v1", CaseID: "case-1",
			Scope: mesagent.ToolSelectionTicket, UserQuery: "读取工单",
			ExpectedTool: mesagent.ToolReadExternalCase,
		},
		mesagent.ToolSelectionProduction, 800,
		evaluationidentity.Identity{Revision: "git:test", Dirty: false}, fingerprint, profile,
	)
	if err == nil || !strings.Contains(err.Error(), "preflight Tool Schema") {
		t.Fatalf("observeToolSelection error = %v, want preflight Schema drift rejection", err)
	}
}

// TestSelectionDefaultOutputPathsAreV3 证明 v3 输出资产默认名，避免覆盖
// v1/v2 历史资产。
func TestSelectionDefaultOutputPathsAreV3(t *testing.T) {
	if toolSelectionObservationsOutput != "testdata/tool-selection-v3.observations.jsonl" {
		t.Fatalf("default observations output = %q", toolSelectionObservationsOutput)
	}
	if toolSelectionSummaryOutput != "testdata/tool-selection-v3.summary.json" {
		t.Fatalf("default summary output = %q", toolSelectionSummaryOutput)
	}
}
