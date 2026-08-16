// 兼容性探针模式：在正式 Tool Selection v3 路径之外，用固定单变量矩阵
// 判断 OpenCode Go Tool Calling 的 400 由 tool_choice、Tool 数量还是真实
// Schema 引起。探针绝不改变正式路径与 v3 输出合同。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 探针输出合同版本与固定矩阵上界：恰好 5 次 Generate。
const (
	compatibilityProbeObservationVersion = "tool-compatibility-probe-v1"
	compatibilityProbeScenarioCount      = 5

	compatibilityProbeScenarioOneSimpleNoChoice   = "one_simple_no_choice"
	compatibilityProbeScenarioOneSimpleRequired   = "one_simple_required"
	compatibilityProbeScenarioManySimpleNoChoice  = "many_simple_no_choice"
	compatibilityProbeScenarioOneRealNoChoice     = "one_real_no_choice"
	compatibilityProbeScenarioFullProductionNoCho = "full_production_no_choice"
	compatibilityProbeToolChoiceModeAbsent        = "absent"
	compatibilityProbeToolChoiceModeRequired      = "required"
	compatibilityProbeSimpleToolName              = "probe_simple_tool"
	compatibilityProbeSimpleToolDescription       = "compatibility probe synthetic minimal read-only tool"
)

// compatibilityProbeOptions 是探针模式的注入参数：全部来自 runWithDependencies
// 解析后的 CLI flag，不重新解析。
type compatibilityProbeOptions struct {
	datasetPath        string
	caseID             string
	profileFlag        string
	probeOutputPath    string
	allowProviderCalls bool
	maxCases           int
	maxProviderCalls   int
	maxProviderTokens  int
	allowDirty         bool
}

// compatibilityProbeObservation 是独立探针观测合同：只记录分类结果与受限
// 字段，绝不保存模型正文、原始错误消息、响应正文、Prompt、Tool 参数或凭据。
type compatibilityProbeObservation struct {
	ObservationSchemaVersion string               `json:"observationSchemaVersion"`
	Scenario                 string               `json:"scenario"`
	ModelProvider            string               `json:"modelProvider"`
	ModelID                  string               `json:"modelId"`
	ModelProfileFingerprint  string               `json:"modelProfileFingerprint"`
	ImplementationRevision   string               `json:"implementationRevision"`
	ImplementationDirty      bool                 `json:"implementationDirty"`
	ToolCount                int                  `json:"toolCount"`
	ToolNames                []string             `json:"toolNames"`
	ToolSchemaFingerprint    string               `json:"toolSchemaFingerprint"`
	ToolChoiceMode           string               `json:"toolChoiceMode"`
	Success                  bool                 `json:"success"`
	ErrorCategory            string               `json:"errorCategory,omitempty"`
	HTTPStatus               string               `json:"httpStatus,omitempty"`
	HTTPStatusCode           int                  `json:"httpStatusCode,omitempty"`
	ProviderType             string               `json:"providerType,omitempty"`
	ProviderCode             string               `json:"providerCode,omitempty"`
	ProviderParam            string               `json:"providerParam,omitempty"`
	FinishReason             string               `json:"finishReason,omitempty"`
	ToolCallCount            int                  `json:"toolCallCount"`
	SelectedTool             string               `json:"selectedTool,omitempty"`
	DurationMillis           int64                `json:"durationMillis"`
	Usage                    *mesagent.ModelUsage `json:"usage,omitempty"`
}

// probeSyntheticToolInfo 构造最小合法合成 Tool：空参数 Schema、固定描述；
// many_simple 场景仅在名称上唯一，其余字段完全一致。
func probeSyntheticToolInfo(name string) *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: name, Desc: compatibilityProbeSimpleToolDescription,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func probeSyntheticToolInfos(count int) []*schema.ToolInfo {
	infos := make([]*schema.ToolInfo, 0, count)
	if count == 1 {
		return append(infos, probeSyntheticToolInfo(compatibilityProbeSimpleToolName))
	}
	for index := range count {
		name := compatibilityProbeSimpleToolName
		if index > 0 {
			name = fmt.Sprintf("%s_%02d", compatibilityProbeSimpleToolName, index)
		}
		infos = append(infos, probeSyntheticToolInfo(
			name,
		))
	}
	return infos
}

// runCompatibilityProbeWithDependencies 执行固定 5 场景矩阵。所有场景共用
// 同一模型 Profile、同一 messages、temperature=0 与相同 max tokens，只调用
// 模型、不执行任何 Tool，顺序执行、禁止并发。失败分类通过
// chatmodel.ClassifyProviderError，输出原子替换，失败绝不留下正式结果。
func runCompatibilityProbeWithDependencies(
	ctx context.Context,
	log *zap.Logger,
	deps selectionEvalDependencies,
	options compatibilityProbeOptions,
) error {
	cases, err := readToolSelectionCases(options.datasetPath)
	if err != nil {
		return fmt.Errorf("read tool selection dataset: %w", err)
	}
	cases, err = selectToolSelectionCases(cases, options.caseID)
	if err != nil {
		return err
	}
	if len(cases) != 1 {
		return fmt.Errorf("compatibility probe requires exactly one case, got %d (use -case-id)", len(cases))
	}
	definition := cases[0]
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.GitHubMCP.Enabled {
		return errors.New("chat model and GitHub MCP must be enabled")
	}
	profileName, modelProfile, modelProfileFingerprint, err := prepareToolSelectionProfile(cfg.Models.Chat, options.profileFlag)
	if err != nil {
		return err
	}
	// 身份闸门与正式模式同语义：dirty/unknown revision 只有 -allow-dirty 才接受。
	identity, identityErr := deps.resolveIdentity()
	if identityErr != nil && !options.allowDirty {
		return fmt.Errorf("resolve implementation revision: %w (pass -allow-dirty for local smoke)", identityErr)
	}
	identity, decisionErr := evaluationidentity.EvaluateImplementationIdentity(identity, options.allowDirty)
	if decisionErr != nil {
		return decisionErr
	}
	// 成本闸门：固定矩阵上界 5 次 Generate，且必须在创建任何 Provider 与连接
	// GitHub MCP 之前 fail-closed。
	budget, err := validateToolSelectionProviderBudget(
		1, compatibilityProbeScenarioCount, modelProfile.ContextWindowTokens,
		options.allowProviderCalls, options.maxCases, options.maxProviderCalls, options.maxProviderTokens,
	)
	if err != nil {
		return err
	}
	if identityErr != nil || identity.Dirty || identity.Revision == "unknown" {
		log.Warn("compatibility probe accepted a dirty or unknown revision for local smoke only",
			zap.String("revision", identity.Revision), zap.Bool("dirty", identity.Dirty))
	}
	fmt.Fprintf(os.Stdout,
		"mesguard-tool-selection-eval compatibility-probe profile=%s provider=%s model=%s scenarios=%d authorized_provider_call_upper_bound=%d authorized_token_hard_upper_bound=%d revision=%s dirty=%t\n",
		profileName, modelProfile.Provider, modelProfile.Model,
		compatibilityProbeScenarioCount, budget.ProviderCalls, budget.TotalTokens, identity.Revision, identity.Dirty)

	githubConnection, err := deps.connectGitHub(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
	if err != nil {
		return fmt.Errorf("connect GitHub MCP: %w", err)
	}
	defer githubConnection.Close()
	skillRuntime, err := mesagent.NewNativeSkillRuntime(ctx, cfg.Agent.SkillsDirectory)
	if err != nil {
		return fmt.Errorf("build native Skill runtime: %w", err)
	}
	assembly, err := assembleSelectionEval(ctx, githubConnection.Tools, skillRuntime, deps.verifySelectionComparability)
	if err != nil {
		return err
	}
	productionInfos, _, err := selectionArmTools(ctx, assembly.productionAuthorization, skillRuntime.Middleware)
	if err != nil {
		return fmt.Errorf("assemble production arm: %w", err)
	}
	chatModel, err := deps.newChatModel(ctx, profileName, modelProfile)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}

	var realReadExternalCase *schema.ToolInfo
	for _, info := range productionInfos {
		if info.Name == mesagent.ToolReadExternalCase {
			realReadExternalCase = info
		}
	}
	if realReadExternalCase == nil {
		return fmt.Errorf("production final tool infos must contain %s", mesagent.ToolReadExternalCase)
	}

	scenarios := []compatibilityProbeScenario{
		{name: compatibilityProbeScenarioOneSimpleNoChoice, infos: probeSyntheticToolInfos(1), required: false},
		{name: compatibilityProbeScenarioOneSimpleRequired, infos: probeSyntheticToolInfos(1), required: true},
		{name: compatibilityProbeScenarioManySimpleNoChoice, infos: probeSyntheticToolInfos(len(productionInfos)), required: false},
		{name: compatibilityProbeScenarioOneRealNoChoice, infos: []*schema.ToolInfo{realReadExternalCase}, required: false},
		{name: compatibilityProbeScenarioFullProductionNoCho, infos: productionInfos, required: false},
	}
	messages := selectionMessages(definition)
	observations := make([]compatibilityProbeObservation, 0, len(scenarios))
	for _, scenario := range scenarios {
		observation, observeErr := probeCompatibilityScenario(
			ctx, chatModel, scenario, messages, identity, modelProfileFingerprint, modelProfile, log,
		)
		if observeErr != nil {
			return observeErr
		}
		observations = append(observations, observation)
	}

	tempPath := options.probeOutputPath + ".tmp-" + uuid.NewString()
	defer os.Remove(tempPath)
	output, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary probe output: %w", err)
	}
	encoder := json.NewEncoder(output)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = output.Close()
			return fmt.Errorf("write probe observation: %w", err)
		}
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close probe output: %w", err)
	}
	if err := replaceEvaluationFile(tempPath, options.probeOutputPath); err != nil {
		return fmt.Errorf("replace probe output: %w", err)
	}
	return nil
}

type compatibilityProbeScenario struct {
	name     string
	infos    []*schema.ToolInfo
	required bool
}

// probeCompatibilityScenario 执行单个场景：非流式 Generate、temperature=0、
// 相同 max tokens；no_choice 场景完全不传 WithToolChoice（不是显式 auto），
// required 场景唯一变量是 WithToolChoice(schema.ToolChoiceForced)。
func probeCompatibilityScenario(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	scenario compatibilityProbeScenario,
	messages []*schema.Message,
	identity evaluationidentity.Identity,
	modelProfileFingerprint string,
	modelProfile config.ChatModelProfileConfig,
	log *zap.Logger,
) (compatibilityProbeObservation, error) {
	bound, err := chatModel.WithTools(scenario.infos)
	if err != nil {
		return compatibilityProbeObservation{}, fmt.Errorf("scenario %s bind tools: %w", scenario.name, err)
	}
	toolChoiceMode := compatibilityProbeToolChoiceModeAbsent
	options := []model.Option{
		model.WithTemperature(0), model.WithMaxTokens(toolSelectionMaxTokens),
	}
	if scenario.required {
		toolChoiceMode = compatibilityProbeToolChoiceModeRequired
		options = append(options, model.WithToolChoice(schema.ToolChoiceForced))
	}
	_, names, schemaHash, _, metadataErr := selectionToolInfoMetadata(scenario.infos)
	if metadataErr != nil {
		return compatibilityProbeObservation{}, fmt.Errorf("scenario %s tool metadata: %w", scenario.name, metadataErr)
	}
	observation := compatibilityProbeObservation{
		ObservationSchemaVersion: compatibilityProbeObservationVersion,
		Scenario:                 scenario.name,
		ModelProvider:            modelProfile.Provider, ModelID: modelProfile.Model,
		ModelProfileFingerprint: modelProfileFingerprint,
		ImplementationRevision:  identity.Revision, ImplementationDirty: identity.Dirty,
		ToolCount: len(scenario.infos), ToolNames: names, ToolSchemaFingerprint: schemaHash,
		ToolChoiceMode: toolChoiceMode,
	}
	startedAt := time.Now()
	message, generateErr := bound.Generate(ctx, messages, options...)
	observation.DurationMillis = time.Since(startedAt).Milliseconds()
	if generateErr != nil {
		// 稳定错误分类：只记录 category、HTTP status 与受限 provider
		// type/code/param，绝不记录原始错误消息、响应正文或 Prompt。
		category := platformchatmodel.ClassifyProviderError(generateErr)
		observation.ErrorCategory = category.Category
		observation.HTTPStatus = category.HTTPStatus
		observation.HTTPStatusCode = category.HTTPStatusCode
		observation.ProviderType = category.Type
		observation.ProviderCode = category.Code
		observation.ProviderParam = category.Param
		if log != nil {
			log.Warn("compatibility probe generate failed",
				zap.String("scenario", scenario.name),
				zap.String("category", category.Category),
				zap.Int("http_status_code", category.HTTPStatusCode),
			)
		}
		return observation, nil
	}
	observation.Success = true
	if message == nil {
		observation.Success = false
		observation.ErrorCategory = "empty_model_response"
		return observation, nil
	}
	if message.ResponseMeta != nil {
		observation.FinishReason = message.ResponseMeta.FinishReason
	}
	observation.ToolCallCount = len(message.ToolCalls)
	if len(message.ToolCalls) == 1 {
		observation.SelectedTool = message.ToolCalls[0].Function.Name
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		// 无 Provider Usage 时不估算 Token：Usage 保持空。
		return observation, nil
	}
	usage := message.ResponseMeta.Usage
	observation.Usage = &mesagent.ModelUsage{
		ModelCalls: 1, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
	return observation, nil
}
