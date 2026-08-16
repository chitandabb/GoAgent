// Command mesguard-tool-selection-eval 对相同业务请求执行单轮 wide/production Tool Schema 配对评测。
// 每次模型调用只允许选择一个下一步只读 Tool，不执行 Tool，也不进入完整 Agent 循环。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	toolSelectionPromptVersion = "tool-selection-v4"
	toolSelectionMaxTokens     = 1024
	// v3 输出资产默认名：新合同生成新资产，不覆盖 v1/v2 历史观测与汇总。
	toolSelectionObservationsOutput = "testdata/tool-selection-v3.observations.jsonl"
	toolSelectionSummaryOutput      = "testdata/tool-selection-v3.summary.json"
)

var (
	selectionUserID         = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	selectionExternalCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	selectionCaseSourceID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	selectionSQLSourceID    = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-tool-selection-eval")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("Tool selection evaluation failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	return runWithDependencies(ctx, args, log, defaultSelectionEvalDependencies())
}

// selectionEvalDependencies 是 runWithDependencies 的注入点：离线测试用 stub
// 替换所有真实 Provider/远端连接工厂，证明 fail-closed 校验发生在任何
// Provider 创建之前（factory.calls == 0）。
type selectionEvalDependencies struct {
	loadConfig                   func() (config.Config, error)
	newChatModel                 func(context.Context, string, config.ChatModelProfileConfig) (model.ToolCallingChatModel, error)
	connectGitHub                func(context.Context, config.GitHubMCPConfig, *zap.Logger) (*githubmcp.Connection, error)
	verifySelectionComparability func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error)
}

// selectionEvalModelFactory 是 Provider 构造 seam：正式运行走真实
// platformchatmodel.New（只接收本次选择的 Profile 名称与最终 Profile，绝不
// 从 activeProfile 重新解析，也绝不为 NewActive 临时切换 activeProfile）。
func selectionEvalModelFactory(ctx context.Context, profileName string, profile config.ChatModelProfileConfig) (model.ToolCallingChatModel, error) {
	instance, err := platformchatmodel.New(ctx, profileName, profile)
	if err != nil {
		return nil, err
	}
	return instance.Model, nil
}

func defaultSelectionEvalDependencies() selectionEvalDependencies {
	return selectionEvalDependencies{
		loadConfig:                   config.Load,
		newChatModel:                 selectionEvalModelFactory,
		connectGitHub:                githubmcp.Connect,
		verifySelectionComparability: mesagent.VerifyToolSelectionComparability,
	}
}

func runWithDependencies(ctx context.Context, args []string, log *zap.Logger, deps selectionEvalDependencies) error {
	flags := flag.NewFlagSet("mesguard-tool-selection-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/tool-selection-v1.jsonl", "versioned JSONL cases")
	outputPath := flags.String("output", toolSelectionObservationsOutput, "observation JSONL output")
	summaryPath := flags.String("summary", toolSelectionSummaryOutput, "summary JSON output")
	concurrency := flags.Int("concurrency", 4, "parallel cases; each case keeps paired variants sequential")
	profileFlag := flags.String("profile", "", "named [models.chat.profiles.<name>] to evaluate with (empty = activeProfile)")
	caseID := flags.String("case-id", "", "exact dataset CaseID to evaluate (empty = whole dataset)")
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "explicitly authorize Provider calls for this evaluation run")
	maxCases := flags.Int("max-cases", 0, "authorized case count cap for Provider runs")
	maxProviderCalls := flags.Int("max-provider-calls", 0, "authorized Provider call upper bound")
	maxProviderTokens := flags.Int("max-provider-tokens", 0, "authorized Provider Token upper bound")
	allowDirty := flags.Bool("allow-dirty", false, "accept a dirty/unknown implementation revision for local smoke; results are NOT formal metrics")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-tool-selection-eval [-dataset path] [-output path] [-summary path] [-concurrency 1..8] [-profile <named-chat-profile>] [-case-id <exact-case-id>] [-allow-provider-calls] [-max-cases N] [-max-provider-calls N] [-max-provider-tokens N] [-allow-dirty]")
	}
	if *concurrency < 1 || *concurrency > 8 {
		return errors.New("concurrency must be between 1 and 8")
	}
	cases, err := readToolSelectionCases(*datasetPath)
	if err != nil {
		return fmt.Errorf("read tool selection dataset: %w", err)
	}
	// 数据集整体仍先经过现有版本、重复 ID 与字段合同校验；-case-id 的精确
	// 选择必须发生在成本预算与 Provider 创建之前，-max-cases 仍是授权上限，
	// 严禁把 max-cases 实现成"前 N 条"截断。
	cases, err = selectToolSelectionCases(cases, *caseID)
	if err != nil {
		return err
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.GitHubMCP.Enabled {
		return errors.New("chat model and GitHub MCP must be enabled")
	}
	// 解析 -profile：空值使用 activeProfile，非空值使用精确命名 Profile；
	// 评测变换（low reasoning、temperature=0、maxOutputTokens=1024）只作用于
	// 局部副本；模型 Profile 指纹基于最终副本与实际 Profile 名；绝不写回配置
	// Map，绝不替换 activeProfile，绝不为 NewActive 临时切换。
	profileName, modelProfile, modelProfileFingerprint, err :=
		prepareToolSelectionProfile(cfg.Models.Chat, *profileFlag)
	if err != nil {
		return err
	}
	// 身份校验必须在调用任何收费 Provider 之前完成：先解析实现 revision，
	// 再决定是否允许继续。
	identity, identityErr := evaluationidentity.ResolveImplementationIdentity()
	if identityErr != nil && !*allowDirty {
		return fmt.Errorf("resolve implementation revision: %w (pass -allow-dirty for local smoke)", identityErr)
	}
	identity, decisionErr := evaluationidentity.EvaluateImplementationIdentity(identity, *allowDirty)
	if decisionErr != nil {
		return decisionErr
	}
	// 成本闸门：缺授权、预算非正、Case 超限、调用或 Token 硬上界超限都在创建
	// 任何 Chat Provider 与任何远端连接（GitHub MCP）之前 fail-closed。默认
	// 每 Case 允许 3 次 Provider 调用（base 校准 1 + wide 1 + production 1）。
	// Token 硬上界由最终命名 Profile 的 contextWindowTokens 派生，不使用固定
	// 16K 输入假设。
	budget, err := validateToolSelectionProviderBudget(
		len(cases), toolSelectionPerCaseEndpointCalls, modelProfile.ContextWindowTokens,
		*allowProviderCalls, *maxCases, *maxProviderCalls, *maxProviderTokens,
	)
	if err != nil {
		return err
	}
	if identityErr != nil || identity.Dirty || identity.Revision == "unknown" {
		log.Warn("dirty or unknown implementation revision accepted for local smoke only; observations are NOT formal metrics",
			zap.String("revision", identity.Revision), zap.Bool("dirty", identity.Dirty))
	}
	fmt.Fprintf(os.Stdout,
		"mesguard-tool-selection-eval profile=%s provider=%s model=%s cases=%d authorized_provider_call_upper_bound=%d authorized_token_hard_upper_bound=%d revision=%s dirty=%t\n",
		profileName, strings.TrimSpace(modelProfile.Provider), strings.TrimSpace(modelProfile.Model),
		budget.Cases, budget.ProviderCalls, budget.TotalTokens, identity.Revision, identity.Dirty)
	githubConnection, err := deps.connectGitHub(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
	if err != nil {
		return fmt.Errorf("connect GitHub MCP: %w", err)
	}
	defer githubConnection.Close()
	skillRuntime, err := mesagent.NewNativeSkillRuntime(ctx, cfg.Agent.SkillsDirectory)
	if err != nil {
		return fmt.Errorf("build native Skill runtime: %w", err)
	}
	// 两臂装配与可比性 preflight 必须在创建任何收费 Provider 之前完成：
	// 生产臂（diagnosis-default）与 wide 臂（evaluation-wide-v2）经过同一个
	// Eino Skill Middleware，VerifyToolSelectionComparability 校验共享 Schema
	// 一致性与严格超集；不一致直接 fail-closed，不发起任何模型调用。
	assembly, err := assembleSelectionEval(ctx, githubConnection.Tools, skillRuntime, deps.verifySelectionComparability)
	if err != nil {
		return err
	}
	chatModel, err := deps.newChatModel(ctx, profileName, modelProfile)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}

	outputTempPath := *outputPath + ".tmp-" + uuid.NewString()
	summaryTempPath := *summaryPath + ".tmp-" + uuid.NewString()
	defer os.Remove(outputTempPath)
	defer os.Remove(summaryTempPath)
	output, err := os.OpenFile(outputTempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary observations output: %w", err)
	}
	encoder := json.NewEncoder(output)
	observations := make([]mesagent.ToolSelectionObservation, 0, len(cases)*2)
	type indexedCase struct {
		index      int
		definition mesagent.ToolSelectionCase
	}
	type caseResult struct {
		observations []mesagent.ToolSelectionObservation
		err          error
	}
	evalCtx, cancelEval := context.WithCancel(ctx)
	defer cancelEval()
	jobs := make(chan indexedCase)
	results := make(chan caseResult)
	var workers sync.WaitGroup
	for range *concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for current := range jobs {
				basePromptTokens, baseErr := measureBasePromptTokens(evalCtx, chatModel, current.definition)
				if baseErr != nil {
					results <- caseResult{err: fmt.Errorf("measure case %q base prompt: %w", current.definition.CaseID, baseErr)}
					continue
				}
				variants := []mesagent.ToolSelectionVariant{mesagent.ToolSelectionWide, mesagent.ToolSelectionProduction}
				if current.index%2 == 1 {
					variants[0], variants[1] = variants[1], variants[0]
				}
				currentResult := caseResult{observations: make([]mesagent.ToolSelectionObservation, 0, 2)}
				for _, variant := range variants {
					observation, observeErr := observeToolSelection(
						evalCtx, chatModel, assembly,
						current.definition, variant, basePromptTokens, identity,
						modelProfileFingerprint, modelProfile,
					)
					if observeErr != nil {
						currentResult.err = fmt.Errorf(
							"observe case %q variant %q: %w", current.definition.CaseID, variant, observeErr,
						)
						break
					}
					if validateErr := observation.Validate(); validateErr != nil {
						currentResult.err = fmt.Errorf(
							"validate case %q variant %q: %w", current.definition.CaseID, variant, validateErr,
						)
						break
					}
					currentResult.observations = append(currentResult.observations, observation)
				}
				results <- currentResult
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index, definition := range cases {
			select {
			case <-evalCtx.Done():
				return
			case jobs <- indexedCase{index: index, definition: definition}:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	var firstErr error
	for current := range results {
		if current.err != nil && firstErr == nil {
			firstErr = current.err
			cancelEval()
		}
		for _, observation := range current.observations {
			if firstErr == nil {
				if err := encoder.Encode(observation); err != nil {
					firstErr = fmt.Errorf("write observation: %w", err)
					cancelEval()
					continue
				}
			}
			observations = append(observations, observation)
			log.Info("Tool selection evaluation observation completed",
				zap.String("case_id", observation.CaseID), zap.String("variant", string(observation.Variant)),
				zap.String("selected_tool", observation.SelectedTool),
				zap.Int("prompt_tokens", observation.Usage.PromptTokens),
			)
		}
	}
	if firstErr != nil {
		_ = output.Close()
		return firstErr
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close observations output: %w", err)
	}
	summary, err := mesagent.EvaluateToolSelection(cases, observations)
	if err != nil {
		return fmt.Errorf("evaluate tool selection: %w", err)
	}
	encodedSummary, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	encodedSummary = append(encodedSummary, '\n')
	if err := os.WriteFile(summaryTempPath, encodedSummary, 0o600); err != nil {
		return fmt.Errorf("write temporary summary: %w", err)
	}
	if err := replaceEvaluationFile(outputTempPath, *outputPath); err != nil {
		return fmt.Errorf("replace observations: %w", err)
	}
	if err := replaceEvaluationFile(summaryTempPath, *summaryPath); err != nil {
		return fmt.Errorf("replace summary: %w", err)
	}
	return nil
}

func replaceEvaluationFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}

func observeToolSelection(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	assembly *selectionEvalAssembly,
	definition mesagent.ToolSelectionCase,
	variant mesagent.ToolSelectionVariant,
	basePromptTokens int,
	identity evaluationidentity.Identity,
	modelProfileFingerprint string,
	modelProfile config.ChatModelProfileConfig,
) (mesagent.ToolSelectionObservation, error) {
	accessCtx, err := withSelectionRunAccess(ctx)
	if err != nil {
		return mesagent.ToolSelectionObservation{}, err
	}
	// 两臂使用同一个真实 Eino Skill Middleware：wide 臂（evaluation-wide-v2
	// 并集 Profile）与 production 臂（diagnosis-default）经过完全相同的装配
	// 链（ToolAuthorizationMiddleware -> Skill Middleware），最终模型 Schema
	// 的共享 Tool 完全一致。preflight 已证明这一点。
	authorization := assembly.productionAuthorization
	toolProfileID := string(agentruntime.ToolProfileDiagnosis)
	if variant == mesagent.ToolSelectionWide {
		authorization = assembly.wideAuthorization
		toolProfileID = string(agentruntime.ToolProfileEvaluationWide)
	}
	_, authorizedCtx, authErr := authorization.BeforeAgent(
		accessCtx, &adk.ChatModelAgentContext{Tools: nil},
	)
	if authErr != nil {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf("assemble %s Tool schema: %w", variant, authErr)
	}
	_, finalCtx, skillErr := assembly.skillMiddleware.BeforeAgent(accessCtx, authorizedCtx)
	if skillErr != nil {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf("append %s skill Tool: %w", variant, skillErr)
	}
	available := finalCtx.Tools
	infos, names, schemaHash, schemaBytes, err := selectionToolSchemas(ctx, available)
	if err != nil {
		return mesagent.ToolSelectionObservation{}, err
	}
	expectedSchemaHash := assembly.productionSchemaHash
	if variant == mesagent.ToolSelectionWide {
		expectedSchemaHash = assembly.wideSchemaHash
	}
	if schemaHash != expectedSchemaHash {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf(
			"%s runtime Tool Schema hash %q differs from preflight Tool Schema %q",
			variant, schemaHash, expectedSchemaHash,
		)
	}
	bound, err := chatModel.WithTools(infos)
	if err != nil {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf("bind tools: %w", err)
	}
	startedAt := time.Now()
	message, generateErr := bound.Generate(ctx, selectionMessages(definition),
		model.WithTemperature(0), model.WithMaxTokens(toolSelectionMaxTokens), model.WithToolChoice(schema.ToolChoiceForced))
	observation := mesagent.ToolSelectionObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID, Variant: variant,
		RunID:                    fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		ObservationSchemaVersion: mesagent.ToolSelectionObservationV3,
		ModelProvider:            modelProfile.Provider, ModelID: modelProfile.Model,
		ReasoningEffort: modelProfile.ReasoningEffort, PromptVersion: toolSelectionPromptVersion,
		MaxOutputTokens: toolSelectionMaxTokens,
		ToolProfileID:   toolProfileID, ModelVisibleNames: names,
		ModelProfileFingerprint: modelProfileFingerprint,
		ImplementationRevision:  identity.Revision, ImplementationDirty: identity.Dirty,
		ComparisonFingerprint: assembly.comparability.ComparisonFingerprint,
		SharedToolNames:       assembly.comparability.SharedToolNames,
		BaselineOnlyToolNames: assembly.comparability.BaselineOnlyToolNames,
		AvailableTools:        names, ToolSchemaHash: schemaHash, ToolSchemaBytes: schemaBytes,
		DurationMillis: time.Since(startedAt).Milliseconds(),
	}
	if generateErr != nil {
		observation.ErrorType = "model_error"
		return observation, nil
	}
	if message == nil {
		observation.ErrorType = "empty_model_response"
		return observation, nil
	}
	if message.ResponseMeta != nil {
		observation.FinishReason = message.ResponseMeta.FinishReason
	}
	observation.ToolCallCount = len(message.ToolCalls)
	if len(message.ToolCalls) == 1 {
		observation.SelectedTool = message.ToolCalls[0].Function.Name
	} else {
		observation.ErrorType = "invalid_tool_call_count"
		observation.ModelText = truncateModelText(message.Content, 1024)
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		observation.ErrorType = "missing_provider_usage"
		return observation, nil
	}
	usage := message.ResponseMeta.Usage
	if basePromptTokens <= 0 || usage.PromptTokens <= basePromptTokens {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf(
			"provider prompt token calibration is invalid: base=%d variant=%d", basePromptTokens, usage.PromptTokens,
		)
	}
	observation.BasePromptTokens = basePromptTokens
	observation.ToolSchemaPromptTokens = usage.PromptTokens - basePromptTokens
	observation.Usage = mesagent.ModelUsage{
		ModelCalls: 1, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
	return observation, nil
}

func measureBasePromptTokens(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	definition mesagent.ToolSelectionCase,
) (int, error) {
	message, err := chatModel.Generate(ctx, selectionMessages(definition),
		model.WithTemperature(0), model.WithMaxTokens(1))
	if err != nil {
		return 0, err
	}
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil ||
		message.ResponseMeta.Usage.PromptTokens <= 0 {
		return 0, errors.New("base prompt response is missing Provider usage")
	}
	return message.ResponseMeta.Usage.PromptTokens, nil
}

func selectionMessages(definition mesagent.ToolSelectionCase) []*schema.Message {
	return []*schema.Message{
		schema.SystemMessage("你是 MESGuard 工具选择评测器。根据用户请求，从提供的工具中选择最合适的一个只读工具作为下一步。必须且只能返回一个工具调用，不要回答问题，不要执行多个工具。只根据工具描述和参数 schema 判断。本评测只衡量工具名选择，不衡量参数值；请求缺少调用参数时，仍须调用最合适的工具，并填写符合 schema 的占位值。"),
		schema.SystemMessage(selectionArgumentContext(definition.Scope)),
		schema.UserMessage(definition.UserQuery),
	}
}

func selectionArgumentContext(scope mesagent.ToolSelectionScope) string {
	switch scope {
	case mesagent.ToolSelectionTicket:
		return fmt.Sprintf("当前任务绑定的 externalCaseId=%s，可直接用于补齐工具参数。", selectionExternalCaseID)
	case mesagent.ToolSelectionGitHub:
		return "若所选工具需要仓库或版本参数，可使用 owner=chitandabb、repo=GoAgent、ref=main、sha=7b3f539 作为合法占位值；仓库发现请求仍按用户给出的名称搜索。用户要求浏览目录、目录结构或文件树时选择 get_repository_tree；只有已知具体文件路径并要求正文时才选择 get_file_contents。"
	case mesagent.ToolSelectionSQL:
		return fmt.Sprintf("当前任务只有一个已授权 SQL 数据源，dataSourceId=%s，Schema Catalog 已发布。用户明确要求查询、核对或统计实际业务数据时，应构造单条只读 SELECT 并选择 execute_readonly_query；缺少具体参数时可用 SELECT TOP 1 * FROM dbo.v_MESGuardExternalCases 作为合法占位。用户要求读取存储过程、视图或函数内部 SQL 定义时选择 get_database_object_definition，缺少对象参数时可用 schema=dbo、objectName=usp_UpdateWorkReport。仅在用户不知道对象或字段时选择 search_schema_catalog。", selectionSQLSourceID)
	default:
		return "缺失参数使用符合 schema 的合法占位值。"
	}
}

func truncateModelText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func selectionToolSchemas(
	ctx context.Context,
	tools []tool.BaseTool,
) ([]*schema.ToolInfo, []string, string, int, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	for _, current := range tools {
		info, err := current.Info(ctx)
		if err != nil {
			return nil, nil, "", 0, err
		}
		infos = append(infos, info)
	}
	return selectionToolInfoMetadata(infos)
}

func selectionToolInfoMetadata(
	infos []*schema.ToolInfo,
) ([]*schema.ToolInfo, []string, string, int, error) {
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	encoded, err := json.Marshal(infos)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("marshal tool schemas: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return infos, names, "sha256:" + hex.EncodeToString(digest[:]), len(encoded), nil
}

// withSelectionRunAccess 为评测 Schema 装配绑定一个合法诊断 RunAccess：
// 装配层只校验 RunAccess 存在与 Profile 匹配，评测不执行任何 Tool，因此
// 权限集合是全部允许的诊断权限，资源 Grant 与工具选择无关。
func withSelectionRunAccess(ctx context.Context) (context.Context, error) {
	permissions, err := agentruntime.NewPermissionSet(
		agentruntime.PermissionCaseRead, agentruntime.PermissionKnowledgeRead,
		agentruntime.PermissionSQLRead, agentruntime.PermissionCodeRead,
		agentruntime.PermissionAttachmentRead, agentruntime.PermissionWebRead,
	)
	if err != nil {
		return nil, err
	}
	grants, err := agentruntime.NewResourceGrants(agentruntime.ResourceGrantsConfig{
		ExternalCaseIDs: []uuid.UUID{selectionCaseSourceID},
	})
	if err != nil {
		return nil, err
	}
	policy, err := agentruntime.NewInvestigationPolicy(diagnosis.InvestigationPolicySchemaVersion, permissions, grants)
	if err != nil {
		return nil, err
	}
	access, err := agentruntime.DeriveDiagnosisRunAccess(
		policy,
		agentruntime.Actor{UserID: selectionUserID, Role: auth.RoleAnalyst},
		agentruntime.AccessCeiling{Permissions: permissions, Grants: grants},
	)
	if err != nil {
		return nil, err
	}
	return agentruntime.WithRunAccess(ctx, access), nil
}

func buildSelectionCatalogs(
	ctx context.Context,
	githubTools []tool.BaseTool,
	skillRuntime *mesagent.NativeSkillRuntime,
) (*mesagent.ToolCatalog, *mesagent.ToolCatalog, error) {
	objectDefinition, err := mesagent.NewDatabaseObjectDefinitionTool(selectionSQLStub{})
	if err != nil {
		return nil, nil, err
	}
	catalogSearch, err := mesagent.NewSearchSchemaCatalogTool(selectionSQLStub{})
	if err != nil {
		return nil, nil, err
	}
	readonlyQuery, err := mesagent.NewExecuteReadonlyQueryTool(selectionSQLStub{})
	if err != nil {
		return nil, nil, err
	}
	// 注册真实 Skill reference Tool，使 read_skill_reference 与生产 Diagnosis
	// Profile 一致；skill 本身由 Eino Skill Middleware 拥有，Catalog 不伪造。
	dependencies := mesagent.DefaultToolCatalogDependencies{
		ExternalCases: selectionExternalCases{}, SkillReference: skillRuntime.ReferenceTool,
		GitHubTools:          githubTools,
		SQLObjectDefinitions: objectDefinition, SchemaCatalog: catalogSearch, ReadonlyQuery: readonlyQuery,
	}
	production, err := mesagent.NewDiagnosisDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return nil, nil, err
	}
	wide, err := mesagent.NewEvaluationWideDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return nil, nil, err
	}
	return production, wide, nil
}

// selectionEvalAssembly 是两臂的完整装配件：Catalog、Authorization
// Middleware、共享 Skill Middleware 与 preflight 得到的 comparison 身份。
type selectionEvalAssembly struct {
	productionCatalog       *mesagent.ToolCatalog
	wideCatalog             *mesagent.ToolCatalog
	productionAuthorization *mesagent.ToolAuthorizationMiddleware
	wideAuthorization       *mesagent.ToolAuthorizationMiddleware
	skillMiddleware         adk.ChatModelAgentMiddleware
	productionSchemaHash    string
	wideSchemaHash          string
	comparability           mesagent.ToolSelectionComparability
}

// selectionArmTools 把一臂装配成最终模型可见的 ToolInfo 列表：与生产
// Diagnosis Runner 相同的真实装配链（ToolAuthorizationMiddleware ->
// Eino Skill Middleware），不伪造任何 Schema。
func selectionArmTools(
	ctx context.Context,
	authorization *mesagent.ToolAuthorizationMiddleware,
	skillMiddleware adk.ChatModelAgentMiddleware,
) ([]*schema.ToolInfo, string, error) {
	accessCtx, err := withSelectionRunAccess(ctx)
	if err != nil {
		return nil, "", err
	}
	_, authorizedCtx, authErr := authorization.BeforeAgent(
		accessCtx, &adk.ChatModelAgentContext{Tools: nil},
	)
	if authErr != nil {
		return nil, "", fmt.Errorf("assemble Tool schema: %w", authErr)
	}
	_, finalCtx, skillErr := skillMiddleware.BeforeAgent(accessCtx, authorizedCtx)
	if skillErr != nil {
		return nil, "", fmt.Errorf("append skill Tool: %w", skillErr)
	}
	infos := make([]*schema.ToolInfo, 0, len(finalCtx.Tools))
	for _, current := range finalCtx.Tools {
		info, infoErr := current.Info(ctx)
		if infoErr != nil {
			return nil, "", infoErr
		}
		infos = append(infos, info)
	}
	metadataInfos, _, schemaHash, _, metadataErr := selectionToolInfoMetadata(infos)
	if metadataErr != nil {
		return nil, "", metadataErr
	}
	return metadataInfos, schemaHash, nil
}

// assembleSelectionEval 在创建任何收费 Provider 之前完成两臂装配与可比性
// preflight：production（diagnosis-default）与 wide（evaluation-wide-v2）
// 都经过同一个 Skill Middleware；VerifyToolSelectionComparability 校验
// 名字集合、共享 Schema 与严格超集，任何漂移直接 fail-closed。
func assembleSelectionEval(
	ctx context.Context,
	githubTools []tool.BaseTool,
	skillRuntime *mesagent.NativeSkillRuntime,
	verify func([]*schema.ToolInfo, []*schema.ToolInfo) (mesagent.ToolSelectionComparability, error),
) (*selectionEvalAssembly, error) {
	productionCatalog, wideCatalog, err := buildSelectionCatalogs(ctx, githubTools, skillRuntime)
	if err != nil {
		return nil, err
	}
	productionAuthorization, err := mesagent.NewToolAuthorizationMiddleware(productionCatalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		return nil, fmt.Errorf("build production Tool authorization middleware: %w", err)
	}
	wideAuthorization, err := mesagent.NewToolAuthorizationMiddleware(wideCatalog, agentruntime.ToolProfileEvaluationWide)
	if err != nil {
		return nil, fmt.Errorf("build wide Tool authorization middleware: %w", err)
	}
	productionInfos, productionSchemaHash, err := selectionArmTools(ctx, productionAuthorization, skillRuntime.Middleware)
	if err != nil {
		return nil, fmt.Errorf("assemble production arm: %w", err)
	}
	wideInfos, wideSchemaHash, err := selectionArmTools(ctx, wideAuthorization, skillRuntime.Middleware)
	if err != nil {
		return nil, fmt.Errorf("assemble wide arm: %w", err)
	}
	comparability, err := verify(productionInfos, wideInfos)
	if err != nil {
		return nil, fmt.Errorf("tool selection comparability preflight: %w", err)
	}
	return &selectionEvalAssembly{
		productionCatalog:       productionCatalog,
		wideCatalog:             wideCatalog,
		productionAuthorization: productionAuthorization,
		wideAuthorization:       wideAuthorization,
		skillMiddleware:         skillRuntime.Middleware,
		productionSchemaHash:    productionSchemaHash,
		wideSchemaHash:          wideSchemaHash,
		comparability:           comparability,
	}, nil
}

// 工具选择评测固定：每 Case 恰好 3 次 Provider 调用上界（base 校准 1 次 +
// wide 1 次 + production 1 次）。Token 硬上界不由本地估算或固定 16K 输入假设
// 派生，而是由最终命名 Profile 的 contextWindowTokens 派生（见
// validateToolSelectionProviderBudget）：每次调用硬上界 <= contextWindowTokens、
// 每 Case 硬上界 = 3 x contextWindowTokens。
const toolSelectionPerCaseEndpointCalls = 3

// checkedMultiply 对预算乘法做溢出检查：溢出 fail-closed，绝不回绕。
func checkedMultiply(factorA, factorB int) (int, error) {
	if factorA <= 0 || factorB <= 0 {
		return 0, errors.New("budget factors must be positive")
	}
	if factorA > math.MaxInt/factorB {
		return 0, fmt.Errorf("budget multiplication overflow (%d * %d)", factorA, factorB)
	}
	return factorA * factorB, nil
}

// validateToolSelectionProviderBudget 是统一成本闸门：缺授权、预算非正、Case
// 超限、调用或 Token 硬上界超限都必须 fail-closed。除非显式给出的命名 Profile
// contextWindowTokens 外不做任何本地 Token 估算：每 Case Token 硬上界 =
// 3 x contextWindowTokens（对应 1 次 base 校准 + 1 次 wide + 1 次 production，
// 每次调用上限为 Profile 的 contextWindowTokens），总硬上界 =
// Cases x 3 x contextWindowTokens；溢出 fail-closed。默认 45 Case 调用上界 =
// 45*3 = 135 次 Provider 调用。只做校验，绝不静默截取数据集。
func validateToolSelectionProviderBudget(
	cases int,
	perCaseEndpointCalls int,
	contextWindowTokens int,
	allowed bool,
	caseLimit int,
	providerCallLimit int,
	totalTokenLimit int,
) (toolSelectionProviderBudget, error) {
	if !allowed {
		return toolSelectionProviderBudget{}, errors.New("Provider run requires -allow-provider-calls")
	}
	if cases < 1 || caseLimit < 1 || providerCallLimit < 1 || totalTokenLimit < 1 {
		return toolSelectionProviderBudget{}, errors.New("Provider run requires positive -max-cases, -max-provider-calls, and -max-provider-tokens")
	}
	if cases > caseLimit {
		return toolSelectionProviderBudget{}, fmt.Errorf("dataset has %d cases, exceeds authorized max-cases %d", cases, caseLimit)
	}
	if perCaseEndpointCalls < 1 || contextWindowTokens < 1 {
		return toolSelectionProviderBudget{}, errors.New("effective per-case Provider call and context window budgets must be positive")
	}
	providerCalls, err := checkedMultiply(cases, perCaseEndpointCalls)
	if err != nil {
		return toolSelectionProviderBudget{}, err
	}
	perCaseTokens, err := checkedMultiply(perCaseEndpointCalls, contextWindowTokens)
	if err != nil {
		return toolSelectionProviderBudget{}, err
	}
	totalTokens, err := checkedMultiply(cases, perCaseTokens)
	if err != nil {
		return toolSelectionProviderBudget{}, err
	}
	budget := toolSelectionProviderBudget{
		Cases:         cases,
		ProviderCalls: providerCalls,
		TotalTokens:   totalTokens,
	}
	if budget.ProviderCalls > providerCallLimit {
		return toolSelectionProviderBudget{}, fmt.Errorf("estimated Provider call upper bound %d exceeds authorized max-provider-calls %d", budget.ProviderCalls, providerCallLimit)
	}
	if budget.TotalTokens > totalTokenLimit {
		return toolSelectionProviderBudget{}, fmt.Errorf("hard Token upper bound %d exceeds authorized max-provider-tokens %d", budget.TotalTokens, totalTokenLimit)
	}
	return budget, nil
}

type toolSelectionProviderBudget struct {
	Cases         int
	ProviderCalls int
	TotalTokens   int
}

// selectToolSelectionCases 在完整数据集校验之后、成本预算与 Provider 创建之前，
// 按精确 CaseID 选择单个 Case；空 caseID 返回全部 Case，保持历史行为。
// -max-cases 仍是授权上限：这里只做选择，绝不做"前 N 条"截断。
func selectToolSelectionCases(cases []mesagent.ToolSelectionCase, caseID string) ([]mesagent.ToolSelectionCase, error) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return cases, nil
	}
	for _, definition := range cases {
		if definition.CaseID == caseID {
			return []mesagent.ToolSelectionCase{definition}, nil
		}
	}
	return nil, fmt.Errorf("case-id %q does not match any case in the dataset", caseID)
}

// prepareToolSelectionProfile 解析 -profile：空值使用 activeProfile，非空值
// 使用精确命名 Profile（如 opencode-deepseek-main），支持不修改生产
// activeProfile 直接评测命名 Profile。评测变换（非空 ReasoningEffort 固定
// low、Temperature=0、MaxOutputTokens=toolSelectionMaxTokens）只作用于局部
// 副本；指纹基于最终副本与实际 Profile 名；绝不写回配置 Map，绝不替换
// activeProfile，绝不为 NewActive 临时切换。
func prepareToolSelectionProfile(
	chat config.ChatModelConfig,
	profileFlag string,
) (profileName string, finalProfile config.ChatModelProfileConfig, fingerprint string, err error) {
	profileName = strings.TrimSpace(profileFlag)
	if profileName == "" {
		profileName = strings.TrimSpace(chat.ActiveProfileName)
	}
	if profileName == "" {
		return "", config.ChatModelProfileConfig{}, "", errors.New("chat model profile name is empty")
	}
	finalProfile, err = chat.Profile(profileName)
	if err != nil {
		return "", config.ChatModelProfileConfig{}, "", err
	}
	if strings.TrimSpace(finalProfile.ReasoningEffort) != "" {
		finalProfile.ReasoningEffort = "low"
	}
	temperature := float32(0)
	finalProfile.Temperature = &temperature
	finalProfile.MaxOutputTokens = toolSelectionMaxTokens
	fingerprint, err = finalProfile.PromptProfileFingerprint(profileName)
	if err != nil {
		return "", config.ChatModelProfileConfig{}, "", fmt.Errorf("compute model profile fingerprint: %w", err)
	}
	return profileName, finalProfile, fingerprint, nil
}

type selectionExternalCases struct{}

func (selectionExternalCases) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("tool selection evaluation does not execute tools")
}

type selectionSQLStub struct{}

func (selectionSQLStub) GetObjectDefinition(context.Context, string, string) (string, string, bool, error) {
	return "", "", false, errors.New("tool selection evaluation does not execute tools")
}

func (selectionSQLStub) SearchPublished(context.Context, uuid.UUID, string, int) ([]repository.SchemaCatalogEntry, error) {
	return nil, errors.New("tool selection evaluation does not execute tools")
}

func (selectionSQLStub) Execute(context.Context, uuid.UUID, string) (repository.ReadonlyQueryResult, error) {
	return repository.ReadonlyQueryResult{}, errors.New("tool selection evaluation does not execute tools")
}

func readToolSelectionCases(path string) ([]mesagent.ToolSelectionCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []mesagent.ToolSelectionCase
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var current mesagent.ToolSelectionCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		result = append(result, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("dataset contains no cases")
	}
	return result, nil
}
