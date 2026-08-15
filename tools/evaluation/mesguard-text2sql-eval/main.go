// Command mesguard-text2sql-eval evaluates model-generated read-only T-SQL
// against the fixed SQL Server fixture.
//
// Two explicit run modes:
//
//   - direct (default): the historical forced Tool Calling capability test.
//     The model sees exactly one Tool and must emit one execute_readonly_query
//     call with a single SELECT; the harness then executes it through the real
//     ReadonlyQueryExecutor + QueryGuard. This mode keeps its historical
//     semantics unchanged.
//   - conversation: the production entry test. Natural-language user input
//     from the versioned dataset goes through the real Conversation Agent
//     assembly (NewConversationDefaultToolCatalog + NewConversationRunner +
//     NewSearchSchemaCatalogTool + NewExecuteReadonlyQueryTool + the real
//     platform/sqlserver ReadonlyQueryExecutor and QueryGuard). The model
//     autonomously decides search_schema_catalog -> execute_readonly_query ->
//     final natural-language answer. Observations use the independent
//     text-to-sql-conversation-observation-v2 contract; historical direct v1
//     data is never mixed into conversation v2 summaries.
//
// Cost guardrails (-allow-provider-calls, -max-cases, -max-provider-calls,
// -max-provider-tokens) and the clean implementation revision are checked
// before any Provider is created.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	texttosqleval "github.com/chitandabb/GoAgent/internal/evaluation/texttosql"
	"github.com/chitandabb/GoAgent/internal/evaluationidentity"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"
	"github.com/chitandabb/GoAgent/internal/repository"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	textToSQLPromptVersion = "text-to-sql-v1"
	textToSQLMaxTokens     = 1024
)

var textToSQLCatalogVersionID = uuid.MustParse("55555555-5555-5555-5555-555555555555")

const (
	textToSQLDirectOutputPath        = "testdata/text-to-sql-v1.observations.jsonl"
	textToSQLDirectSummaryPath       = "testdata/text-to-sql-v1.summary.json"
	textToSQLConversationOutputPath  = "testdata/text-to-sql-conversation-v2.observations.jsonl"
	textToSQLConversationSummaryPath = "testdata/text-to-sql-conversation-v2.summary.json"
)

type textToSQLMode string

const (
	textToSQLModeDirect       textToSQLMode = "direct"
	textToSQLModeConversation textToSQLMode = "conversation"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runEvaluation(args, func() (config.Config, error) { return config.Load() }, newActiveChatModel)
}

// textToSQLModelFactory 是 Provider 构造 seam：正式运行走真实
// platformchatmodel.NewActive；测试替换为桩，证明护栏先于 Provider。
type textToSQLModelFactory func(context.Context, config.ChatModelConfig) (model.ToolCallingChatModel, error)

var newActiveChatModel textToSQLModelFactory = func(ctx context.Context, cfg config.ChatModelConfig) (model.ToolCallingChatModel, error) {
	instance, err := platformchatmodel.NewActive(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return instance.Model, nil
}

func runEvaluation(
	args []string,
	loadConfig func() (config.Config, error),
	newModel textToSQLModelFactory,
) error {
	flags := flag.NewFlagSet("mesguard-text2sql-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", string(textToSQLModeDirect), "evaluation entry mode: direct (historical forced Tool Calling capability test) or conversation (production Conversation Agent entry)")
	datasetPath := flags.String("dataset", "testdata/text-to-sql-v1.jsonl", "versioned JSONL execution cases")
	outputPath := flags.String("output", "", "observation JSONL output (default per mode)")
	summaryPath := flags.String("summary", "", "summary JSON output (default per mode)")
	timeout := flags.Duration("timeout", 10*time.Minute, "total evaluation timeout")
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "explicitly authorize Provider calls for this evaluation run")
	maxCases := flags.Int("max-cases", 0, "authorized case count cap for Provider runs")
	maxProviderCalls := flags.Int("max-provider-calls", 0, "authorized Provider call upper bound")
	maxProviderTokens := flags.Int("max-provider-tokens", 0, "authorized Provider Token upper bound")
	allowDirty := flags.Bool("allow-dirty", false, "accept a dirty/unknown implementation revision for local smoke; results are NOT formal metrics")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mesguard-text2sql-eval [-mode direct|conversation] [-dataset path] [-output path] [-summary path] [-timeout duration] [-allow-provider-calls] [-max-cases N] [-max-provider-calls N] [-max-provider-tokens N] [-allow-dirty]")
	}
	selectedMode := textToSQLMode(strings.ToLower(strings.TrimSpace(*mode)))
	switch selectedMode {
	case textToSQLModeDirect, textToSQLModeConversation:
	default:
		return errors.New("mode must be direct or conversation")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	cases, err := readTextToSQLCases(*datasetPath)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled || !cfg.SQLServer.Enabled {
		return errors.New("chat model and SQL Server must be enabled")
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	if selectedMode == textToSQLModeDirect {
		// direct 模式保持历史语义：推理强度非空时固定为 low。
		if strings.TrimSpace(profile.ReasoningEffort) != "" {
			profile.ReasoningEffort = "low"
		}
	}
	cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName] = profile
	dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
	if err != nil {
		return errors.New("configured SQL Server ID is invalid")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// 成本护栏与实现身份必须在创建任何 Provider 之前检查：dirty/unknown
	// 实现、缺少显式授权或预算不足都直接拒绝，不创建 Provider。
	identity, identityErr := evaluationidentity.ResolveImplementationIdentity()
	if identityErr != nil && !*allowDirty {
		return fmt.Errorf("resolve implementation revision: %w (pass -allow-dirty for local smoke)", identityErr)
	}
	identity, err = evaluationidentity.EvaluateImplementationIdentity(identity, *allowDirty)
	if err != nil {
		return err
	}
	perCaseCalls, perCaseTokens := perCaseTextToSQLBudget(selectedMode, cases, dataSourceID, cfg)
	budget, err := validateTextToSQLProviderBudget(
		len(cases), perCaseCalls, perCaseTokens,
		*allowProviderCalls, *maxCases, *maxProviderCalls, *maxProviderTokens,
	)
	if err != nil {
		return err
	}
	if identityErr != nil || identity.Dirty || identity.Revision == "unknown" {
		fmt.Fprintf(os.Stderr,
			"WARNING dirty or unknown implementation revision accepted for local smoke only; observations are NOT formal metrics (revision=%s dirty=%t)\n",
			identity.Revision, identity.Dirty)
	}
	fmt.Fprintf(os.Stdout,
		"mesguard-text2sql-eval mode=%s cases=%d authorized_provider_call_upper_bound=%d authorized_token_upper_bound=%d revision=%s dirty=%t\n",
		selectedMode, budget.Cases, budget.ProviderCalls, budget.TotalTokens, identity.Revision, identity.Dirty)
	// 指纹基于写回后的最终 Profile 计算，保证记录的指纹与实际模型调用配置一致。
	modelProfileFingerprint, err := profile.PromptProfileFingerprint(cfg.Models.Chat.ActiveProfileName)
	if err != nil {
		return fmt.Errorf("compute model profile fingerprint: %w", err)
	}

	chatModel, err := newModel(ctx, cfg.Models.Chat)
	if err != nil {
		return fmt.Errorf("build chat model: %w", err)
	}

	switch selectedMode {
	case textToSQLModeDirect:
		return runDirectTextToSQL(ctx, cfg, chatModel, cases, dataSourceID, *outputPath, *summaryPath)
	case textToSQLModeConversation:
		return runConversationTextToSQL(ctx, cfg, chatModel, cases, dataSourceID, profile,
			identity, modelProfileFingerprint, budget, *outputPath, *summaryPath)
	}
	return nil
}

// perCaseTextToSQLBudget 计算每个 case 的 Provider 调用与 Token 保守上界：
// direct 恰好一次 Generate，Token 上界 = 字节数（系统提示 + 用户输入，字节
// 数恒 ≥ Provider token 数）+ 最大补全 token；conversation 每 case 上限为
// Runner 的迭代/Token 预算（与 NewConversationRunner 默认一致）。
func perCaseTextToSQLBudget(
	mode textToSQLMode,
	cases []mesagent.TextToSQLEvaluationCase,
	dataSourceID uuid.UUID,
	cfg config.Config,
) (calls, tokens int) {
	if mode == textToSQLModeConversation {
		calls = cfg.Agent.ConversationMaxIterations
		if calls < 1 {
			calls = 8
		}
		tokens = cfg.Agent.MaxTotalTokens
		if tokens < 1 {
			tokens = 16000
		}
		return calls, tokens
	}
	maxBytes := 0
	systemPrompt := textToSQLSystemPrompt(dataSourceID)
	for _, definition := range cases {
		size := len(systemPrompt) + len(definition.UserQuery)
		if size > maxBytes {
			maxBytes = size
		}
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return 1, maxBytes + textToSQLMaxTokens
}

type textToSQLProviderBudget struct {
	Cases         int
	ProviderCalls int
	TotalTokens   int
}

func validateTextToSQLProviderBudget(
	cases int,
	perCaseProviderCalls int,
	perCaseTotalTokens int,
	allowed bool,
	caseLimit int,
	providerCallLimit int,
	totalTokenLimit int,
) (textToSQLProviderBudget, error) {
	if !allowed {
		return textToSQLProviderBudget{}, errors.New("Provider run requires -allow-provider-calls")
	}
	if cases < 1 || caseLimit < 1 || providerCallLimit < 1 || totalTokenLimit < 1 {
		return textToSQLProviderBudget{}, errors.New("Provider run requires positive -max-cases, -max-provider-calls, and -max-provider-tokens")
	}
	if cases > caseLimit {
		return textToSQLProviderBudget{}, fmt.Errorf("dataset has %d cases, exceeds authorized max-cases %d", cases, caseLimit)
	}
	if perCaseProviderCalls < 1 || perCaseTotalTokens < 1 {
		return textToSQLProviderBudget{}, errors.New("effective per-case Provider call and Token budgets must be positive")
	}
	budget := textToSQLProviderBudget{
		Cases:         cases,
		ProviderCalls: cases * perCaseProviderCalls,
		TotalTokens:   cases * perCaseTotalTokens,
	}
	if budget.ProviderCalls > providerCallLimit {
		return textToSQLProviderBudget{}, fmt.Errorf("estimated Provider call upper bound %d exceeds authorized max-provider-calls %d", budget.ProviderCalls, providerCallLimit)
	}
	if budget.TotalTokens > totalTokenLimit {
		return textToSQLProviderBudget{}, fmt.Errorf("estimated Token upper bound %d exceeds authorized max-provider-tokens %d", budget.TotalTokens, totalTokenLimit)
	}
	return budget, nil
}

func runDirectTextToSQL(
	ctx context.Context,
	cfg config.Config,
	chatModel model.ToolCallingChatModel,
	cases []mesagent.TextToSQLEvaluationCase,
	dataSourceID uuid.UUID,
	outputPath, summaryPath string,
) error {
	if outputPath == "" {
		outputPath = textToSQLDirectOutputPath
	}
	if summaryPath == "" {
		summaryPath = textToSQLDirectSummaryPath
	}
	selectionTool, err := mesagent.NewExecuteReadonlyQueryTool(noopQueryExecutor{})
	if err != nil {
		return fmt.Errorf("build evaluation Tool schema: %w", err)
	}
	toolInfo, err := selectionTool.Info(ctx)
	if err != nil {
		return fmt.Errorf("read evaluation Tool schema: %w", err)
	}
	boundModel, err := chatModel.WithTools([]*schema.ToolInfo{toolInfo})
	if err != nil {
		return fmt.Errorf("bind evaluation Tool: %w", err)
	}

	db, executor, err := openTextToSQLReadonlyExecutor(ctx, cfg.SQLServer, dataSourceID)
	if err != nil {
		return err
	}
	defer db.Close()

	observations := make([]mesagent.TextToSQLEvaluationObservation, 0, len(cases))
	for _, definition := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := observeTextToSQL(ctx, boundModel, executor, cfg, dataSourceID, definition)
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("validate case %q: %w", definition.CaseID, err)
		}
		observations = append(observations, observation)
		fmt.Fprintf(os.Stdout, "%s correct=%t error=%s\n", definition.CaseID, observation.Correct, observation.ErrorType)
	}
	summary, err := mesagent.EvaluateTextToSQL(cases, observations)
	if err != nil {
		return err
	}
	return writeTextToSQLEvaluationFiles(outputPath, summaryPath, observations, summary)
}

func runConversationTextToSQL(
	ctx context.Context,
	cfg config.Config,
	chatModel model.ToolCallingChatModel,
	cases []mesagent.TextToSQLEvaluationCase,
	dataSourceID uuid.UUID,
	profile config.ChatModelProfileConfig,
	identity evaluationidentity.Identity,
	modelProfileFingerprint string,
	budget textToSQLProviderBudget,
	outputPath, summaryPath string,
) error {
	if outputPath == "" {
		outputPath = textToSQLConversationOutputPath
	}
	if summaryPath == "" {
		summaryPath = textToSQLConversationSummaryPath
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return fmt.Errorf("load Agent prompts: %w", err)
	}
	if strings.TrimSpace(prompts.ConversationInstruction) == "" {
		return errors.New("conversation instruction is required")
	}
	if strings.TrimSpace(cfg.Agent.ConversationPromptVersion) == "" {
		return errors.New("conversationPromptVersion is required")
	}
	db, executor, err := openTextToSQLReadonlyExecutor(ctx, cfg.SQLServer, dataSourceID)
	if err != nil {
		return err
	}
	defer db.Close()
	maxIterations := cfg.Agent.ConversationMaxIterations
	if maxIterations < 1 {
		maxIterations = 8
	}
	maxToolCalls := cfg.Agent.MaxToolCalls
	if maxToolCalls < 1 {
		maxToolCalls = 8
	}
	maxTotalTokens := cfg.Agent.MaxTotalTokens
	if maxTotalTokens < 1 {
		maxTotalTokens = 16000
	}
	maxContextRunes := cfg.Agent.ConversationMaxContextRunes
	if maxContextRunes < 1 {
		maxContextRunes = 32000
	}
	timeoutMillis := cfg.Agent.ConversationTimeoutMillis
	if timeoutMillis < 1 {
		timeoutMillis = 60000
	}
	assembly, err := buildConversationEvaluation(ctx, conversationEvaluationDependencies{
		chatModel:         chatModel,
		externalCases:     unavailableExternalCaseGetter{},
		schemaSearcher:    fixedSchemaCatalogSearcher{},
		readonlyExecutor:  executor,
		logger:            zap.NewNop(),
		systemInstruction: prompts.ConversationInstruction,
		modelProvider:     profile.Provider,
		modelID:           profile.Model,
		promptVersion:     cfg.Agent.ConversationPromptVersion,
		maxIterations:     maxIterations,
		maxToolCalls:      maxToolCalls,
		maxTotalTokens:    maxTotalTokens,
		maxContextRunes:   maxContextRunes,
		timeout:           time.Duration(timeoutMillis) * time.Millisecond,
		sqlDataSourceID:   dataSourceID,
	})
	if err != nil {
		return err
	}
	reasoningEffort := strings.TrimSpace(profile.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = "none"
	}
	observationIdentity := conversationEvaluationIdentity{
		modelProvider:           profile.Provider,
		modelID:                 profile.Model,
		reasoningEffort:         reasoningEffort,
		promptVersion:           cfg.Agent.ConversationPromptVersion,
		modelProfileFingerprint: modelProfileFingerprint,
		implementationRevision:  identity.Revision,
		implementationDirty:     identity.Dirty,
		toolProfileID:           string(agentruntime.ToolProfileConversation),
		toolSchemaFingerprint:   assembly.toolSchemaFingerprint,
	}
	observations := make([]texttosqleval.TextToSQLConversationEvaluationObservation, 0, len(cases))
	for _, definition := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := observeConversationCase(ctx, assembly, definition, observationIdentity)
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("validate case %q: %w", definition.CaseID, err)
		}
		observations = append(observations, observation)
		fmt.Fprintf(os.Stdout, "%s correct=%t error=%s tools=%d traceComplete=%t sequenceCorrect=%t\n",
			definition.CaseID, observation.Correct, observation.ErrorType, observation.ActualToolCallCount,
			observation.ToolTraceComplete, observation.ToolSequenceCorrect)
	}
	summary, err := texttosqleval.EvaluateTextToSQLConversation(cases, observations)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"conversation summary cases=%d formal=%t implementationDirty=%t toolSequenceCorrect=%d executionCorrect=%d answerCorrect=%d endToEndCorrect=%d toolSequenceAccuracy=%.3f executionAccuracy=%.3f answerAccuracy=%.3f endToEndAccuracy=%.3f usage=%+v\n",
		summary.Cases, summary.Formal, summary.ImplementationDirty, summary.ToolSequenceCorrect,
		summary.ExecutionCorrect, summary.AnswerCorrect, summary.EndToEndCorrect,
		summary.ToolSequenceAccuracy, summary.ExecutionAccuracy, summary.AnswerAccuracy,
		summary.EndToEndAccuracy, summary.Usage,
	)
	return writeTextToSQLConversationEvaluationFiles(outputPath, summaryPath, observations, summary)
}

func openTextToSQLReadonlyExecutor(
	ctx context.Context,
	cfg config.SQLServerConfig,
	dataSourceID uuid.UUID,
) (*sql.DB, repository.ReadonlyQueryExecutor, error) {
	db, err := platformsqlserver.Open(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open SQL Server: %w", err)
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pingCtx)
	pingCancel()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping SQL Server: %w", err)
	}
	executor, err := platformsqlserver.NewReadonlyQueryExecutor(
		db, cfg, fixedCatalogAuthorizer{dataSourceID: dataSourceID}, zap.NewNop(),
	)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("build readonly query executor: %w", err)
	}
	return db, executor, nil
}

// ---------------------------------------------------------------------------
// conversation 模式装配与观测

// conversationEvaluationDependencies 是 conversation 评测的生产装配依赖：
// 与生产 Conversation Agent 相同的入口（NewConversationDefaultToolCatalog +
// NewConversationRunner + NewSearchSchemaCatalogTool + NewExecuteReadonlyQueryTool
// + 真实 ReadonlyQueryExecutor/QueryGuard）。评测 harness 只在 Tool 边界加
// 只读记录层，绝不直接调用 SQL Tool，也不强制 Tool Choice。
type conversationEvaluationDependencies struct {
	chatModel         model.ToolCallingChatModel
	externalCases     mesagent.ExternalCaseGetter
	schemaSearcher    mesagent.SchemaCatalogSearcher
	readonlyExecutor  repository.ReadonlyQueryExecutor
	logger            *zap.Logger
	systemInstruction string
	modelProvider     string
	modelID           string
	promptVersion     string
	maxIterations     int
	maxToolCalls      int
	maxTotalTokens    int
	maxContextRunes   int
	timeout           time.Duration
	sqlDataSourceID   uuid.UUID
}

type conversationEvaluationAssembly struct {
	runner                *mesagent.ConversationRunner
	recorder              *conversationToolRecorder
	toolSchemaFingerprint string
}

func buildConversationEvaluation(
	ctx context.Context,
	deps conversationEvaluationDependencies,
) (conversationEvaluationAssembly, error) {
	if deps.chatModel == nil || deps.externalCases == nil || deps.schemaSearcher == nil ||
		deps.readonlyExecutor == nil || deps.logger == nil {
		return conversationEvaluationAssembly{}, errors.New("conversation evaluation dependencies are incomplete")
	}
	if strings.TrimSpace(deps.systemInstruction) == "" || strings.TrimSpace(deps.modelProvider) == "" ||
		strings.TrimSpace(deps.modelID) == "" || strings.TrimSpace(deps.promptVersion) == "" {
		return conversationEvaluationAssembly{}, errors.New("conversation evaluation model identity is incomplete")
	}
	schemaTool, err := mesagent.NewSearchSchemaCatalogTool(deps.schemaSearcher)
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("build schema catalog Tool: %w", err)
	}
	queryTool, err := mesagent.NewExecuteReadonlyQueryTool(deps.readonlyExecutor)
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("build readonly query Tool: %w", err)
	}
	recorder := &conversationToolRecorder{}
	catalog, err := mesagent.NewConversationDefaultToolCatalog(ctx, mesagent.DefaultToolCatalogDependencies{
		ExternalCases: deps.externalCases,
		SchemaCatalog: &recordingConversationTool{name: mesagent.ToolSearchSchemaCatalog, inner: schemaTool, recorder: recorder},
		ReadonlyQuery: &recordingConversationTool{name: mesagent.ToolExecuteReadonlyQuery, inner: queryTool, recorder: recorder},
	})
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("build conversation Tool catalog: %w", err)
	}
	runner, err := mesagent.NewConversationRunner(mesagent.ConversationRunnerConfig{
		ChatModel: deps.chatModel, ToolCatalog: catalog,
		SystemInstruction: deps.systemInstruction,
		ModelProvider:     deps.modelProvider, ModelID: deps.modelID, PromptVersion: deps.promptVersion,
		Logger:        deps.logger,
		MaxIterations: deps.maxIterations, MaxToolCalls: deps.maxToolCalls,
		MaxTotalTokens: deps.maxTotalTokens, MaxContextRunes: deps.maxContextRunes,
		Timeout: deps.timeout, SQLDataSourceID: deps.sqlDataSourceID,
	})
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("build conversation runner: %w", err)
	}
	resolved, err := catalog.ResolveProfile(ctx, agentruntime.ToolProfileConversation)
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("resolve conversation Tool profile: %w", err)
	}
	fingerprint, err := mesagent.CanonicalToolContractFingerprint(ctx, resolved.Tools)
	if err != nil {
		return conversationEvaluationAssembly{}, fmt.Errorf("compute conversation Tool schema fingerprint: %w", err)
	}
	return conversationEvaluationAssembly{
		runner: runner, recorder: recorder, toolSchemaFingerprint: fingerprint,
	}, nil
}

// conversationEvaluationIdentity 是写入每条观测的 v2 身份字段。
type conversationEvaluationIdentity struct {
	modelProvider           string
	modelID                 string
	reasoningEffort         string
	promptVersion           string
	modelProfileFingerprint string
	implementationRevision  string
	implementationDirty     bool
	toolProfileID           string
	toolSchemaFingerprint   string
}

// observeConversationCase 通过真实 ConversationRunner 执行一个自然语言 case：
// 用户输入直接来自版本化数据集，模型自主决定 Tool 顺序与最终答案；观测记录
// 实际 Tool 顺序、生成 SQL 的 hash、执行结果、答案正确性和 Provider usage。
func observeConversationCase(
	ctx context.Context,
	assembly conversationEvaluationAssembly,
	definition mesagent.TextToSQLEvaluationCase,
	identity conversationEvaluationIdentity,
) texttosqleval.TextToSQLConversationEvaluationObservation {
	startedAt := time.Now()
	assembly.recorder.reset()
	userID := uuid.New()
	conversationID := uuid.New()
	messageID := uuid.New()
	current := conversation.Message{
		ID: messageID, ConversationID: conversationID, Seq: 1,
		Role: conversation.MessageRoleUser, Content: definition.UserQuery,
	}
	request := conversation.AgentRequest{
		Conversation: conversation.Conversation{
			ID: conversationID, UserID: userID, Status: conversation.StatusActive,
		},
		UserMessage: current,
		History:     []conversation.Message{current},
	}
	runCtx := conversation.WithCommandContext(ctx, conversation.CommandContext{
		ConversationID: conversationID, UserMessageID: messageID,
		Actor: conversation.Actor{UserID: userID},
	})
	response, respondErr := assembly.runner.Respond(runCtx, request)
	observation := texttosqleval.TextToSQLConversationEvaluationObservation{
		ObservationSchemaVersion: texttosqleval.TextToSQLConversationObservationSchemaVersion,
		EntryMode:                texttosqleval.TextToSQLConversationEntryMode,
		DatasetVersion:           definition.DatasetVersion,
		CaseID:                   definition.CaseID,
		RunID:                    definition.CaseID + "-" + uuid.NewString(),
		ModelProvider:            identity.modelProvider,
		ModelID:                  identity.modelID,
		ReasoningEffort:          identity.reasoningEffort,
		PromptVersion:            identity.promptVersion,
		ModelProfileFingerprint:  identity.modelProfileFingerprint,
		ImplementationRevision:   identity.implementationRevision,
		ImplementationDirty:      identity.implementationDirty,
		ToolProfileID:            identity.toolProfileID,
		ToolSchemaFingerprint:    identity.toolSchemaFingerprint,
		DurationMillis:           time.Since(startedAt).Milliseconds(),
	}
	records := assembly.recorder.snapshot()
	observation.ActualToolCalls = textToSQLConversationCalls(records)
	if response.RunObservation != nil {
		observation.Usage = conversationRunUsageToModelUsage(response.RunObservation.Usage)
		observation.ActualToolCallCount = response.RunObservation.ToolCalls
	}
	// Runner 的 ToolCalls 由执行中间件统计；在 RunAccess Guard 中提前
	// fail-closed 的 SQL 尝试不会进入该中间件，但 SQL 边界 recorder 已明确观测到。
	// 取两者较大值保留这类被拒绝的调用尝试；非 SQL Tool 仍会使 Runner
	// 总数大于 recorder 长度，并被标记为不完整轨迹。
	if len(observation.ActualToolCalls) > observation.ActualToolCallCount {
		observation.ActualToolCallCount = len(observation.ActualToolCalls)
	}
	observation.ToolTraceComplete, observation.ToolSequenceCorrect =
		texttosqleval.TextToSQLConversationToolTraceMatchesRequiredSequence(
			observation.ActualToolCallCount, observation.ActualToolCalls,
		)
	for _, record := range records {
		if record.name == mesagent.ToolExecuteReadonlyQuery && record.errorType == "invalid_tool_arguments" {
			observation.ErrorType = "invalid_tool_arguments"
			return observation
		}
	}
	if respondErr != nil && errors.Is(respondErr, mesagent.ErrToolNotAllowed) {
		// RunAccess Guard 在工具实现边界之前 fail-closed；此时 recorder
		// 看不到调用，但 Runner 观测已经给出更强的授权失败语义。
		observation.ErrorType = "tool_not_allowed"
		return observation
	}
	if !observation.ToolTraceComplete {
		observation.ErrorType = "unobserved_tool_call"
		return observation
	}
	if len(observation.ActualToolCalls) == 0 {
		observation.ErrorType = "no_sql_query"
		return observation
	}
	if !observation.ToolSequenceCorrect {
		observation.ErrorType = "invalid_tool_sequence"
		return observation
	}
	if respondErr != nil {
		observation.ErrorType = conversationEvaluationErrorType(respondErr)
		return observation
	}
	observation.Answer = response.Content
	var lastSQL *conversationToolRecord
	for index := range records {
		if records[index].name == mesagent.ToolExecuteReadonlyQuery {
			lastSQL = &records[index]
		}
	}
	if lastSQL == nil {
		observation.ErrorType = "no_sql_query"
		return observation
	}
	observation.GeneratedQuery = lastSQL.query
	observation.QueryHash = lastSQL.queryHash
	if !lastSQL.succeeded {
		observation.ErrorType = lastSQL.errorType
		return observation
	}
	observation.Columns = lastSQL.columns
	observation.Rows = lastSQL.rows
	observation.Truncated = lastSQL.truncated
	if strings.TrimSpace(observation.Answer) == "" {
		observation.ErrorType = "empty_answer"
		return observation
	}
	observation.ExecutionCorrect = mesagent.TextToSQLResultMatches(
		definition, observation.Columns, observation.Rows, observation.Truncated,
	)
	if !observation.ExecutionCorrect {
		observation.ErrorType = "result_mismatch"
		return observation
	}
	observation.AnswerCorrect = texttosqleval.TextToSQLAnswerMatchesExpectedValues(definition, observation.Answer)
	if !observation.AnswerCorrect {
		observation.ErrorType = "answer_mismatch"
		return observation
	}
	observation.Correct = true
	return observation
}

func conversationRunUsageToModelUsage(usage conversation.AgentRunUsage) mesagent.ModelUsage {
	return mesagent.ModelUsage{
		ModelCalls: usage.ModelCalls, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens,
	}
}

func conversationEvaluationErrorType(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "agent_timeout"
	case errors.Is(err, mesagent.ErrAgentToolRunLimitExhausted), errors.Is(err, mesagent.ErrToolCallBudgetExhausted):
		return "tool_call_budget_exhausted"
	case errors.Is(err, mesagent.ErrTokenBudgetExhausted):
		return "token_budget_exhausted"
	case errors.Is(err, mesagent.ErrConversationPromptWindowExceeded):
		return "prompt_window_exceeded"
	case errors.Is(err, mesagent.ErrToolNotAllowed):
		// RunAccess 在执行期拒绝（如缺 sql.read 或资源 Grant）：Runner 在
		// Tool 边界 fail-closed 中止回合，底层 executor 零调用。
		return "tool_not_allowed"
	default:
		return "agent_error"
	}
}

// ---------------------------------------------------------------------------
// Tool 调用记录层（只读观测，不改变执行语义）

type conversationToolRecord struct {
	name      string
	query     string
	queryHash string
	succeeded bool
	errorType string
	columns   []string
	rows      [][]any
	truncated bool
}

type conversationToolRecorder struct {
	mu    sync.Mutex
	calls []conversationToolRecord
}

func (r *conversationToolRecorder) reset() {
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
}

func (r *conversationToolRecorder) append(record conversationToolRecord) {
	r.mu.Lock()
	r.calls = append(r.calls, record)
	r.mu.Unlock()
}

func (r *conversationToolRecorder) snapshot() []conversationToolRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]conversationToolRecord(nil), r.calls...)
}

// recordingConversationTool 是模型-可见 Tool 边界上的只读观测层：委托给真实
// Tool（真实 SearchSchemaCatalogTool/ExecuteReadonlyQueryTool，其内是真实
// Executor 与 QueryGuard），同时按调用顺序记录名称、生成 SQL 的 hash 与
// 执行结果。评测 harness 自身绝不直接调用 SQL Tool。
type recordingConversationTool struct {
	name     string
	inner    tool.InvokableTool
	recorder *conversationToolRecorder
}

func (t *recordingConversationTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *recordingConversationTool) InvokableRun(
	ctx context.Context, argumentsInJSON string, opts ...tool.Option,
) (string, error) {
	output, err := t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
	record := conversationToolRecord{name: t.name, succeeded: err == nil}
	if t.name == mesagent.ToolExecuteReadonlyQuery {
		if arguments, decodeErr := decodeQueryArguments(argumentsInJSON); decodeErr == nil {
			record.query = arguments.Query
			record.queryHash = hashQuery(arguments.Query)
		} else {
			record.succeeded = false
			record.errorType = "invalid_tool_arguments"
		}
		if err != nil && record.errorType == "" {
			record.errorType = conversationSQLErrorType(err)
		} else {
			var result repository.ReadonlyQueryResult
			if unmarshalErr := json.Unmarshal([]byte(output), &result); unmarshalErr == nil {
				record.columns = result.Columns
				record.rows = result.Rows
				record.truncated = result.Truncated
			}
		}
	}
	t.recorder.append(record)
	return output, err
}

func textToSQLConversationCalls(records []conversationToolRecord) []texttosqleval.TextToSQLConversationToolCall {
	result := make([]texttosqleval.TextToSQLConversationToolCall, 0, len(records))
	for _, record := range records {
		call := texttosqleval.TextToSQLConversationToolCall{ToolName: record.name, Succeeded: record.succeeded}
		if record.name == mesagent.ToolExecuteReadonlyQuery {
			call.QueryHash = record.queryHash
		}
		if !record.succeeded {
			call.ErrorType = record.errorType
		}
		result = append(result, call)
	}
	return result
}

func conversationSQLErrorType(err error) string {
	switch {
	case errors.Is(err, platformsqlserver.ErrReadonlyQueryRejected):
		return "guard_rejected"
	case errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied):
		return "catalog_denied"
	default:
		return "execution_error"
	}
}

// ---------------------------------------------------------------------------
// conversation 模式固定夹具（与 direct 模式同一套只读数据源契约）

type unavailableExternalCaseGetter struct{}

func (unavailableExternalCaseGetter) Get(context.Context, uuid.UUID) (*externalcase.ExternalCase, error) {
	return nil, errors.New("external case reader is unavailable")
}

type fixedSchemaCatalogSearcher struct{}

func (fixedSchemaCatalogSearcher) SearchPublished(
	_ context.Context, _ uuid.UUID, keyword string, limit int,
) ([]repository.SchemaCatalogEntry, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	columns := []struct{ name, dataType, comment string }{
		{"TicketID", "nvarchar", "工单 ID"},
		{"CaseType", "nvarchar", "工单类型"},
		{"Title", "nvarchar", "标题"},
		{"Description", "nvarchar", "描述"},
		{"Category", "nvarchar", "分类"},
		{"Module", "nvarchar", "模块"},
		{"Status", "nvarchar", "状态"},
		{"Priority", "nvarchar", "优先级"},
		{"OccurredAt", "datetime", "发生时间"},
		{"ReportedAt", "datetime", "上报时间"},
		{"SourceUpdatedAt", "datetime", "来源更新时间"},
		{"CustomerCode", "nvarchar", "客户编码"},
		{"CustomerName", "nvarchar", "客户名称"},
		{"ProductCode", "nvarchar", "产品编码"},
		{"ProductName", "nvarchar", "产品名称"},
		{"ProductVersion", "nvarchar", "产品版本"},
		{"WorkOrderNo", "nvarchar", "工单号"},
		{"WorkpieceNo", "nvarchar", "工件号"},
		{"MaterialCode", "nvarchar", "物料编码"},
		{"BatchNo", "nvarchar", "批次号"},
		{"SerialNo", "nvarchar", "序列号"},
		{"FactoryCode", "nvarchar", "工厂编码"},
		{"WorkshopCode", "nvarchar", "车间编码"},
		{"ProductionLineCode", "nvarchar", "产线编码"},
		{"WorkstationCode", "nvarchar", "工位编码"},
		{"EquipmentCode", "nvarchar", "设备编码"},
		{"SourceSystem", "nvarchar", "来源系统"},
		{"DeploymentEnvironment", "nvarchar", "部署环境"},
		{"BusinessDatabaseAlias", "nvarchar", "业务库别名"},
		{"ReporterDepartment", "nvarchar", "上报部门"},
		{"ImpactScope", "nvarchar", "影响范围"},
	}
	entries := make([]repository.SchemaCatalogEntry, 0, limit)
	for _, column := range columns {
		if len(entries) >= limit {
			break
		}
		if keyword != "" && !strings.Contains(strings.ToLower(column.name), keyword) &&
			!strings.Contains(strings.ToLower(column.comment), keyword) {
			continue
		}
		entries = append(entries, repository.SchemaCatalogEntry{
			CatalogVersion: 1, ObjectSchema: "dbo", ObjectName: "v_MESGuardExternalCases",
			ObjectType: "VIEW", ColumnName: column.name, DataType: column.dataType,
			Comment: column.comment, SensitivityLevel: "internal",
		})
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// direct 模式（历史能力测试，语义保持不变）

func observeTextToSQL(
	ctx context.Context,
	chatModel model.ToolCallingChatModel,
	executor repository.ReadonlyQueryExecutor,
	cfg config.Config,
	dataSourceID uuid.UUID,
	definition mesagent.TextToSQLEvaluationCase,
) mesagent.TextToSQLEvaluationObservation {
	startedAt := time.Now()
	profile, _ := cfg.Models.Chat.ActiveProfile()
	observation := mesagent.TextToSQLEvaluationObservation{
		DatasetVersion:  definition.DatasetVersion,
		CaseID:          definition.CaseID,
		RunID:           definition.CaseID + "-" + uuid.NewString(),
		ModelProvider:   profile.Provider,
		ModelID:         profile.Model,
		ReasoningEffort: profile.ReasoningEffort,
		PromptVersion:   textToSQLPromptVersion,
	}
	message, generateErr := chatModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage(textToSQLSystemPrompt(dataSourceID)),
		schema.UserMessage(definition.UserQuery),
	}, model.WithTemperature(0), model.WithMaxTokens(textToSQLMaxTokens), model.WithToolChoice(schema.ToolChoiceForced))
	observation.DurationMillis = time.Since(startedAt).Milliseconds()
	if generateErr != nil {
		observation.ErrorType = "model_error"
		return observation
	}
	if message == nil {
		observation.ErrorType = "empty_model_response"
		return observation
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		observation.ErrorType = "missing_provider_usage"
		return observation
	}
	usage := message.ResponseMeta.Usage
	observation.Usage = mesagent.ModelUsage{
		ModelCalls: 1, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
	observation.ToolCallCount = len(message.ToolCalls)
	if len(message.ToolCalls) != 1 {
		observation.ErrorType = "invalid_tool_call_count"
		return observation
	}
	call := message.ToolCalls[0]
	observation.SelectedTool = call.Function.Name
	if call.Function.Name != mesagent.ToolExecuteReadonlyQuery {
		observation.ErrorType = "unexpected_tool"
		return observation
	}
	arguments, err := decodeQueryArguments(call.Function.Arguments)
	if err != nil {
		observation.ErrorType = "invalid_tool_arguments"
		return observation
	}
	if arguments.DataSourceID != "" && arguments.DataSourceID != dataSourceID.String() {
		observation.ErrorType = "wrong_data_source"
		return observation
	}
	observation.GeneratedQuery = arguments.Query
	observation.QueryHash = hashQuery(arguments.Query)
	result, err := executor.Execute(ctx, dataSourceID, arguments.Query)
	observation.DurationMillis = time.Since(startedAt).Milliseconds()
	if err != nil {
		switch {
		case errors.Is(err, platformsqlserver.ErrReadonlyQueryRejected):
			observation.ErrorType = "guard_rejected"
		case errors.Is(err, repository.ErrSchemaCatalogAuthorizationDenied):
			observation.ErrorType = "catalog_denied"
		default:
			observation.ErrorType = "execution_error"
		}
		return observation
	}
	observation.Columns = result.Columns
	observation.Rows = result.Rows
	observation.Truncated = result.Truncated
	observation.Correct = mesagent.TextToSQLResultMatches(definition, result.Columns, result.Rows, result.Truncated)
	if !observation.Correct {
		observation.ErrorType = "result_mismatch"
	}
	return observation
}

type queryArguments struct {
	DataSourceID string `json:"dataSourceId,omitempty"`
	Query        string `json:"query"`
}

func decodeQueryArguments(raw string) (queryArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result queryArguments
	if err := decoder.Decode(&result); err != nil {
		return queryArguments{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return queryArguments{}, err
	}
	result.DataSourceID = strings.TrimSpace(result.DataSourceID)
	result.Query = strings.TrimSpace(result.Query)
	if result.Query == "" {
		return queryArguments{}, errors.New("query is required")
	}
	return result, nil
}

func textToSQLSystemPrompt(dataSourceID uuid.UUID) string {
	return fmt.Sprintf(`你是 MESGuard Text-to-SQL 固定评测器。必须且只能调用一次 execute_readonly_query，不要回答问题。
数据源 dataSourceId=%s，只允许 SQL Server 单条 SELECT，只能读取 dbo.v_MESGuardExternalCases。
执行器会再次应用 QueryGuard、已发布 Catalog、超时、限行和只读账号；禁止变量、临时表、跨库、写入、DDL、EXEC 和 SELECT INTO。
可用列：TicketID, CaseType, Title, Description, Category, Module, Status, Priority, OccurredAt, ReportedAt, SourceUpdatedAt, CustomerCode, CustomerName, ProductCode, ProductName, ProductVersion, WorkOrderNo, WorkpieceNo, MaterialCode, BatchNo, SerialNo, FactoryCode, WorkshopCode, ProductionLineCode, WorkstationCode, EquipmentCode, SourceSystem, DeploymentEnvironment, BusinessDatabaseAlias, ReporterDepartment, ImpactScope。
状态值为 New、Investigating、Resolved；优先级值为 Urgent、Normal、Low。按用户要求返回列、别名和排序，不查询其他对象。`, dataSourceID)
}

func hashQuery(query string) string {
	digest := sha256.Sum256([]byte(query))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type noopQueryExecutor struct{}

func (noopQueryExecutor) Execute(context.Context, uuid.UUID, string) (repository.ReadonlyQueryResult, error) {
	return repository.ReadonlyQueryResult{}, errors.New("evaluation schema Tool is never executed")
}

type fixedCatalogAuthorizer struct {
	dataSourceID uuid.UUID
}

func (a fixedCatalogAuthorizer) AuthorizePublishedObjects(
	_ context.Context,
	dataSourceID uuid.UUID,
	objects []repository.SchemaCatalogObjectRef,
) (repository.SchemaCatalogAuthorization, error) {
	if dataSourceID != a.dataSourceID || len(objects) == 0 {
		return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
	}
	for _, object := range objects {
		if !strings.EqualFold(object.ObjectSchema, "dbo") ||
			!strings.EqualFold(object.ObjectName, "v_MESGuardExternalCases") {
			return repository.SchemaCatalogAuthorization{}, repository.ErrSchemaCatalogAuthorizationDenied
		}
	}
	return repository.SchemaCatalogAuthorization{
		CatalogVersionID: textToSQLCatalogVersionID,
		CatalogVersion:   1,
		Objects:          append([]repository.SchemaCatalogObjectRef(nil), objects...),
	}, nil
}

func readTextToSQLCases(path string) ([]mesagent.TextToSQLEvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dataset: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []mesagent.TextToSQLEvaluationCase
	for line := 1; scanner.Scan(); line++ {
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		var current mesagent.TextToSQLEvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values on one line")
			}
			return nil, fmt.Errorf("decode line %d: %w", line, err)
		}
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("validate line %d: %w", line, err)
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

func writeTextToSQLEvaluationFiles(
	outputPath, summaryPath string,
	observations []mesagent.TextToSQLEvaluationObservation,
	summary mesagent.TextToSQLEvaluationSummary,
) error {
	return writeEvaluationFiles(outputPath, summaryPath, observations, summary)
}

func writeTextToSQLConversationEvaluationFiles(
	outputPath, summaryPath string,
	observations []texttosqleval.TextToSQLConversationEvaluationObservation,
	summary texttosqleval.TextToSQLConversationEvaluationSummary,
) error {
	return writeEvaluationFiles(outputPath, summaryPath, observations, summary)
}

func writeEvaluationFiles[T any](outputPath, summaryPath string, observations []T, summary any) error {
	outputTemp, err := os.CreateTemp("", "mesguard-text2sql-observations-*")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(outputTemp)
	for _, observation := range observations {
		if err := encoder.Encode(observation); err != nil {
			_ = outputTemp.Close()
			return err
		}
	}
	if err := outputTemp.Close(); err != nil {
		return err
	}
	summaryTemp, err := os.CreateTemp("", "mesguard-text2sql-summary-*")
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		_ = summaryTemp.Close()
		return err
	}
	if _, err := summaryTemp.Write(append(encoded, '\n')); err != nil {
		_ = summaryTemp.Close()
		return err
	}
	if err := summaryTemp.Close(); err != nil {
		return err
	}
	if err := replaceEvaluationFile(outputTemp.Name(), outputPath); err != nil {
		return err
	}
	return replaceEvaluationFile(summaryTemp.Name(), summaryPath)
}

func replaceEvaluationFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
