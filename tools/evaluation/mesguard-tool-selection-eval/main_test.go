package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	"github.com/chitandabb/GoAgent/internal/resilience"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
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

// toolInfoFingerprintForTest 与 selectionToolInfoMetadata 同口径：json.Marshal
// 走 schema.ToolInfo 自带 MarshalJSON（保留 params/jsonschema 两种表示）。
func toolInfoFingerprintForTest(t *testing.T, info *schema.ToolInfo) string {
	t.Helper()
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal ToolInfo: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// realSelectionChatModelForTest 用真实 platformchatmodel.New 构造模型（与
// selectionEvalModelFactory 同 seam）。本测试只 WithTools、绝不 Generate；
// 选中 Profile 的 BaseURL 被覆盖为不可达地址，确保测试无法意外访问真实
// Provider。
func realSelectionChatModelForTest(t *testing.T) model.ToolCallingChatModel {
	t.Helper()
	models := selectionChatModelsForTest()
	profileName, profile, _, err := prepareToolSelectionProfile(models, "")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile: %v", err)
	}
	profile.BaseURL = "http://127.0.0.1:9/v1"
	t.Setenv("MESGUARD_STEPFUN_API_KEY", "test-key")
	instance, err := platformchatmodel.New(context.Background(), profileName, profile)
	if err != nil {
		t.Fatalf("chatmodel.New: %v", err)
	}
	if instance.Model == nil {
		t.Fatal("chatmodel.New returned nil Model")
	}
	return instance.Model
}

// TestToolSchemaHashStableAcrossRealBindingOrders 复现 f5831a4 单 Case 探针的
// 顺序依赖：真实装配得到两臂 preflight hash 后，用真实 Factory 模型按
// wide→production 顺序绑定，重新装配两臂必须与 preflight hash 一致；再按
// production→wide 逆序验证；重复绑定也必须稳定。全程不 Generate、不创建
// 真实 Provider 请求。
func TestToolSchemaHashStableAcrossRealBindingOrders(t *testing.T) {
	skillRuntime := skillRuntimeForSelectionTest(t)
	ctx := context.Background()
	assembly, err := assembleSelectionEval(ctx, nil, skillRuntime, mesagent.VerifyToolSelectionComparability)
	if err != nil {
		t.Fatalf("assembleSelectionEval: %v", err)
	}
	chatModel := realSelectionChatModelForTest(t)

	armInfos := func(authorization *mesagent.ToolAuthorizationMiddleware) ([]*schema.ToolInfo, string) {
		t.Helper()
		infos, hash, err := selectionArmTools(ctx, authorization, skillRuntime.Middleware)
		if err != nil {
			t.Fatalf("selectionArmTools: %v", err)
		}
		return infos, hash
	}
	productionInfos, productionHash := armInfos(assembly.productionAuthorization)
	wideInfos, wideHash := armInfos(assembly.wideAuthorization)
	if productionHash != assembly.productionSchemaHash || wideHash != assembly.wideSchemaHash {
		t.Fatalf("fresh arm hash must equal preflight: production %s/%s wide %s/%s",
			productionHash, assembly.productionSchemaHash, wideHash, assembly.wideSchemaHash)
	}

	// wide -> production：wide 绑定后重新装配两臂必须与 preflight 一致。
	bound, err := chatModel.WithTools(wideInfos)
	if err != nil {
		t.Fatalf("bind wide: %v", err)
	}
	if _, got := armInfos(assembly.productionAuthorization); got != assembly.productionSchemaHash {
		t.Fatalf("production arm hash drifted after wide binding: %s != preflight %s", got, assembly.productionSchemaHash)
	}
	if _, got := armInfos(assembly.wideAuthorization); got != assembly.wideSchemaHash {
		t.Fatalf("wide arm hash drifted after wide binding: %s != preflight %s", got, assembly.wideSchemaHash)
	}
	// 重复绑定也稳定。
	if _, err := bound.WithTools(wideInfos); err != nil {
		t.Fatalf("re-bind wide: %v", err)
	}
	if _, got := armInfos(assembly.productionAuthorization); got != assembly.productionSchemaHash {
		t.Fatalf("production arm hash drifted after repeated wide binding: %s != preflight %s", got, assembly.productionSchemaHash)
	}

	// production -> wide 逆序。
	if _, err := chatModel.WithTools(productionInfos); err != nil {
		t.Fatalf("bind production: %v", err)
	}
	if _, got := armInfos(assembly.wideAuthorization); got != assembly.wideSchemaHash {
		t.Fatalf("wide arm hash drifted after production binding: %s != preflight %s", got, assembly.wideSchemaHash)
	}
	if _, got := armInfos(assembly.productionAuthorization); got != assembly.productionSchemaHash {
		t.Fatalf("production arm hash drifted after production binding: %s != preflight %s", got, assembly.productionSchemaHash)
	}
}

// TestCatalogAndSkillToolSchemasStableAcrossBinding 覆盖两类 Tool 的不同风险：
// Catalog-owned Tool（InferTool 重复返回同一 ToolInfo/JSONSchema 指针）绑定
// 前后 Info/Schema/fingerprint 不变；middleware-owned skill Tool（每次 Info
// 重建 ToolInfo）多次 Info 稳定，且其他 Tool 绑定不影响它；skill 最终仍恰好
// 出现一次，不修改现有装配合同。
func TestCatalogAndSkillToolSchemasStableAcrossBinding(t *testing.T) {
	skillRuntime := skillRuntimeForSelectionTest(t)
	ctx := context.Background()
	assembly, err := assembleSelectionEval(ctx, nil, skillRuntime, mesagent.VerifyToolSelectionComparability)
	if err != nil {
		t.Fatalf("assembleSelectionEval: %v", err)
	}
	chatModel := realSelectionChatModelForTest(t)

	accessCtx, err := withSelectionRunAccess(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, authorizedCtx, err := assembly.productionAuthorization.BeforeAgent(accessCtx, &adk.ChatModelAgentContext{Tools: nil})
	if err != nil {
		t.Fatalf("BeforeAgent(authorization): %v", err)
	}
	_, finalCtx, err := assembly.skillMiddleware.BeforeAgent(accessCtx, authorizedCtx)
	if err != nil {
		t.Fatalf("BeforeAgent(skill): %v", err)
	}

	var skillTool tool.BaseTool
	var catalogTools []tool.BaseTool
	skillCount := 0
	for _, current := range finalCtx.Tools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			t.Fatalf("Info: %v", infoErr)
		}
		if info.Name == mesagent.ToolSkill {
			skillCount++
			skillTool = current
		} else {
			catalogTools = append(catalogTools, current)
		}
	}
	if skillCount != 1 || skillTool == nil {
		t.Fatalf("skill Tool must appear exactly once, got %d", skillCount)
	}
	skillHash := func() string {
		t.Helper()
		info, infoErr := skillTool.Info(ctx)
		if infoErr != nil {
			t.Fatalf("skill Info: %v", infoErr)
		}
		return toolInfoFingerprintForTest(t, info)
	}
	baselineSkillHash := skillHash()
	// middleware-owned skill Tool：多次 Info 必须稳定。
	if second := skillHash(); second != baselineSkillHash {
		t.Fatalf("skill Tool Info unstable across calls: %s != %s", second, baselineSkillHash)
	}

	for _, current := range catalogTools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			t.Fatalf("Info: %v", infoErr)
		}
		before := toolInfoFingerprintForTest(t, info)
		if _, bindErr := chatModel.WithTools([]*schema.ToolInfo{info}); bindErr != nil {
			t.Fatalf("bind %s: %v", info.Name, bindErr)
		}
		afterInfo, infoErr := current.Info(ctx)
		if infoErr != nil {
			t.Fatalf("Info after binding: %v", infoErr)
		}
		if after := toolInfoFingerprintForTest(t, afterInfo); after != before {
			t.Fatalf("catalog tool %s schema mutated by binding: %s -> %s", info.Name, before, after)
		}
		if after := skillHash(); after != baselineSkillHash {
			t.Fatalf("skill Tool schema drifted after binding %s: %s -> %s", info.Name, baselineSkillHash, after)
		}
	}
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
	named := config.ChatModelProfileConfig{
		Provider: "opencode-go", BaseURL: "https://opencode.deepseek.com/api/v1",
		APIKeyEnv: "MESGUARD_OPENCODE_DEEPSEEK_API_KEY", Model: "deepseek-v4-flash",
		ReasoningEffort: "low", TimeoutMillis: 120_000,
		ContextWindowTokens: 262_144, MaxOutputTokens: 8192,
		PromptSafetyMarginTokens: 4096, PromptSafetyMarginRatio: 0.05,
		TokenizerStrategy: config.TokenizerStrategyLocalCalibrated,
	}
	return config.ChatModelConfig{
		Enabled:           true,
		ActiveProfileName: "stepfun-main",
		Profiles: map[string]config.ChatModelProfileConfig{
			"stepfun-main":           active,
			"opencode-deepseek-main": named,
		},
	}
}

// TestPrepareToolSelectionProfileOverridesEffort 验证变换顺序：high -> low。
// 变换只作用于局部副本：prepareToolSelectionProfile 绝不写回 config Map。
func TestPrepareToolSelectionProfileOverridesEffort(t *testing.T) {
	models := selectionChatModelsForTest()
	profileName, profile, fingerprint, err := prepareToolSelectionProfile(models, "")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile(): %v", err)
	}
	if profileName != models.ActiveProfileName {
		t.Fatalf("profileName = %q, want active %q", profileName, models.ActiveProfileName)
	}
	if profile.ReasoningEffort != "low" {
		t.Fatalf("final reasoningEffort = %q, want low", profile.ReasoningEffort)
	}
	active, err := models.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	if active.ReasoningEffort != "high" {
		t.Fatalf("activeProfile must stay untouched, got reasoningEffort %q", active.ReasoningEffort)
	}
	if fingerprint == "" {
		t.Fatal("fingerprint must be non-empty")
	}
}

// TestPrepareToolSelectionProfileFingerprintMatchesFinalProfile 验证指纹等于
// 最终 Profile（副本）的指纹、与修改前 Profile 的指纹不同，且最终 Profile
// 携带评测实际使用的全部参数（ReasoningEffort、Temperature=0、
// MaxOutputTokens=toolSelectionMaxTokens）。
func TestPrepareToolSelectionProfileFingerprintMatchesFinalProfile(t *testing.T) {
	models := selectionChatModelsForTest()
	original, err := models.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	originalFingerprint, err := original.PromptProfileFingerprint(models.ActiveProfileName)
	if err != nil {
		t.Fatalf("original PromptProfileFingerprint(): %v", err)
	}
	profileName, profile, fingerprint, err := prepareToolSelectionProfile(models, "")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile(): %v", err)
	}
	finalFingerprint, err := profile.PromptProfileFingerprint(profileName)
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
	active, err := models.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	if active.ReasoningEffort != "high" || active.MaxOutputTokens != 4096 {
		t.Fatalf("activeProfile must stay untouched: %+v", active)
	}
}

// TestPrepareToolSelectionProfileKeepsEmptyEffort 验证未声明
// ReasoningEffort 时保持为空，不无条件改写为 low；Temperature 与
// MaxOutputTokens 仍按评测参数设置。
func TestPrepareToolSelectionProfileKeepsEmptyEffort(t *testing.T) {
	models := selectionChatModelsForTest()
	profile := models.Profiles[models.ActiveProfileName]
	profile.ReasoningEffort = ""
	models.Profiles[models.ActiveProfileName] = profile
	_, finalProfile, _, err := prepareToolSelectionProfile(models, "")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile(): %v", err)
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

// TestPrepareToolSelectionProfileSelectsNamedProfile 证明 -profile 非空时精确
// 选择命名 Profile（如 opencode-deepseek-main），activeProfile 完全不改；
// 指纹基于最终副本与实际 Profile 名。
func TestPrepareToolSelectionProfileSelectsNamedProfile(t *testing.T) {
	models := selectionChatModelsForTest()
	profileName, profile, fingerprint, err := prepareToolSelectionProfile(models, "opencode-deepseek-main")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile(named): %v", err)
	}
	if profileName != "opencode-deepseek-main" {
		t.Fatalf("profileName = %q, want opencode-deepseek-main", profileName)
	}
	if profile.Provider != "opencode-go" || profile.Model != "deepseek-v4-flash" {
		t.Fatalf("named profile = %+v", profile)
	}
	if profile.ReasoningEffort != "low" {
		t.Fatalf("named final reasoningEffort = %q, want low", profile.ReasoningEffort)
	}
	active, err := models.ActiveProfile()
	if err != nil {
		t.Fatalf("ActiveProfile(): %v", err)
	}
	if active.Provider != "stepfun" || active.ReasoningEffort != "high" {
		t.Fatalf("activeProfile must stay untouched: %+v", active)
	}
	finalFingerprint, err := profile.PromptProfileFingerprint(profileName)
	if err != nil {
		t.Fatalf("final PromptProfileFingerprint(): %v", err)
	}
	if fingerprint != finalFingerprint {
		t.Fatalf("recorded fingerprint %q != final named profile fingerprint %q", fingerprint, finalFingerprint)
	}
}

// TestPrepareToolSelectionProfileRejectsUnknownNamedProfile 证明未知命名
// Profile 被拒绝，不退回 activeProfile。
func TestPrepareToolSelectionProfileRejectsUnknownNamedProfile(t *testing.T) {
	models := selectionChatModelsForTest()
	if _, _, _, err := prepareToolSelectionProfile(models, "missing-profile"); err == nil {
		t.Fatal("unknown named profile must be rejected")
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
		Models:    config.ModelsConfig{Chat: selectionChatModelsForTest()},
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

func writeSelectionTwoCaseDatasetForTest(t *testing.T) string {
	t.Helper()
	dataset := filepath.Join(t.TempDir(), "dataset-two.jsonl")
	lines := `{"datasetVersion":"tools-v1","caseId":"case-1","scope":"github","userQuery":"查找代码","expectedTool":"read_external_case"}` + "\n" +
		`{"datasetVersion":"tools-v1","caseId":"case-2","scope":"github","userQuery":"再次查找代码","expectedTool":"read_external_case"}` + "\n"
	if err := os.WriteFile(dataset, []byte(lines), 0o600); err != nil {
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
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		return nil, errors.New("factory must not be reached before comparability preflight")
	}
	deps.verifySelectionComparability = func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error) {
		return mesagent.ToolSelectionComparability{}, errors.New("drifted shared schema")
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "1000000",
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
	deps.newChatModel = func(_ context.Context, profileName string, profile config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		if profileName != "stepfun-main" {
			return nil, fmt.Errorf("factory received profile %q, want stepfun-main", profileName)
		}
		if profile.ReasoningEffort != "low" || profile.Temperature == nil || *profile.Temperature != 0 ||
			profile.MaxOutputTokens != toolSelectionMaxTokens {
			return nil, fmt.Errorf("factory received untransformed profile: %+v", profile)
		}
		return modelStub, nil
	}
	// 真实 VerifyToolSelectionComparability（默认依赖）。

	if err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "1000000",
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
	_, profile, fingerprint, err := prepareToolSelectionProfile(cfg.Models.Chat, "")
	if err != nil {
		t.Fatalf("prepareToolSelectionProfile: %v", err)
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
		zap.NewNop(),
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

// TestValidateToolSelectionProviderBudget 是成本闸门的契约测试：缺授权、
// 非正预算、Case 超限、调用或 Token 硬上界超限都必须 fail-closed，且默认
// 每 Case 允许 3 次 Provider 调用（base 校准 1 次 + wide 1 次 +
// production 1 次）。Token 硬上界 = Cases x 3 x contextWindowTokens。
func TestValidateToolSelectionProviderBudget(t *testing.T) {
	if _, err := validateToolSelectionProviderBudget(1, 3, 1000, false, 1, 10, 10000); err == nil ||
		!strings.Contains(err.Error(), "allow-provider-calls") {
		t.Fatalf("missing -allow-provider-calls must be refused, got %v", err)
	}
	if _, err := validateToolSelectionProviderBudget(1, 3, 1000, true, 0, 10, 10000); err == nil {
		t.Fatal("non-positive max-cases must be refused")
	}
	if _, err := validateToolSelectionProviderBudget(1, 3, 1000, true, -1, 10, 10000); err == nil {
		t.Fatal("negative max-cases must be refused")
	}
	if _, err := validateToolSelectionProviderBudget(2, 3, 1000, true, 1, 10, 10000); err == nil ||
		!strings.Contains(err.Error(), "max-cases") {
		t.Fatalf("cases above max-cases must be refused, got %v", err)
	}
	// 3 cases x 3 calls/case = 9 上界超过 8。
	if _, err := validateToolSelectionProviderBudget(3, 3, 1000, true, 3, 8, 100000); err == nil ||
		!strings.Contains(err.Error(), "max-provider-calls") {
		t.Fatalf("call upper bound above max-provider-calls must be refused, got %v", err)
	}
	// 1 case 硬上界 = 3 x 4000 = 12000 超过 1500。
	if _, err := validateToolSelectionProviderBudget(1, 3, 4000, true, 1, 100, 1500); err == nil ||
		!strings.Contains(err.Error(), "max-provider-tokens") {
		t.Fatalf("hard Token upper bound above max-provider-tokens must be refused, got %v", err)
	}
	budget, err := validateToolSelectionProviderBudget(3, 3, 2000, true, 3, 9, 18000)
	if err != nil {
		t.Fatalf("valid budget refused: %v", err)
	}
	if budget.Cases != 3 || budget.ProviderCalls != 9 || budget.TotalTokens != 3*3*2000 {
		t.Fatalf("budget = %+v, want cases=3 calls=9 tokens=%d", budget, 3*3*2000)
	}
}

// TestSelectionDefaultCallUpperBoundIs135For45Cases 证明默认预算公式：
// 45 Case x 3 次调用/Case = 135 次 Provider 调用上界（base 校准 1 次 +
// wide 1 次 + production 1 次），且 Token 硬上界按
// Case 数 x 3 x contextWindowTokens 扩展。
func TestSelectionDefaultCallUpperBoundIs135For45Cases(t *testing.T) {
	budget, err := validateToolSelectionProviderBudget(
		45, 3, 8192, true,
		45, 135, 45*3*8192,
	)
	if err != nil {
		t.Fatalf("default 45-case budget refused: %v", err)
	}
	if budget.ProviderCalls != 135 {
		t.Fatalf("default call upper bound = %d, want 45*3=135", budget.ProviderCalls)
	}
	if budget.Cases != 45 || budget.TotalTokens != 45*3*8192 {
		t.Fatalf("budget = %+v, want cases=45 tokens=%d", budget, 45*3*8192)
	}
}

// TestToolSelectionTokenHardUpperBoundFollowsProfileContextWindow 证明 Token 硬
// 上界由最终命名 Profile 的 contextWindowTokens 派生（每次调用 <= 窗口、
// 每 Case = 3 x 窗口、总数 = Case 数 x 3 x 窗口），不再依赖固定 16K 输入假设：
// 8K 与 128K 窗口的 Profile 必须得出不同的硬上界，而调用上界保持 3 次/Case。
func TestToolSelectionTokenHardUpperBoundFollowsProfileContextWindow(t *testing.T) {
	budget8K, err := validateToolSelectionProviderBudget(45, 3, 8192, true, 45, 135, 45*3*8192)
	if err != nil {
		t.Fatalf("8K-window budget refused: %v", err)
	}
	budget128K, err := validateToolSelectionProviderBudget(45, 3, 131072, true, 45, 135, 45*3*131072)
	if err != nil {
		t.Fatalf("128K-window budget refused: %v", err)
	}
	if budget8K.ProviderCalls != 135 || budget128K.ProviderCalls != 135 {
		t.Fatalf("call upper bound must stay 45*3=135 for every context window: 8K=%d 128K=%d",
			budget8K.ProviderCalls, budget128K.ProviderCalls)
	}
	if budget8K.TotalTokens == budget128K.TotalTokens {
		t.Fatal("hard Token upper bound must depend on the profile context window")
	}
	if budget8K.TotalTokens != 45*3*8192 || budget128K.TotalTokens != 45*3*131072 {
		t.Fatalf("hard Token upper bound must be cases*3*contextWindow: 8K=%d (want %d) 128K=%d (want %d)",
			budget8K.TotalTokens, 45*3*8192, budget128K.TotalTokens, 45*3*131072)
	}
	// 8K 窗口下的硬上界必须显著低于旧的固定 16K 假设（45 x 3 x (16K+1K)）：
	// 这证明上界不再按无法证明的 "base prompt ~16K" 魔数计算。
	if budget8K.TotalTokens >= 45*3*(16*1024+1024) {
		t.Fatalf("Token upper bound must not be derived from the fixed 16K base assumption: %d", budget8K.TotalTokens)
	}
}

// TestToolSelectionTokenHardUpperBoundOverflowFailsClosed 证明调用数与 Token
// 硬上界的乘法在溢出时 fail-closed，绝不回绕成负数或小值后放行。
func TestToolSelectionTokenHardUpperBoundOverflowFailsClosed(t *testing.T) {
	_, err := validateToolSelectionProviderBudget(math.MaxInt, 3, 262144, true, math.MaxInt, math.MaxInt, math.MaxInt)
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("call-bound multiplication overflow must fail closed, got %v", err)
	}
	_, err = validateToolSelectionProviderBudget(2, 3, math.MaxInt, true, 2, math.MaxInt, math.MaxInt)
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("Token-bound multiplication overflow must fail closed, got %v", err)
	}
	_, err = validateToolSelectionProviderBudget(1, 3, 262144, true, 1, math.MaxInt, math.MaxInt)
	if err != nil {
		t.Fatalf("valid single-case budget refused: %v", err)
	}
}

// TestSelectToolSelectionCasesIsExactAndNeverTruncates 证明 -case-id 在完整
// 数据集校验后进行精确选择：空值与空白等价于整个数据集；已知 CaseID 精确
// 返回单 Case；未知 CaseID fail-closed；不允许实现成"前 N 条"截断。
func TestSelectToolSelectionCasesIsExactAndNeverTruncates(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	all, err := readToolSelectionCases(dataset)
	if err != nil {
		t.Fatalf("readToolSelectionCases: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("test dataset must contain cases")
	}
	selected, err := selectToolSelectionCases(all, "")
	if err != nil || len(selected) != len(all) {
		t.Fatalf("empty case-id must keep the whole dataset: len=%d err=%v", len(selected), err)
	}
	selected, err = selectToolSelectionCases(all, "   ")
	if err != nil || len(selected) != len(all) {
		t.Fatalf("whitespace case-id must keep the whole dataset: len=%d err=%v", len(selected), err)
	}
	selected, err = selectToolSelectionCases(all, " "+all[len(all)-1].CaseID+" ")
	if err != nil || len(selected) != 1 || selected[0].CaseID != all[len(all)-1].CaseID {
		t.Fatalf("case-id must be trimmed and select exactly one case: %+v err=%v", selected, err)
	}
	if _, err := selectToolSelectionCases(all, "missing-case"); err == nil ||
		!strings.Contains(err.Error(), "missing-case") {
		t.Fatalf("unknown case-id must fail closed with the id in the message, got %v", err)
	}
}

// TestSelectionBudgetFailClosedBeforeFactories 证明成本闸门整条 fail-closed
// 链在创建任何 Provider / 远端连接之前执行：缺授权、max-cases 超限、调用
// 上界超限、Token 上界超限四种场景 newChatModel 与 connectGitHub 调用数均
// 为 0，且不创建输出文件。
func TestSelectionBudgetFailClosedBeforeFactories(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	twoCaseDataset := writeSelectionTwoCaseDatasetForTest(t)
	scenarios := []struct {
		name     string
		twoCases bool
		args     []string
		wantErr  string
	}{
		{name: "missing authorization", wantErr: "allow-provider-calls"},
		{name: "cases exceed max", twoCases: true, args: []string{
			"-allow-provider-calls", "-max-cases", "1",
			"-max-provider-calls", "100", "-max-provider-tokens", "100000000",
		}, wantErr: "max-cases"},
		{name: "call bound exceeded", args: []string{
			"-allow-provider-calls", "-max-cases", "3",
			"-max-provider-calls", "2", "-max-provider-tokens", "1000000",
		}, wantErr: "max-provider-calls"},
		{name: "token bound exceeded", args: []string{
			"-allow-provider-calls", "-max-cases", "1",
			"-max-provider-calls", "10", "-max-provider-tokens", "1",
		}, wantErr: "max-provider-tokens"},
		{name: "unknown case-id fails closed", args: []string{
			"-allow-provider-calls", "-max-cases", "1",
			"-max-provider-calls", "10", "-max-provider-tokens", "1000000",
			"-case-id", "missing-case",
		}, wantErr: "missing-case"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			outputDir := t.TempDir()
			output := filepath.Join(outputDir, "obs.jsonl")
			summary := filepath.Join(outputDir, "summary.json")
			ds := dataset
			if scenario.twoCases {
				ds = twoCaseDataset
			}
			var modelCalls atomic.Int32
			var githubCalls atomic.Int32
			deps := defaultSelectionEvalDependencies()
			deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
			deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
				modelCalls.Add(1)
				return nil, errors.New("factory must not be reached before budget gate")
			}
			deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
				githubCalls.Add(1)
				return nil, errors.New("GitHub must not be reached before budget gate")
			}
			args := append([]string{"-dataset", ds, "-output", output, "-summary", summary,
				"-concurrency", "1", "-allow-dirty"}, scenario.args...)
			err := runWithDependencies(context.Background(), args, zap.NewNop(), deps)
			if err == nil || !strings.Contains(err.Error(), scenario.wantErr) {
				t.Fatalf("runWithDependencies error = %v, want %q", err, scenario.wantErr)
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

// TestSelectionCaseIDSelectsSingleCaseForProviderRun 证明 -case-id 精确选择后
// 预算按选中 Case 数计算并通过，Provider 只创建一次，且一路上没有触碰
// activeProfile。
func TestSelectionCaseIDSelectsSingleCaseForProviderRun(t *testing.T) {
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
	deps.newChatModel = func(_ context.Context, profileName string, profile config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		factoryCalls.Add(1)
		if profileName != "opencode-deepseek-main" {
			return nil, fmt.Errorf("factory received profile %q, want the explicitly named profile", profileName)
		}
		if profile.Provider != "opencode-go" || profile.Model != "deepseek-v4-flash" {
			return nil, fmt.Errorf("factory received wrong named profile: %+v", profile)
		}
		return modelStub, nil
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-profile", "opencode-deepseek-main",
		"-case-id", "case-1",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "1000000",
	}, zap.NewNop(), deps)
	if err != nil {
		t.Fatalf("runWithDependencies: %v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("newChatModel called %d times, want 1", factoryCalls.Load())
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 2 {
		t.Fatalf("single selected case must produce 2 observations (wide + production), got %d", len(lines))
	}
}

// TestSelectionBudgetPrintCoversProfileAndBounds 证明运行前打印包含 Case 数、
// 调用上界、Token 上界、Profile 名、Provider、模型与实现 revision/dirty。
func TestSelectionBudgetPrintCoversProfileAndBounds(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "obs.jsonl")
	summary := filepath.Join(outputDir, "summary.json")

	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writeEnd
	defer func() { os.Stdout = oldStdout }()

	deps := defaultSelectionEvalDependencies()
	modelStub := &selectionEvalModelStub{}
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		return modelStub, nil
	}

	runErr := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-profile", "opencode-deepseek-main",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "3000000",
	}, zap.NewNop(), deps)
	_ = writeEnd.Close()
	printed, _ := io.ReadAll(readEnd)
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("runWithDependencies: %v", runErr)
	}
	printedText := string(printed)
	// opencode-deepseek-main 的 ContextWindowTokens=262144：单 Case 硬 Token
	// 上界 = 3 x 262144 = 786432。
	for _, want := range []string{
		"cases=1", "authorized_provider_call_upper_bound=3",
		"authorized_token_hard_upper_bound=786432", "profile=opencode-deepseek-main",
		"provider=opencode-go", "model=deepseek-v4-flash", "revision=",
	} {
		if !strings.Contains(printedText, want) {
			t.Fatalf("pre-run print missing %q, got:\n%s", want, printedText)
		}
	}
}

// selectionEvalErrorModelStub 是可脚本化的模型桩：Generate 按调用序号返回
// errors 中对应错误（越界或 nil 时返回成功响应）。校准调用
// （WithMaxTokens(1)）返回 800 tokens，带 Tool 调用返回 1000 tokens——
// 判别基于 option，不依赖调用顺序，因此并发交错下也保持每 Case 的
// base < arm 校准一致性。
type selectionEvalErrorModelStub struct {
	errors []error
	calls  atomic.Int32
}

func (m *selectionEvalErrorModelStub) Generate(
	_ context.Context,
	_ []*schema.Message,
	options ...model.Option,
) (*schema.Message, error) {
	index := int(m.calls.Add(1)) - 1
	if index < len(m.errors) && m.errors[index] != nil {
		return nil, m.errors[index]
	}
	promptTokens := 1000
	applied := model.GetCommonOptions(nil, options...)
	if applied.MaxTokens != nil && *applied.MaxTokens == 1 {
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

func (m *selectionEvalErrorModelStub) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *selectionEvalErrorModelStub) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *selectionEvalErrorModelStub) resetForTest() {
	m.calls.Store(0)
}

// runSelectionEvalWithStubForTest 用给定模型桩跑完整单 Case 评测（仅本地
// 桩，无任何真实 Provider）。
func runSelectionEvalWithStubForTest(t *testing.T, modelStub model.ToolCallingChatModel) (string, string) {
	t.Helper()
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "obs.jsonl")
	summary := filepath.Join(outputDir, "summary.json")
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		return modelStub, nil
	}
	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-profile", "opencode-deepseek-main",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "10", "-max-provider-tokens", "3000000",
	}, zap.NewNop(), deps)
	if err != nil {
		t.Fatalf("runWithDependencies: %v", err)
	}
	return output, summary
}

// selectionSummaryForTest 是 summary.json 的局部投影，只解码本测试关心的
// 字段。
type selectionSummaryForTest struct {
	Wide struct {
		PromptTokens int `json:"promptTokens"`
	} `json:"wide"`
	Production struct {
		PromptTokens int `json:"promptTokens"`
	} `json:"production"`
	FailureTypes       map[string]int `json:"failureTypes"`
	ProviderAccounting struct {
		ModelGenerateAttempts int `json:"modelGenerateAttempts"`
		UsageReportedAttempts int `json:"usageReportedAttempts"`
		UsageMissingAttempts  int `json:"usageMissingAttempts"`
		PromptTokens          int `json:"promptTokens"`
		CompletionTokens      int `json:"completionTokens"`
		TotalTokens           int `json:"totalTokens"`
		CachedTokens          int `json:"cachedTokens"`
		ReasoningTokens       int `json:"reasoningTokens"`
	} `json:"providerAccounting"`
}

func readSelectionSummaryForTest(t *testing.T, path string) selectionSummaryForTest {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var summary selectionSummaryForTest
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatalf("decode summary %s: %v", contents, err)
	}
	return summary
}

func readSelectionObservationsForTest(t *testing.T, path string) []mesagent.ToolSelectionObservation {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	var observations []mesagent.ToolSelectionObservation
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var observation mesagent.ToolSelectionObservation
		if err := json.Unmarshal([]byte(line), &observation); err != nil {
			t.Fatalf("decode observation: %v", err)
		}
		observations = append(observations, observation)
	}
	return observations
}

// TestSelectionProviderErrorClassificationHTTPModes 证明 400/429 被稳定分类
// 到 observation.ErrorType，错误消息绝不泄漏，且 Summary 记账：校准成功 1
// 次 + 两臂失败 2 次 = 3 次 Generate 调用、1 次上报 Usage、2 次缺失 Usage。
func TestSelectionProviderErrorClassificationHTTPModes(t *testing.T) {
	secret := "sensitive provider message with prompt and credential details"
	stub := &selectionEvalErrorModelStub{errors: []error{
		nil,
		&openai.APIError{Message: secret, Type: "invalid_request_error", Code: "bad_param",
			HTTPStatus: "400 Bad Request", HTTPStatusCode: 400},
		&openai.APIError{Message: secret, Type: "rate_limit_error", Code: "insufficient_quota",
			HTTPStatus: "429 Too Many Requests", HTTPStatusCode: 429},
	}}
	output, summaryPath := runSelectionEvalWithStubForTest(t, stub)
	observations := readSelectionObservationsForTest(t, output)
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	byVariant := map[string]mesagent.ToolSelectionObservation{}
	for _, observation := range observations {
		byVariant[string(observation.Variant)] = observation
	}
	if got := byVariant["wide"].ErrorType; got != "provider_bad_request" {
		t.Fatalf("wide ErrorType = %q, want provider_bad_request", got)
	}
	if got := byVariant["production"].ErrorType; got != "provider_rate_limited" {
		t.Fatalf("production ErrorType = %q, want provider_rate_limited", got)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if strings.Contains(string(contents), "credential") || strings.Contains(string(contents), secret) {
		t.Fatal("observations must not leak the provider error message")
	}
	summary := readSelectionSummaryForTest(t, summaryPath)
	accounting := summary.ProviderAccounting
	if accounting.ModelGenerateAttempts != 3 || accounting.UsageReportedAttempts != 1 || accounting.UsageMissingAttempts != 2 {
		t.Fatalf("accounting = %+v, want attempts=3 reported=1 missing=2", accounting)
	}
	if accounting.PromptTokens != 800 || accounting.TotalTokens != 850 {
		t.Fatalf("accumulated usage = %+v, want only the calibration attempt (800/850)", accounting)
	}
	if summary.FailureTypes["provider_bad_request"] != 1 || summary.FailureTypes["provider_rate_limited"] != 1 {
		t.Fatalf("failureTypes = %+v", summary.FailureTypes)
	}
}

func TestSelectionCalibrationProviderErrorIsSanitized(t *testing.T) {
	dataset := writeSelectionDatasetForTest(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "obs.jsonl")
	summary := filepath.Join(outputDir, "summary.json")
	secret := "sensitive calibration response body with credential details"
	stub := &selectionEvalErrorModelStub{errors: []error{
		&openai.APIError{
			Message:        secret,
			Type:           "invalid_request_error",
			Code:           "unsupported_tool_choice",
			HTTPStatus:     "400 Bad Request",
			HTTPStatusCode: 400,
		},
	}}
	deps := defaultSelectionEvalDependencies()
	deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
	deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
		return &githubmcp.Connection{}, nil
	}
	deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
		return stub, nil
	}

	err := runWithDependencies(context.Background(), []string{
		"-dataset", dataset, "-output", output, "-summary", summary,
		"-concurrency", "1", "-allow-dirty",
		"-profile", "opencode-deepseek-main",
		"-allow-provider-calls", "-max-cases", "1",
		"-max-provider-calls", "3", "-max-provider-tokens", "3000000",
	}, zap.NewNop(), deps)
	if err == nil {
		t.Fatal("calibration Provider failure must stop the evaluation")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "credential") {
		t.Fatalf("calibration error leaked Provider message: %v", err)
	}
	if !strings.Contains(err.Error(), platformchatmodel.ProviderErrorCategoryBadRequest) {
		t.Fatalf("calibration error = %v, want stable Provider category", err)
	}
}

// TestSelectionProviderErrorClassificationAuthAndServerModes 证明 401/503 被
// 稳定分类且不泄漏。
func TestSelectionProviderErrorClassificationAuthAndServerModes(t *testing.T) {
	stub := &selectionEvalErrorModelStub{errors: []error{
		nil,
		&openai.APIError{Message: "secret auth body", Type: "authentication_error", Code: "invalid_api_key",
			HTTPStatus: "401 Unauthorized", HTTPStatusCode: 401},
		&openai.APIError{Message: "secret server body", Type: "server_error", Code: "overloaded",
			HTTPStatus: "503 Service Unavailable", HTTPStatusCode: 503},
	}}
	output, summaryPath := runSelectionEvalWithStubForTest(t, stub)
	observations := readSelectionObservationsForTest(t, output)
	byVariant := map[string]mesagent.ToolSelectionObservation{}
	for _, observation := range observations {
		byVariant[string(observation.Variant)] = observation
	}
	if got := byVariant["wide"].ErrorType; got != "provider_auth_error" {
		t.Fatalf("wide ErrorType = %q, want provider_auth_error", got)
	}
	if got := byVariant["production"].ErrorType; got != "provider_server_error" {
		t.Fatalf("production ErrorType = %q, want provider_server_error", got)
	}
	summary := readSelectionSummaryForTest(t, summaryPath)
	accounting := summary.ProviderAccounting
	if accounting.ModelGenerateAttempts != 3 || accounting.UsageReportedAttempts != 1 || accounting.UsageMissingAttempts != 2 {
		t.Fatalf("accounting = %+v, want attempts=3 reported=1 missing=2", accounting)
	}
}

// TestSelectionProviderErrorClassificationTimeoutTransportAndUnknown 证明
// deadline/传输错误/未知错误分别分类为 timeout/transport/model_error。
func TestSelectionProviderErrorClassificationTimeoutTransportAndUnknown(t *testing.T) {
	t.Run("deadline and transport", func(t *testing.T) {
		stub := &selectionEvalErrorModelStub{errors: []error{
			nil,
			context.DeadlineExceeded,
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		}}
		output, _ := runSelectionEvalWithStubForTest(t, stub)
		observations := readSelectionObservationsForTest(t, output)
		byVariant := map[string]mesagent.ToolSelectionObservation{}
		for _, observation := range observations {
			byVariant[string(observation.Variant)] = observation
		}
		if got := byVariant["wide"].ErrorType; got != "provider_timeout" {
			t.Fatalf("wide ErrorType = %q, want provider_timeout", got)
		}
		if got := byVariant["production"].ErrorType; got != "provider_transport_error" {
			t.Fatalf("production ErrorType = %q, want provider_transport_error", got)
		}
	})
	t.Run("unknown falls back to model_error", func(t *testing.T) {
		stub := &selectionEvalErrorModelStub{errors: []error{
			nil,
			errors.New("plain non-provider error"),
			errors.New("plain non-provider error"),
		}}
		output, summaryPath := runSelectionEvalWithStubForTest(t, stub)
		observations := readSelectionObservationsForTest(t, output)
		for _, observation := range observations {
			if observation.ErrorType != "model_error" {
				t.Fatalf("ErrorType = %q, want model_error", observation.ErrorType)
			}
		}
		summary := readSelectionSummaryForTest(t, summaryPath)
		if summary.FailureTypes["model_error"] != 2 {
			t.Fatalf("failureTypes = %+v, want model_error=2", summary.FailureTypes)
		}
	})
}

// TestSelectionProviderAccountingCalibrationCountedOnceAndArmsClean 证明全
// 成功运行时校准 Usage 恰好计一次（800 + 两臂 1000 + 1000），且校准不混入
// 两臂指标。
func TestSelectionProviderAccountingCalibrationCountedOnceAndArmsClean(t *testing.T) {
	output, summaryPath := runSelectionEvalWithStubForTest(t, &selectionEvalErrorModelStub{})
	observations := readSelectionObservationsForTest(t, output)
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	summary := readSelectionSummaryForTest(t, summaryPath)
	accounting := summary.ProviderAccounting
	if accounting.ModelGenerateAttempts != 3 || accounting.UsageReportedAttempts != 3 || accounting.UsageMissingAttempts != 0 {
		t.Fatalf("accounting = %+v, want attempts=3 reported=3 missing=0", accounting)
	}
	if accounting.PromptTokens != 800+1000+1000 {
		t.Fatalf("accumulated prompt tokens = %d, want 2800 (calibration counted exactly once)", accounting.PromptTokens)
	}
	if accounting.CompletionTokens != 50*3 || accounting.TotalTokens != 850+1050+1050 {
		t.Fatalf("accumulated completion/total = %d/%d", accounting.CompletionTokens, accounting.TotalTokens)
	}
	// 校准绝不混入两臂指标。
	if summary.Wide.PromptTokens != 1000 || summary.Production.PromptTokens != 1000 {
		t.Fatalf("arm metrics polluted by calibration: wide=%d production=%d",
			summary.Wide.PromptTokens, summary.Production.PromptTokens)
	}
}

// TestSelectionProviderAccountingDeterministicAcrossConcurrency 证明并发
// 1..8 下 Generate 记账确定且一致：2 个 Case × 3 次尝试 = 6，校准每个 Case 恰好
// 一次。并发安全由 -race 验证（本测试无共享可变状态，归并只在主协程）。
func TestSelectionProviderAccountingDeterministicAcrossConcurrency(t *testing.T) {
	for _, concurrency := range []string{"1", "2", "4", "8"} {
		t.Run("concurrency="+concurrency, func(t *testing.T) {
			dataset := writeSelectionTwoCaseDatasetForTest(t)
			outputDir := t.TempDir()
			output := filepath.Join(outputDir, "obs.jsonl")
			summary := filepath.Join(outputDir, "summary.json")
			stub := &selectionEvalErrorModelStub{}
			deps := defaultSelectionEvalDependencies()
			deps.loadConfig = func() (config.Config, error) { return selectionTestConfig(), nil }
			deps.connectGitHub = func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error) {
				return &githubmcp.Connection{}, nil
			}
			deps.newChatModel = func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
				return stub, nil
			}
			err := runWithDependencies(context.Background(), []string{
				"-dataset", dataset, "-output", output, "-summary", summary,
				"-concurrency", concurrency, "-allow-dirty",
				"-profile", "opencode-deepseek-main",
				"-allow-provider-calls", "-max-cases", "2",
				"-max-provider-calls", "20", "-max-provider-tokens", "6000000",
			}, zap.NewNop(), deps)
			if err != nil {
				t.Fatalf("runWithDependencies(concurrency=%s): %v", concurrency, err)
			}
			accounting := readSelectionSummaryForTest(t, summary).ProviderAccounting
			if accounting.ModelGenerateAttempts != 6 || accounting.UsageReportedAttempts != 6 || accounting.UsageMissingAttempts != 0 {
				t.Fatalf("accounting = %+v, want attempts=6 reported=6 missing=0", accounting)
			}
			// 两个 Case 各一次校准（800）+ 各两臂（1000×4）。
			if accounting.PromptTokens != 2*800+4*1000 {
				t.Fatalf("prompt tokens = %d, want 5600 (each calibration counted once)", accounting.PromptTokens)
			}
		})
	}
}
