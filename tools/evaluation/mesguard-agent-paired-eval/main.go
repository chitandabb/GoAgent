// Command mesguard-agent-paired-eval 对同一版本化样本顺序运行 baseline 和 experiment。
// 输出是可交给 mesguard-agent-eval 汇总的真实 EvaluationObservation JSONL。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/externalcase"
	platformchatmodel "github.com/chitandabb/GoAgent/internal/platform/chatmodel"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	"github.com/chitandabb/GoAgent/internal/platform/githubmcp"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"
	platformsqlserver "github.com/chitandabb/GoAgent/internal/platform/sqlserver"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var pairedEvalCaseID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

const evidenceGateReviewedCaseTargetForProviderRun = 30

type pairedEvalCaseGetter struct{}

func (pairedEvalCaseGetter) Get(_ context.Context, id uuid.UUID) (*externalcase.ExternalCase, error) {
	if id != pairedEvalCaseID {
		return nil, errors.New("paired evaluation synthetic case not found")
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	return &externalcase.ExternalCase{
		ID: id, ExternalCaseKey: "EVAL-001", CaseType: "incident",
		Title:       "工位报工后状态未更新",
		Description: "操作员完成报工，ERP 工单仍显示处理中，现场网络正常。",
		Category:    "workflow", Module: "work-reporting",
		Status: externalcase.StatusOpen, Priority: externalcase.PriorityMedium,
		ReportedAt: now, SourceUpdatedAt: now,
		SourceFingerprint: "agent-evaluation-real-v1",
	}, nil
}

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-agent-paired-eval")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], log); err != nil {
		log.Error("Agent paired evaluation failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *zap.Logger) error {
	flags := flag.NewFlagSet("mesguard-agent-paired-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "testdata/agent-evaluation.real-v1.jsonl", "versioned JSONL evaluation cases")
	outputPath := flags.String("output", "testdata/agent-evaluation.real-v1.observations.jsonl", "output JSONL observations")
	reasoningEffort := flags.String("reasoning-effort", "", "provider-supported effort; defaults to config")
	maxTotalTokens := flags.Int("max-total-tokens", 0, "override the Evidence Gate total token budget; defaults to config")
	comparison := flags.String("comparison", "tool-selection", "paired variable: tool-selection or evidence-gate")
	allowProviderCalls := flags.Bool("allow-provider-calls", false, "explicitly authorize Provider calls for evidence-gate comparison")
	maxCases := flags.Int("max-cases", 0, "maximum evidence-gate cases authorized for this Provider run")
	maxProviderCalls := flags.Int("max-provider-calls", 0, "maximum estimated Provider calls authorized for this evidence-gate run")
	maxProviderTokens := flags.Int("max-provider-tokens", 0, "maximum total Token budget authorized across both evidence-gate arms")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("usage: mesguard-agent-paired-eval [-dataset path] [-output path] [-reasoning-effort provider-value]: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *comparison != "tool-selection" && *comparison != "evidence-gate" {
		return errors.New("comparison must be tool-selection or evidence-gate")
	}

	cases, err := readEvaluationCases(*datasetPath)
	if err != nil {
		return fmt.Errorf("read evaluation dataset: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Models.Chat.Enabled {
		return errors.New("chat model is disabled")
	}
	if !cfg.GitHubMCP.Enabled {
		return errors.New("github MCP must be enabled for paired evaluation")
	}
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*reasoningEffort) != "" {
		profile.ReasoningEffort = strings.ToLower(strings.TrimSpace(*reasoningEffort))
	}
	if _, err := parseReasoningEffort(profile.ReasoningEffort); err != nil {
		return err
	}
	cfg.Models.Chat.Profiles[cfg.Models.Chat.ActiveProfileName] = profile
	if *maxTotalTokens != 0 {
		if *maxTotalTokens < 1000 || *maxTotalTokens > 1_000_000 {
			return errors.New("max-total-tokens must be between 1000 and 1000000")
		}
		cfg.Agent.MaxTotalTokens = *maxTotalTokens
	}
	if *comparison == "evidence-gate" {
		budget, budgetErr := validateEvidenceGateProviderBudget(
			len(cases), cfg.Agent.MaxAgentRuns, cfg.Agent.MaxToolCalls, cfg.Agent.MaxTotalTokens,
			*allowProviderCalls, *maxCases, *maxProviderCalls, *maxProviderTokens,
		)
		if budgetErr != nil {
			return budgetErr
		}
		log.Info("Evidence Gate Provider paired run authorized",
			zap.Int("cases", budget.Cases),
			zap.Int("estimated_provider_call_upper_bound", budget.ProviderCalls),
			zap.Int("embedding_request_upper_bound", 0),
			zap.Int("rerank_request_upper_bound", 0),
			zap.Int("total_token_budget_upper_bound", budget.TotalTokens),
		)
	}
	prompts, err := cfg.Agent.LoadPrompts()
	if err != nil {
		return fmt.Errorf("load Agent prompts: %w", err)
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

	var sqlDB *sql.DB
	var sqlObjectDefinition tool.BaseTool
	var schemaCatalog tool.BaseTool
	var readonlyQuery tool.BaseTool
	var closePostgres func() error
	var sqlCatalogFixture *sqlCatalogEvaluationFixture
	sqlEnabled := evaluationDatasetHasTag(cases, "sql-enabled") || evaluationDatasetHasTag(cases, "sql-query-enabled")
	if sqlEnabled {
		if !cfg.SQLServer.Enabled {
			return errors.New("SQL Server must be enabled for SQL evaluation cases")
		}
		sqlDB, err = platformsqlserver.Open(ctx, cfg.SQLServer)
		if err != nil {
			return fmt.Errorf("open SQL Server for paired evaluation: %w", err)
		}
		defer sqlDB.Close()
		if err := sqlDB.PingContext(ctx); err != nil {
			return fmt.Errorf("ping SQL Server for paired evaluation: %w", err)
		}
		reader, readerErr := platformsqlserver.NewObjectDefinitionReader(sqlDB, cfg.SQLServer, log.Named("sqlserver_investigation"))
		if readerErr != nil {
			return fmt.Errorf("build SQL object definition reader: %w", readerErr)
		}
		sqlObjectDefinition, err = mesagent.NewDatabaseObjectDefinitionTool(reader)
		if err != nil {
			return fmt.Errorf("build SQL object definition Tool: %w", err)
		}
	}
	if evaluationDatasetHasTag(cases, "sql-query-enabled") {
		postgresDB, postgresClose, postgresErr := platformpostgres.Open(ctx, cfg.Postgres, log.Named("postgres_eval"))
		if postgresErr != nil {
			return fmt.Errorf("open PostgreSQL for SQL Catalog evaluation: %w", postgresErr)
		}
		closePostgres = postgresClose
		defer func() { _ = closePostgres() }()
		sqlDataSourceID, parseErr := uuid.Parse(cfg.SQLServer.ID)
		if parseErr != nil {
			return fmt.Errorf("parse SQL data source id for Catalog evaluation: %w", parseErr)
		}
		sqlCatalogFixture, err = beginSQLCatalogEvaluationFixture(ctx, postgresDB, sqlDataSourceID)
		if err != nil {
			return fmt.Errorf("begin SQL Catalog evaluation fixture: %w", err)
		}
		defer func() { _ = sqlCatalogFixture.Rollback() }()
		catalog := platformpostgres.NewSchemaCatalogRepository(sqlCatalogFixture.DB())
		schemaCatalog, err = mesagent.NewSearchSchemaCatalogTool(catalog)
		if err != nil {
			return fmt.Errorf("build SQL Catalog search Tool: %w", err)
		}
		readonlyExecutor, executorErr := platformsqlserver.NewReadonlyQueryExecutor(
			sqlDB, cfg.SQLServer, catalog, log.Named("sqlserver_readonly_query"),
		)
		if executorErr != nil {
			return fmt.Errorf("build readonly query executor: %w", executorErr)
		}
		readonlyQuery, err = mesagent.NewExecuteReadonlyQueryTool(readonlyExecutor)
		if err != nil {
			return fmt.Errorf("build readonly query Tool: %w", err)
		}
	}

	var output *os.File
	if *comparison == "evidence-gate" {
		output, err = os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	} else {
		output, err = os.Create(*outputPath)
	}
	if err != nil {
		return fmt.Errorf("create observations output: %w", err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)

	for _, definition := range cases {
		if definition.TaskType != mesagent.TaskTypeDiagnosis {
			return fmt.Errorf("case %q uses unsupported paired evaluation task type %q", definition.CaseID, definition.TaskType)
		}
		scope, scopeErr := newPairedEvaluationScope(definition, cfg)
		if scopeErr != nil {
			return fmt.Errorf("build scope for case %q: %w", definition.CaseID, scopeErr)
		}
		pairingFingerprint, fingerprintErr := evidenceGatePairingFingerprint(definition, cfg)
		if fingerprintErr != nil {
			return fmt.Errorf("fingerprint case %q: %w", definition.CaseID, fingerprintErr)
		}
		for _, variant := range []mesagent.EvaluationVariant{mesagent.EvaluationBaseline, mesagent.EvaluationExperiment} {
			if err := ctx.Err(); err != nil {
				return err
			}
			orchestrator, buildErr := buildPairedEvaluationRun(
				ctx, cfg, prompts, chatModel, githubConnection.Tools, sqlObjectDefinition,
				schemaCatalog, readonlyQuery, log, variant, *comparison,
			)
			if buildErr != nil {
				return fmt.Errorf("build %s run for case %q: %w", variant, definition.CaseID, buildErr)
			}
			startedAt := time.Now()
			result, invokeErr := orchestrator.Invoke(
				mesagent.WithTaskScope(ctx, scope),
				mesagent.RunRequest{
					UserQuery: definition.UserQuery, ExternalCaseID: pairedEvalCaseID.String(),
					RequestedSkill: definition.ExpectedSkill,
				},
			)
			if invokeErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if *comparison != "evidence-gate" {
					return fmt.Errorf("invoke %s run for case %q: %w", variant, definition.CaseID, invokeErr)
				}
			}
			duration := time.Since(startedAt)
			if *comparison == "evidence-gate" {
				observation := evidenceGateObservationFromResult(
					definition, variant, cfg, pairingFingerprint, result, duration, invokeErr,
				)
				if err := observation.Validate(); err != nil {
					return fmt.Errorf("validate %s Evidence Gate observation for case %q: %w", variant, definition.CaseID, err)
				}
				if err := encoder.Encode(observation); err != nil {
					return fmt.Errorf("write %s Evidence Gate observation for case %q: %w", variant, definition.CaseID, err)
				}
				if invokeErr != nil {
					log.Warn("Evidence Gate paired arm failed and was recorded",
						zap.String("case_id", definition.CaseID),
						zap.String("variant", string(variant)),
						zap.String("error_type", observation.ErrorType),
					)
					continue
				}
			} else {
				observation := observationFromResult(definition, variant, cfg, result, duration)
				if err := observation.Validate(); err != nil {
					return fmt.Errorf("validate %s observation for case %q: %w", variant, definition.CaseID, err)
				}
				if err := encoder.Encode(observation); err != nil {
					return fmt.Errorf("write %s observation for case %q: %w", variant, definition.CaseID, err)
				}
			}
			log.Info("Agent paired evaluation run completed",
				zap.String("case_id", definition.CaseID), zap.String("variant", string(variant)),
				zap.Int("total_tokens", result.Usage.TotalTokens), zap.Int("tool_calls", len(result.ToolExecutions)),
				zap.Duration("duration", time.Since(startedAt)),
			)
		}
	}
	return nil
}

func buildPairedEvaluationRun(
	ctx context.Context,
	cfg config.Config,
	prompts config.AgentPrompts,
	chatModel model.ToolCallingChatModel,
	githubTools []tool.BaseTool,
	sqlObjectDefinition tool.BaseTool,
	schemaCatalog tool.BaseTool,
	readonlyQuery tool.BaseTool,
	log *zap.Logger,
	variant mesagent.EvaluationVariant,
	comparison string,
) (*mesagent.EvidenceOrchestrator, error) {
	mode := mesagent.RunnerModeExperiment
	if comparison == "tool-selection" && variant == mesagent.EvaluationBaseline {
		mode = mesagent.RunnerModeBaseline
	}
	runner, err := mesagent.NewDefaultRunner(ctx, mesagent.DefaultRunnerDependencies{
		ChatModel: chatModel, ExternalCases: pairedEvalCaseGetter{},
		SkillRoot:           cfg.Agent.SkillsDirectory,
		SystemInstruction:   prompts.SystemInstruction,
		BaselineInstruction: prompts.BaselineInstruction,
		Mode:                mode,
		GitHubTools:         githubTools, GitHubArgumentRewrite: githubmcp.NewArgumentRewriter(),
		SQLObjectDefinitions: sqlObjectDefinition,
		SchemaCatalog:        schemaCatalog, ReadonlyQuery: readonlyQuery,
		Logger: log.Named(string(variant)),
	})
	if err != nil {
		return nil, fmt.Errorf("build Agent runner: %w", err)
	}
	orchestrator, err := mesagent.NewEvidenceOrchestrator(ctx, mesagent.EvidenceOrchestratorConfig{
		Runner: runner, Logger: log.Named("evidence_" + string(variant)),
		DisableEarlyExit: comparison == "evidence-gate" && variant == mesagent.EvaluationBaseline,
		MaxAgentRuns:     cfg.Agent.MaxAgentRuns, MaxToolCalls: cfg.Agent.MaxToolCalls,
		MaxEvidenceItems: cfg.Agent.MaxEvidenceItems, MaxTotalTokens: cfg.Agent.MaxTotalTokens,
		Timeout:                   time.Duration(cfg.Agent.TimeoutMillis) * time.Millisecond,
		ReportContractInstruction: prompts.ReportContractInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("build Evidence orchestrator: %w", err)
	}
	return orchestrator, nil
}

type evidenceGateProviderBudget struct {
	Cases         int
	ProviderCalls int
	TotalTokens   int
}

func validateEvidenceGateProviderBudget(
	cases int,
	maxAgentRuns int,
	maxToolCalls int,
	maxTotalTokens int,
	allowed bool,
	caseLimit int,
	providerCallLimit int,
	totalTokenLimit int,
) (evidenceGateProviderBudget, error) {
	if !allowed {
		return evidenceGateProviderBudget{}, errors.New("evidence-gate Provider run requires -allow-provider-calls")
	}
	if cases < 1 || caseLimit < 1 || providerCallLimit < 1 || totalTokenLimit < 1 {
		return evidenceGateProviderBudget{}, errors.New("evidence-gate Provider run requires positive -max-cases, -max-provider-calls, and -max-provider-tokens")
	}
	if cases > evidenceGateReviewedCaseTargetForProviderRun || caseLimit > evidenceGateReviewedCaseTargetForProviderRun {
		return evidenceGateProviderBudget{}, fmt.Errorf("evidence-gate Provider run is capped at %d reviewed cases", evidenceGateReviewedCaseTargetForProviderRun)
	}
	if cases > caseLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("dataset has %d cases, exceeds authorized max-cases %d", cases, caseLimit)
	}
	if maxAgentRuns < 1 || maxToolCalls < 1 || maxTotalTokens < 1 {
		return evidenceGateProviderBudget{}, errors.New("effective Agent run, Tool call, and Token budgets must be positive")
	}
	budget := evidenceGateProviderBudget{
		Cases: cases,
		// One model turn produces either a final answer or one Tool call. This is a
		// conservative upper bound across both paired arms.
		ProviderCalls: cases * 2 * (maxAgentRuns + maxToolCalls),
		TotalTokens:   cases * 2 * maxTotalTokens,
	}
	if budget.ProviderCalls > providerCallLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("estimated Provider call upper bound %d exceeds authorized max-provider-calls %d", budget.ProviderCalls, providerCallLimit)
	}
	if budget.TotalTokens > totalTokenLimit {
		return evidenceGateProviderBudget{}, fmt.Errorf("total Token budget upper bound %d exceeds authorized max-provider-tokens %d", budget.TotalTokens, totalTokenLimit)
	}
	return budget, nil
}

func evidenceGatePairingFingerprint(definition mesagent.EvaluationCase, cfg config.Config) (string, error) {
	profile, err := cfg.Models.Chat.ActiveProfile()
	if err != nil {
		return "", err
	}
	payload := struct {
		Case             mesagent.EvaluationCase `json:"case"`
		ModelProvider    string                  `json:"modelProvider"`
		ModelID          string                  `json:"modelId"`
		ModelProfile     string                  `json:"modelProfile"`
		ReasoningEffort  string                  `json:"reasoningEffort"`
		PromptVersion    string                  `json:"promptVersion"`
		MaxAgentRuns     int                     `json:"maxAgentRuns"`
		MaxToolCalls     int                     `json:"maxToolCalls"`
		MaxEvidenceItems int                     `json:"maxEvidenceItems"`
		MaxTotalTokens   int                     `json:"maxTotalTokens"`
		TimeoutMillis    int                     `json:"timeoutMillis"`
	}{
		Case: definition, ModelProvider: profile.Provider, ModelID: profile.Model,
		ModelProfile: cfg.Models.Chat.ActiveProfileName, ReasoningEffort: profile.ReasoningEffort,
		PromptVersion: cfg.Agent.PromptVersion, MaxAgentRuns: cfg.Agent.MaxAgentRuns,
		MaxToolCalls: cfg.Agent.MaxToolCalls, MaxEvidenceItems: cfg.Agent.MaxEvidenceItems,
		MaxTotalTokens: cfg.Agent.MaxTotalTokens, TimeoutMillis: cfg.Agent.TimeoutMillis,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func evidenceGateObservationFromResult(
	definition mesagent.EvaluationCase,
	variant mesagent.EvaluationVariant,
	cfg config.Config,
	pairingFingerprint string,
	result mesagent.OrchestrationResult,
	duration time.Duration,
	invokeErr error,
) mesagent.EvidenceGateEvaluationObservation {
	profile, _ := cfg.Models.Chat.ActiveProfile()
	reasoningEffort := strings.TrimSpace(profile.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = "none"
	}
	errorType := ""
	var degradationReasons []string
	if invokeErr != nil {
		errorType = evidenceGateInvocationErrorType(invokeErr)
		degradationReasons = []string{"invoke_failed_before_report"}
	} else if result.Partial {
		errorType = strings.TrimSpace(result.StopReason)
		if errorType == "" {
			errorType = "evidence_gate_partial"
		}
		degradationReasons = append(degradationReasons, result.MissingEvidence...)
	}
	return mesagent.EvidenceGateEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID,
		Variant: variant, RunID: fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		EarlyExitEnabled:   variant == mesagent.EvaluationExperiment,
		PairingFingerprint: pairingFingerprint, ModelProvider: profile.Provider, ModelID: profile.Model,
		ModelProfile: cfg.Models.Chat.ActiveProfileName, PromptVersion: cfg.Agent.PromptVersion,
		ReasoningEffort: reasoningEffort, AgentRuns: result.AgentRuns,
		Completed:       invokeErr == nil && !result.Partial,
		QualityReviewed: false, Usage: result.Usage, ToolCalls: len(result.ToolExecutions),
		DurationMillis: duration.Milliseconds(), ErrorType: errorType,
		DegradationReasons: degradationReasons,
	}
}

func evidenceGateInvocationErrorType(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "provider_or_orchestration_error"
	}
}

func newPairedEvaluationScope(definition mesagent.EvaluationCase, cfg config.Config) (mesagent.TaskScope, error) {
	dependencies := []mesagent.ToolDependency{mesagent.ToolDependencyExternalCase}
	dataSources := []mesagent.ScopedDataSource{{
		ID:   uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		Role: mesagent.DataSourceRoleCaseSource, SafetyMode: mesagent.DataSourceSafetyReadOnly,
	}}
	capabilities := []mesagent.ToolCapability{mesagent.ToolCapabilityCase}
	if containsString(definition.Tags, "github-enabled") {
		dependencies = append(dependencies, mesagent.ToolDependencyGitHubMCP)
		capabilities = append(capabilities, mesagent.ToolCapabilityCode)
	}
	if containsString(definition.Tags, "sql-enabled") || containsString(definition.Tags, "sql-query-enabled") {
		dataSourceID, err := uuid.Parse(cfg.SQLServer.ID)
		if err != nil {
			return mesagent.TaskScope{}, fmt.Errorf("parse SQL data source id: %w", err)
		}
		dependencies = append(dependencies, mesagent.ToolDependencySQLServer)
		capabilities = append(capabilities, mesagent.ToolCapabilitySQL)
		dataSources = append(dataSources, mesagent.ScopedDataSource{
			ID: dataSourceID, Role: mesagent.DataSourceRoleProduction,
			SafetyMode: mesagent.DataSourceSafetyReadOnly,
		})
	}
	return mesagent.NewTaskScope(mesagent.TaskScopeConfig{
		UserID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Role:   auth.RoleAnalyst, TaskType: mesagent.TaskTypeDiagnosis,
		DataSources:           dataSources,
		AllowedCapabilities:   capabilities,
		AvailableDependencies: dependencies,
	})
}

func evaluationDatasetHasTag(cases []mesagent.EvaluationCase, tag string) bool {
	for _, definition := range cases {
		if containsString(definition.Tags, tag) {
			return true
		}
	}
	return false
}

func observationFromResult(
	definition mesagent.EvaluationCase,
	variant mesagent.EvaluationVariant,
	cfg config.Config,
	result mesagent.OrchestrationResult,
	duration time.Duration,
) mesagent.EvaluationObservation {
	profile, _ := cfg.Models.Chat.ActiveProfile()
	actualTools := make([]string, 0, len(result.ToolExecutions))
	for _, execution := range result.ToolExecutions {
		actualTools = append(actualTools, execution.Name)
	}
	evidence := make([]string, 0, len(result.EvidenceItems))
	for _, item := range result.EvidenceItems {
		value := string(item.SourceType)
		if value != "" && !containsString(evidence, value) {
			evidence = append(evidence, value)
		}
	}
	return mesagent.EvaluationObservation{
		DatasetVersion:   definition.DatasetVersion,
		CaseID:           definition.CaseID,
		Variant:          variant,
		RunID:            fmt.Sprintf("%s-%s-%s", definition.CaseID, variant, uuid.NewString()),
		Model:            profile.Provider,
		ModelVersion:     profile.Model,
		ReasoningEffort:  profile.ReasoningEffort,
		PromptVersion:    cfg.Agent.PromptVersion,
		SelectedSkill:    result.SelectedSkill,
		ActualToolCalls:  actualTools,
		AllowedTools:     result.AllowedTools,
		Evidence:         evidence,
		Limitations:      result.Report.Limitations,
		ConclusionStatus: result.Report.ConclusionStatus,
		Partial:          result.Partial,
		Usage:            result.Usage,
		DurationMillis:   duration.Milliseconds(),
		ErrorType:        result.StopReason,
	}
}

func readEvaluationCases(path string) ([]mesagent.EvaluationCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []mesagent.EvaluationCase
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var current mesagent.EvaluationCase
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := ensureEvaluationJSONEOF(decoder); err != nil {
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
		return nil, errors.New("evaluation dataset contains no cases")
	}
	return result, nil
}

func ensureEvaluationJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values on one line")
}

func parseReasoningEffort(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "low", "medium", "high", "xhigh", "max":
		return normalized, nil
	default:
		return "", errors.New("reasoning-effort must be empty, low, medium, high, xhigh, or max")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
