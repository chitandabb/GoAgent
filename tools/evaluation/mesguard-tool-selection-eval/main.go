// Command mesguard-tool-selection-eval 对相同业务请求执行单轮宽/过滤 Tool Schema 配对评测。
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
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
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
	flags := flag.NewFlagSet("mesguard-tool-selection-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/tool-selection-v1.jsonl", "versioned JSONL cases")
	outputPath := flags.String("output", "testdata/tool-selection-v1.observations.jsonl", "observation JSONL output")
	summaryPath := flags.String("summary", "testdata/tool-selection-v1.summary.json", "summary JSON output")
	concurrency := flags.Int("concurrency", 4, "parallel cases; each case keeps paired variants sequential")
	allowDirty := flags.Bool("allow-dirty", false, "accept a dirty/unknown implementation revision for local smoke; results are NOT formal metrics")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-tool-selection-eval [-dataset path] [-output path] [-summary path] [-concurrency 1..8] [-allow-dirty]")
	}
	if *concurrency < 1 || *concurrency > 8 {
		return errors.New("concurrency must be between 1 and 8")
	}
	cases, err := readToolSelectionCases(*datasetPath)
	if err != nil {
		return fmt.Errorf("read tool selection dataset: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.GitHubMCP.Enabled {
		return errors.New("chat model and GitHub MCP must be enabled")
	}
	// 身份校验必须在调用任何收费 Provider 之前完成：先解析实现 revision，
	// 再决定是否允许继续。
	identity, identityErr := resolveImplementationIdentity()
	if identityErr != nil && !*allowDirty {
		return fmt.Errorf("resolve implementation revision: %w (pass -allow-dirty for local smoke)", identityErr)
	}
	identity, decisionErr := evaluateImplementationIdentity(identity, *allowDirty)
	if decisionErr != nil {
		return decisionErr
	}
	if identityErr != nil || identity.dirty || identity.revision == "unknown" {
		log.Warn("dirty or unknown implementation revision accepted for local smoke only; observations are NOT formal metrics",
			zap.String("revision", identity.revision), zap.Bool("dirty", identity.dirty))
	}
	// 工具选择评测固定低推理强度：先完成全部 Profile 变换，写回配置，再基于
	// 最终 Profile 计算 PromptProfileFingerprint，最后创建 Provider。指纹与
	// 实际模型调用配置必须完全一致。
	modelProfile, modelProfileFingerprint, err := prepareToolSelectionModelProfile(cfg.Models.Chat)
	if err != nil {
		return err
	}
	instance, err := platformchatmodel.NewActive(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}
	chatModel := instance.Model
	githubConnection, err := githubmcp.Connect(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
	if err != nil {
		return fmt.Errorf("connect GitHub MCP: %w", err)
	}
	defer githubConnection.Close()
	skillRuntime, err := mesagent.NewNativeSkillRuntime(ctx, cfg.Agent.SkillsDirectory)
	if err != nil {
		return fmt.Errorf("build native Skill runtime: %w", err)
	}
	catalog, wideCatalog, err := buildSelectionCatalogs(ctx, githubConnection.Tools, skillRuntime)
	if err != nil {
		return err
	}
	// 与生产 Diagnosis Runner 相同的装配入口：固定 diagnosis-default Profile
	// 由 Middleware 注入 Catalog-owned Tool，真实 Eino Skill Middleware 追加
	// 真实 skill Tool。wide 臂使用独立 evaluation-wide-v1 Profile。
	authorization, err := mesagent.NewToolAuthorizationMiddleware(catalog, agentruntime.ToolProfileDiagnosis)
	if err != nil {
		return fmt.Errorf("build Tool authorization middleware: %w", err)
	}
	wideAuthorization, err := mesagent.NewToolAuthorizationMiddleware(wideCatalog, agentruntime.ToolProfileEvaluationWide)
	if err != nil {
		return fmt.Errorf("build wide Tool authorization middleware: %w", err)
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
				variants := []mesagent.ToolSelectionVariant{mesagent.ToolSelectionWide, mesagent.ToolSelectionFiltered}
				if current.index%2 == 1 {
					variants[0], variants[1] = variants[1], variants[0]
				}
				currentResult := caseResult{observations: make([]mesagent.ToolSelectionObservation, 0, 2)}
				for _, variant := range variants {
					observation, observeErr := observeToolSelection(
						evalCtx, chatModel, wideCatalog, authorization, wideAuthorization,
						skillRuntime.Middleware,
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
	wideCatalog *mesagent.ToolCatalog,
	authorization *mesagent.ToolAuthorizationMiddleware,
	wideAuthorization *mesagent.ToolAuthorizationMiddleware,
	skillMiddleware adk.ChatModelAgentMiddleware,
	definition mesagent.ToolSelectionCase,
	variant mesagent.ToolSelectionVariant,
	basePromptTokens int,
	identity implementationIdentity,
	modelProfileFingerprint string,
	modelProfile config.ChatModelProfileConfig,
) (mesagent.ToolSelectionObservation, error) {
	var available []tool.BaseTool
	var toolProfileID string
	var modelVisibleNames []string
	if variant == mesagent.ToolSelectionWide {
		// wide baseline 使用独立 evaluation-wide-v1 固定 Profile：全部实际
		// 注册的业务 Tool（无 skill/read_skill_reference），不得伪装成
		// diagnosis-default。它不再依赖 ToolsFor(TaskScope)。
		// wide 臂不使用 Skill 中间件，直接以 Profile 名单作为最终 Schema。
		accessCtx, err := withSelectionRunAccess(ctx)
		if err != nil {
			return mesagent.ToolSelectionObservation{}, err
		}
		_, authorizedCtx, authErr := wideAuthorization.BeforeAgent(
			accessCtx, &adk.ChatModelAgentContext{Tools: nil},
		)
		if authErr != nil {
			return mesagent.ToolSelectionObservation{}, fmt.Errorf("assemble wide Tool schema: %w", authErr)
		}
		available = authorizedCtx.Tools
		names, namesErr := mesagent.ToolNamesFromTools(ctx, available)
		if namesErr != nil {
			return mesagent.ToolSelectionObservation{}, namesErr
		}
		modelVisibleNames = names
		toolProfileID = string(agentruntime.ToolProfileEvaluationWide)
	} else {
		// experiment 基于固定 diagnosis-default Profile，并使用与生产
		// Diagnosis Runner 相同的最终 Schema 装配：ToolAuthorizationMiddleware
		// 注入 Catalog-owned Tool，真实 Eino Skill Middleware 追加真实 skill。
		// ModelVisibleNames、AvailableTools 与 ToolSchemaHash 都描述同一份
		// 真正传给模型的 Schema。
		accessCtx, err := withSelectionRunAccess(ctx)
		if err != nil {
			return mesagent.ToolSelectionObservation{}, err
		}
		_, authorizedCtx, authErr := authorization.BeforeAgent(
			accessCtx, &adk.ChatModelAgentContext{Tools: nil},
		)
		if authErr != nil {
			return mesagent.ToolSelectionObservation{}, fmt.Errorf("assemble production Tool schema: %w", authErr)
		}
		_, finalCtx, skillErr := skillMiddleware.BeforeAgent(accessCtx, authorizedCtx)
		if skillErr != nil {
			return mesagent.ToolSelectionObservation{}, fmt.Errorf("append production skill Tool: %w", skillErr)
		}
		available = finalCtx.Tools
		names, namesErr := mesagent.ToolNamesFromTools(ctx, available)
		if namesErr != nil {
			return mesagent.ToolSelectionObservation{}, namesErr
		}
		modelVisibleNames = names
		toolProfileID = string(agentruntime.ToolProfileDiagnosis)
	}
	infos, names, schemaHash, schemaBytes, err := selectionToolSchemas(ctx, available)
	if err != nil {
		return mesagent.ToolSelectionObservation{}, err
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
		ObservationSchemaVersion: mesagent.ToolSelectionObservationV2,
		ModelProvider:            modelProfile.Provider, ModelID: modelProfile.Model,
		ReasoningEffort: modelProfile.ReasoningEffort, PromptVersion: toolSelectionPromptVersion,
		MaxOutputTokens: toolSelectionMaxTokens,
		ToolProfileID:   toolProfileID, ModelVisibleNames: modelVisibleNames,
		ModelProfileFingerprint: modelProfileFingerprint,
		ImplementationRevision:  identity.revision, ImplementationDirty: identity.dirty,
		AvailableTools: names, ToolSchemaHash: schemaHash, ToolSchemaBytes: schemaBytes,
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
	filtered, err := mesagent.NewDiagnosisDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return nil, nil, err
	}
	wide, err := mesagent.NewEvaluationWideDefaultToolCatalog(ctx, dependencies)
	if err != nil {
		return nil, nil, err
	}
	return filtered, wide, nil
}

// implementationIdentity 记录实现提交与其工作树状态。正式评测要求
// revision 具体且工作树干净；dirty/unknown 结果只能作为本地 smoke，不能
// 作为正式指标。
type implementationIdentity struct {
	revision string
	dirty    bool
}

const (
	gitRevParseTimeout = 2 * time.Second
	gitStatusTimeout   = 2 * time.Second
)

// git 命令执行 seam：评测命令默认走真实 git；测试替换为桩函数，避免依赖
// 真实 git 工作树状态或故障注入。
var (
	gitRevParseShortHead = func(ctx context.Context) (string, error) {
		output, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
		return string(output), err
	}
	gitStatusPorcelain = func(ctx context.Context) (string, error) {
		output, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
		return string(output), err
	}
)

// resolveImplementationIdentity fail-closed 地解析实现身份：优先读取 Go
// build info 的 VCS 元数据，且必须同时确认 revision 与 modified 状态；
// BuildInfo 缺失或不完整时回退到带独立超时的 git 命令。git status 失败时
// 不能默认 clean——无法确认工作树状态一律返回 error，且 identity 保留
// gitFallbackIdentity 已经取得的已知 revision + dirty=true，不得覆盖成
// unknown。任何无法确认 clean/dirty 的情况都以 error 返回。
func resolveImplementationIdentity() (implementationIdentity, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision, modified string
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
		if revision != "" && modified != "" {
			return implementationIdentity{revision: revision, dirty: modified == "true"}, nil
		}
	}
	// BuildInfo 缺失或不完整（revision/modified 缺一）时回退 git。
	// git 返回 error 时原样保留它已取得的 identity（已知 revision 或
	// unknown），不覆盖为 unknown。
	identity, err := gitFallbackIdentity()
	if err != nil {
		return identity, err
	}
	return identity, nil
}

func gitFallbackIdentity() (implementationIdentity, error) {
	revCtx, cancelRev := context.WithTimeout(context.Background(), gitRevParseTimeout)
	defer cancelRev()
	revisionOutput, err := gitRevParseShortHead(revCtx)
	if err != nil {
		return implementationIdentity{revision: "unknown", dirty: true}, fmt.Errorf("git rev-parse failed: %w", err)
	}
	revision := strings.TrimSpace(revisionOutput)
	if revision == "" {
		return implementationIdentity{revision: "unknown", dirty: true}, errors.New("git rev-parse returned an empty revision")
	}
	statusCtx, cancelStatus := context.WithTimeout(context.Background(), gitStatusTimeout)
	defer cancelStatus()
	statusOutput, err := gitStatusPorcelain(statusCtx)
	if err != nil {
		// git status 失败不能默认 dirty=false：无法确认工作树状态。
		return implementationIdentity{revision: revision, dirty: true}, fmt.Errorf("git status failed: %w", err)
	}
	return implementationIdentity{
		revision: revision, dirty: len(bytes.TrimSpace([]byte(statusOutput))) > 0,
	}, nil
}

// evaluateImplementationIdentity 决定身份是否可用于正式评测。formal 模式
// 下 dirty/unknown 直接拒绝；-allow-dirty 模式接受并强制记录 dirty=true，
// 结果仅用于本地 smoke。
func evaluateImplementationIdentity(identity implementationIdentity, allowDirty bool) (implementationIdentity, error) {
	if identity.revision == "unknown" || identity.dirty {
		if !allowDirty {
			return implementationIdentity{}, errors.New("implementation revision is dirty or unknown; refuse formal evaluation (pass -allow-dirty for local smoke)")
		}
		return implementationIdentity{revision: identity.revision, dirty: true}, nil
	}
	return identity, nil
}

// prepareToolSelectionModelProfile 完成评测需要的全部实际模型参数变换后，
// 把最终 Profile 写回配置，并基于最终 Profile 计算 PromptProfileFingerprint。
// 顺序固定为：读取 ActiveProfile -> 应用所有变换（ReasoningEffort 非空时
// 置 low、空时保持为空；Temperature 置 0；MaxOutputTokens 置
// toolSelectionMaxTokens）-> 写回 cfg.Models.Chat.Profiles -> 计算指纹 ->
// 创建 Provider，保证记录的指纹与实际模型调用配置一致。
func prepareToolSelectionModelProfile(
	models config.ChatModelConfig,
) (config.ChatModelProfileConfig, string, error) {
	profile, err := models.ActiveProfile()
	if err != nil {
		return config.ChatModelProfileConfig{}, "", err
	}
	if strings.TrimSpace(profile.ReasoningEffort) != "" {
		profile.ReasoningEffort = "low"
	}
	temperature := float32(0)
	profile.Temperature = &temperature
	profile.MaxOutputTokens = toolSelectionMaxTokens
	models.Profiles[models.ActiveProfileName] = profile
	fingerprint, err := profile.PromptProfileFingerprint(models.ActiveProfileName)
	if err != nil {
		return config.ChatModelProfileConfig{}, "", fmt.Errorf("compute model profile fingerprint: %w", err)
	}
	return profile, fingerprint, nil
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
