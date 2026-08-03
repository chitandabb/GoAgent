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
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/repository"

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
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-tool-selection-eval [-dataset path] [-output path] [-summary path] [-concurrency 1..8]")
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
	// 工具选择评测固定低推理强度；不修改生产配置文件。
	cfg.Models.Chat.ReasoningEffort = "low"
	chatModel, err := platformchatmodel.NewStepFun(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build StepFun model: %w", err)
	}
	githubConnection, err := githubmcp.Connect(ctx, cfg.GitHubMCP, log.Named("github_mcp"))
	if err != nil {
		return fmt.Errorf("connect GitHub MCP: %w", err)
	}
	defer githubConnection.Close()
	catalog, err := buildSelectionCatalog(ctx, githubConnection.Tools)
	if err != nil {
		return err
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
						evalCtx, chatModel, catalog, cfg, current.definition, variant, basePromptTokens,
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
	catalog *mesagent.ToolCatalog,
	cfg config.Config,
	definition mesagent.ToolSelectionCase,
	variant mesagent.ToolSelectionVariant,
	basePromptTokens int,
) (mesagent.ToolSelectionObservation, error) {
	scope, err := selectionScope(definition.Scope)
	if err != nil {
		return mesagent.ToolSelectionObservation{}, err
	}
	var available []tool.BaseTool
	if variant == mesagent.ToolSelectionWide {
		available, err = catalog.EvaluationBaselineToolsFor(ctx, scope)
	} else {
		available, err = catalog.ToolsFor(ctx, scope)
	}
	if err != nil {
		return mesagent.ToolSelectionObservation{}, fmt.Errorf("resolve tools: %w", err)
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
		RunID:         fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		ModelProvider: cfg.Models.Chat.Provider, ModelID: cfg.Models.Chat.Model,
		ReasoningEffort: cfg.Models.Chat.ReasoningEffort, PromptVersion: toolSelectionPromptVersion,
		MaxOutputTokens: toolSelectionMaxTokens,
		AvailableTools:  names, ToolSchemaHash: schemaHash, ToolSchemaBytes: schemaBytes,
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

func selectionScope(kind mesagent.ToolSelectionScope) (mesagent.TaskScope, error) {
	dependencies := []mesagent.ToolDependency{mesagent.ToolDependencyExternalCase}
	dataSources := []mesagent.ScopedDataSource{{
		ID: selectionCaseSourceID, Role: mesagent.DataSourceRoleCaseSource,
		SafetyMode: mesagent.DataSourceSafetyReadOnly,
	}}
	capabilities := []mesagent.ToolCapability{mesagent.ToolCapabilityCase}
	switch kind {
	case mesagent.ToolSelectionTicket:
	case mesagent.ToolSelectionGitHub:
		dependencies = append(dependencies, mesagent.ToolDependencyGitHubMCP)
		capabilities = append(capabilities, mesagent.ToolCapabilityCode)
	case mesagent.ToolSelectionSQL:
		dependencies = append(dependencies, mesagent.ToolDependencySQLServer)
		capabilities = append(capabilities, mesagent.ToolCapabilitySQL)
		dataSources = append(dataSources, mesagent.ScopedDataSource{
			ID: selectionSQLSourceID, Role: mesagent.DataSourceRoleProduction,
			SafetyMode: mesagent.DataSourceSafetyReadOnly,
		})
	default:
		return mesagent.TaskScope{}, fmt.Errorf("unsupported selection scope %q", kind)
	}
	return mesagent.NewTaskScope(mesagent.TaskScopeConfig{
		UserID: selectionUserID, Role: auth.RoleAnalyst, TaskType: mesagent.TaskTypeDiagnosis,
		DataSources: dataSources, AllowedCapabilities: capabilities, AvailableDependencies: dependencies,
	})
}

func buildSelectionCatalog(ctx context.Context, githubTools []tool.BaseTool) (*mesagent.ToolCatalog, error) {
	objectDefinition, err := mesagent.NewDatabaseObjectDefinitionTool(selectionSQLStub{})
	if err != nil {
		return nil, err
	}
	catalogSearch, err := mesagent.NewSearchSchemaCatalogTool(selectionSQLStub{})
	if err != nil {
		return nil, err
	}
	readonlyQuery, err := mesagent.NewExecuteReadonlyQueryTool(selectionSQLStub{})
	if err != nil {
		return nil, err
	}
	return mesagent.NewDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: selectionExternalCases{}, GitHubTools: githubTools,
		SQLObjectDefinitions: objectDefinition, SchemaCatalog: catalogSearch, ReadonlyQuery: readonlyQuery,
	})
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
